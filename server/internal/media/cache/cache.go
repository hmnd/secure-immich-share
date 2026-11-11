// Package cache stores verified media on disk and runs the jobs that
// produce it: single-flight per key, a bounded sanitize semaphore
// with visitor and prewarm queues, failure memory, reader counts that
// protect files in use from LRU eviction, and startup recovery.
package cache

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/hmnd/secure-immich-share/server/internal/clock"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
)

var (
	ErrBusy    = errors.New("cache: sanitize queue full")
	ErrFailed  = errors.New("cache: job failed")
	ErrClosed  = errors.New("cache: shutting down")
	ErrSkipped = errors.New("cache: prewarm skipped under load")
)

type Key string

// KeyFor derives the cache key. checksum is the original's hash (unchanged
// by Immich edits); updatedAt changes with edits.
func KeyFor(assetID, checksum string, updatedAt time.Time, variant sanitize.Variant, pipeline string) Key {
	h := sha256.New()
	for _, part := range []string{assetID, checksum, updatedAt.UTC().Format(time.RFC3339Nano), string(variant), pipeline} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return Key(hex.EncodeToString(h.Sum(nil)))
}

// Result is what a job produced.
type Result struct {
	Verified sanitize.Verified   // set on success
	Fail     sanitize.FailReason // set on failure
}

// Job produces a verified file in TmpDir. It calls nextStep each time it
// moves to the next step of its source chain.
type Job func(ctx context.Context, nextStep func()) Result

type Options struct {
	Dir                string
	MaxBytes           int64
	Concurrency        int
	Queue              int
	PrewarmConcurrency int
	FailedTTL          time.Duration
	Pipeline           string
	Runner             *sanitize.Runner // re-verifies entries recovered at startup
	Clock              clock.Clock
	Logger             *slog.Logger
}

// upstreamFailedTTL is how long a job that failed on an Immich outage is
// remembered.
const upstreamFailedTTL = 30 * time.Second

type Cache struct {
	o      Options
	tmp    string
	base   context.Context
	cancel context.CancelFunc
	verify singleflight.Group

	mu             sync.Mutex
	entries        map[Key]*entry
	lru            *list.List // committed and recovered entries, front = most recent
	visitorQ       []*entry
	prewarmQ       []*entry
	running        int
	prewarmRunning int
	total          int64
	closed         bool // no new jobs; base is cancelled after the grace period
	wg             sync.WaitGroup
}

type entry struct {
	key  Key
	st   State
	job  Job
	done chan struct{} // closed when the queued or running job ends

	// The file, while committed or recovered.
	v         sanitize.Verified // committed only
	path      string
	container sanitize.Container
	size      int64
	readers   int
	elem      *list.Element
}

type sidecar struct {
	Container sanitize.Container `json:"kind"`
	Size      int64              `json:"size"`
	Pipeline  string             `json:"pipeline"`
}

// Open prepares the cache directory: tmp/ is emptied and committed entries
// are recovered (file plus a sidecar for the current pipeline); anything
// else is deleted.
func Open(o Options) (*Cache, error) {
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	if o.Queue < 1 {
		o.Queue = 1
	}
	if o.PrewarmConcurrency < 1 {
		o.PrewarmConcurrency = 1
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	c := &Cache{o: o, tmp: filepath.Join(o.Dir, "tmp"), entries: map[Key]*entry{}, lru: list.New()}
	c.base, c.cancel = context.WithCancel(context.Background())
	if err := os.MkdirAll(o.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	if err := os.RemoveAll(c.tmp); err != nil {
		return nil, fmt.Errorf("cache: clean tmp: %w", err)
	}
	if err := os.MkdirAll(c.tmp, 0o750); err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	if err := c.recover(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Cache) TmpDir() string { return c.tmp }

func (c *Cache) recover() error {
	type found struct {
		key  Key
		meta sidecar
		data string
		mod  time.Time
	}
	var keep []found
	err := filepath.WalkDir(c.o.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == c.tmp {
				return filepath.SkipDir
			}
			return nil
		}
		dir := filepath.Dir(p)
		if filepath.Dir(dir) != filepath.Clean(c.o.Dir) {
			return os.Remove(p)
		}
		name := filepath.Base(p)
		key, ext, _ := strings.Cut(name, ".")
		if ext != "json" {
			if _, err := os.Stat(filepath.Join(dir, key+".json")); err != nil {
				// Already gone when its stale sidecar was visited first.
				if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
			return nil
		}
		raw, err := os.ReadFile(p)
		var meta sidecar
		if err == nil {
			err = json.Unmarshal(raw, &meta)
		}
		data := filepath.Join(dir, key+"."+meta.Container.Ext())
		st, serr := os.Stat(data)
		if err != nil || meta.Pipeline != c.o.Pipeline || serr != nil || st.Size() != meta.Size || len(key) != 64 {
			_ = os.Remove(data)
			return os.Remove(p)
		}
		keep = append(keep, found{key: Key(key), meta: meta, data: data, mod: st.ModTime()})
		return nil
	})
	if err != nil {
		return fmt.Errorf("cache: recover: %w", err)
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].mod.After(keep[j].mod) })
	for _, f := range keep {
		e := &entry{key: f.key, st: State{Phase: Recovered}, path: f.data, container: f.meta.Container, size: f.meta.Size}
		e.elem = c.lru.PushBack(e)
		c.entries[f.key] = e
		c.total += f.meta.Size
	}
	c.evictLocked()
	return nil
}

func (c *Cache) dataPath(k Key, kind sanitize.Container) string {
	return filepath.Join(c.o.Dir, string(k[:2]), string(k)+"."+kind.Ext())
}

func (c *Cache) metaPath(k Key) string {
	return filepath.Join(c.o.Dir, string(k[:2]), string(k)+".json")
}

// getLocked returns the entry for k, expiring a failure past its TTL.
func (c *Cache) getLocked(k Key) *entry {
	e := c.entries[k]
	if e == nil {
		e = &entry{key: k, st: State{Phase: Absent}}
		c.entries[k] = e
	}
	if e.st.Phase == Failed && !c.o.Clock.Now().Before(e.st.Until) {
		c.fire(e, event{kind: evExpire})
	}
	return e
}

func (c *Cache) fire(e *entry, ev event) effect {
	next, eff, err := transition(e.st, ev)
	if err != nil {
		c.o.Logger.Error("cache: state machine", "error", err)
	}
	e.st = next
	return eff
}

func (c *Cache) dropAbsentLocked(e *entry) {
	if e.st.Phase == Absent && e.readers == 0 {
		delete(c.entries, e.key)
	}
}

// Start makes sure a job for k exists at priority p (joining or upgrading
// one in progress) and returns the state. Visitors get ErrBusy when the
// queue is full; prewarm gets ErrSkipped when the visitor queue is half
// full. A failed key returns its Failed state without new work.
func (c *Cache) Start(k Key, p Priority, job Job) (State, error) {
	st, _, err := c.start(k, p, job)
	return st, err
}

// start is Start that also returns the job's done channel while it is
// queued or running.
func (c *Cache) start(k Key, p Priority, job Job) (State, <-chan struct{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return State{}, nil, ErrClosed
	}
	e := c.getLocked(k)
	ev := event{kind: evVisitor, queueRoom: len(c.visitorQ) < c.o.Queue}
	if p == Prewarm {
		ev = event{kind: evPrewarm, queueHalf: len(c.visitorQ) >= (c.o.Queue+1)/2}
	}
	switch c.fire(e, ev) {
	case effRefuse:
		st := e.st
		c.dropAbsentLocked(e)
		switch {
		case st.Phase == Failed:
			return st, nil, nil
		case p == Prewarm:
			return st, nil, ErrSkipped
		default:
			return st, nil, ErrBusy
		}
	case effEnqueue:
		e.job = job
		e.done = make(chan struct{})
		if p == Visitor {
			c.visitorQ = append(c.visitorQ, e)
		} else {
			c.prewarmQ = append(c.prewarmQ, e)
		}
		c.dispatchLocked()
	case effPromote:
		c.prewarmQ = remove(c.prewarmQ, e)
		c.visitorQ = append(c.visitorQ, e)
		c.dispatchLocked()
	}
	if e.st.Phase == Queued || e.st.Phase == Running {
		return e.st, e.done, nil
	}
	return e.st, nil, nil
}

func remove(q []*entry, e *entry) []*entry {
	for i, x := range q {
		if x == e {
			return append(q[:i], q[i+1:]...)
		}
	}
	return q
}

// dispatchLocked starts queued jobs while slots are free; prewarm jobs only
// run when no visitor is waiting, and at most PrewarmConcurrency at once.
func (c *Cache) dispatchLocked() {
	for c.running < c.o.Concurrency && !c.closed {
		var e *entry
		switch {
		case len(c.visitorQ) > 0:
			e, c.visitorQ = c.visitorQ[0], c.visitorQ[1:]
		case len(c.prewarmQ) > 0 && c.prewarmRunning < c.o.PrewarmConcurrency:
			e, c.prewarmQ = c.prewarmQ[0], c.prewarmQ[1:]
			c.prewarmRunning++
		default:
			return
		}
		c.fire(e, event{kind: evSlot})
		c.running++
		c.wg.Add(1)
		go c.run(e)
	}
}

func (c *Cache) run(e *entry) {
	defer c.wg.Done()
	res := func() (res Result) {
		defer func() {
			if r := recover(); r != nil {
				c.o.Logger.Error("cache: job panic", "panic", r)
				res = Result{Fail: sanitize.FailTool}
			}
		}()
		return e.job(c.base, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.fire(e, event{kind: evNextStep})
		})
	}()
	var commitErr error
	var v sanitize.Verified
	if !res.Verified.IsZero() {
		v, commitErr = c.commit(e.key, res.Verified)
		if commitErr != nil {
			c.o.Logger.Error("cache: commit", "error", commitErr)
			_ = os.Remove(res.Verified.Path())
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.running--
	if e.st.Priority == Prewarm {
		c.prewarmRunning--
	}
	e.job = nil
	switch {
	case !v.IsZero():
		c.fire(e, event{kind: evVerified})
		e.v, e.path, e.container, e.size = v, v.Path(), v.Container(), v.Size()
		e.elem = c.lru.PushFront(e)
		c.total += e.size
		// The new file stays until its waiters had a chance to read it.
		c.evictLocked(e)
	case c.closed:
		c.fire(e, event{kind: evShutdown})
	default:
		reason := res.Fail
		if reason == "" || commitErr != nil {
			reason = sanitize.FailTool
		}
		ttl := c.o.FailedTTL
		if reason == sanitize.FailUpstream {
			ttl = upstreamFailedTTL
		}
		c.fire(e, event{kind: evFail, reason: reason, until: c.o.Clock.Now().Add(ttl)})
	}
	close(e.done)
	c.dropAbsentLocked(e)
	c.dispatchLocked()
}

func (c *Cache) commit(k Key, tmp sanitize.Verified) (sanitize.Verified, error) {
	v, err := tmp.MoveTo(c.dataPath(k, tmp.Container()))
	if err != nil {
		return sanitize.Verified{}, err
	}
	meta, _ := json.Marshal(sidecar{Container: v.Container(), Size: v.Size(), Pipeline: c.o.Pipeline})
	mp := c.metaPath(k)
	tmpMeta := filepath.Join(c.tmp, string(k)+".json")
	if err := os.WriteFile(tmpMeta, meta, 0o600); err != nil {
		_ = os.Remove(v.Path())
		return sanitize.Verified{}, err
	}
	if err := os.Rename(tmpMeta, mp); err != nil {
		_ = os.Remove(v.Path())
		return sanitize.Verified{}, err
	}
	return v, nil
}

// evictLocked removes least recently used files without readers (except
// keep) until the cache fits.
func (c *Cache) evictLocked(keep ...*entry) {
	for el := c.lru.Back(); el != nil && c.total > c.o.MaxBytes; {
		prev := el.Prev()
		e := el.Value.(*entry)
		if e.readers == 0 && (len(keep) == 0 || e != keep[0]) {
			c.removeLocked(e, evEvict)
		}
		el = prev
	}
}

func (c *Cache) removeLocked(e *entry, ev eventKind) {
	c.fire(e, event{kind: ev})
	c.lru.Remove(e.elem)
	e.elem = nil
	c.total -= e.size
	_ = os.Remove(e.path)
	_ = os.Remove(c.metaPath(e.key))
	e.v, e.path, e.size = sanitize.Verified{}, "", 0
	c.dropAbsentLocked(e)
}

// Handle is a committed file held open against eviction. Release it when
// done.
type Handle struct {
	V    sanitize.Verified
	ETag string
	once sync.Once
	c    *Cache
	e    *entry
}

func (h *Handle) Release() {
	h.once.Do(func() {
		h.c.mu.Lock()
		defer h.c.mu.Unlock()
		h.e.readers--
		if h.e.st.Phase == Committed {
			h.c.evictLocked()
		} else {
			h.c.dropAbsentLocked(h.e)
		}
	})
}

// Acquire returns a handle when k is committed (verifying a recovered
// entry first), or ok=false.
func (c *Cache) Acquire(ctx context.Context, k Key) (*Handle, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.getLocked(k)
	if e.st.Phase == Recovered {
		c.reverifyLocked(ctx, e)
	}
	if e.st.Phase != Committed {
		c.dropAbsentLocked(e)
		return nil, false
	}
	e.readers++
	c.lru.MoveToFront(e.elem)
	return &Handle{V: e.v, ETag: string(k[:32]), c: c, e: e}, true
}

// reverifyLocked verifies a recovered entry's file, once for all
// concurrent callers, and commits or removes it. It unlocks c.mu while
// the verifier runs, holding a reader so the file isn't evicted.
func (c *Cache) reverifyLocked(ctx context.Context, e *entry) {
	e.readers++
	path := e.path
	c.mu.Unlock()
	_, err, _ := c.verify.Do(string(e.key), func() (any, error) {
		v, err := c.o.Runner.Reverify(ctx, path)
		c.mu.Lock()
		defer c.mu.Unlock()
		if err == nil && e.st.Phase == Recovered {
			c.fire(e, event{kind: evReverified})
			e.v = v
		}
		return nil, err
	})
	c.mu.Lock()
	e.readers--
	if err != nil {
		c.o.Logger.Warn("cache: recovered entry failed verification", "error", err)
		if e.st.Phase == Recovered && e.readers == 0 {
			c.removeLocked(e, evInvalid)
		}
	}
}

// Get returns the committed file for k, running job at visitor priority if
// needed and waiting for it. Leaving (ctx done) doesn't cancel the job.
func (c *Cache) Get(ctx context.Context, k Key, job Job) (*Handle, error) {
	for {
		if h, ok := c.Acquire(ctx, k); ok {
			return h, nil
		}
		st, done, err := c.start(k, Visitor, job)
		if err != nil {
			return nil, err
		}
		if st.Phase == Failed {
			return nil, ErrFailed
		}
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

type Stats struct {
	Entries int // files on disk
}

func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{Entries: c.lru.Len()}
}

// Close stops accepting jobs, drops queued ones, gives running jobs grace
// to finish, then cancels them and empties tmp/.
func (c *Cache) Close(grace time.Duration) {
	c.mu.Lock()
	c.closed = true
	for _, e := range append(c.visitorQ, c.prewarmQ...) {
		c.fire(e, event{kind: evShutdown})
		e.job = nil
		close(e.done)
		c.dropAbsentLocked(e)
	}
	c.visitorQ, c.prewarmQ = nil, nil
	c.mu.Unlock()

	finished := make(chan struct{})
	go func() { c.wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(grace):
	}
	c.cancel()
	<-finished
	_ = os.RemoveAll(c.tmp)
}
