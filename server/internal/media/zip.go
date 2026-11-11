package media

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/media/cache"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/share"
)

// ZipPhase is the state of a zip job. Ready and failed jobs are dropped
// ZIP_TTL after they settle.
type ZipPhase string

const (
	ZipPreparing ZipPhase = "preparing"
	ZipReady     ZipPhase = "ready"
	ZipFailed    ZipPhase = "failed"
)

type zipState struct {
	phase    ZipPhase
	done     int         // preparing: entries settled
	archive  *zipArchive // ready
	expireAt time.Time   // ready, failed
}

// zipArchive is a ready zip: the pinned entry files and the layout.
type zipArchive struct {
	handles []*cache.Handle
	missing []string
	layout  *zipLayout
}

type zipEventKind string

const (
	zipEntryDone zipEventKind = "entry_done" // an entry is committed (pinned) or skipped
	zipAllDone   zipEventKind = "all_done"   // every entry settled; at least one included
	zipAllFailed zipEventKind = "all_failed" // every entry skipped, or the share is gone
)

type zipEvent struct {
	kind     zipEventKind
	archive  *zipArchive // zipAllDone
	expireAt time.Time   // zipAllDone, zipAllFailed
}

// zipTransition is the only place zip job states change.
func zipTransition(s zipState, e zipEvent) (zipState, error) {
	if s.phase == ZipPreparing {
		switch e.kind {
		case zipEntryDone:
			return zipState{phase: ZipPreparing, done: s.done + 1}, nil
		case zipAllDone:
			return zipState{phase: ZipReady, archive: e.archive, expireAt: e.expireAt}, nil
		case zipAllFailed:
			return zipState{phase: ZipFailed, expireAt: e.expireAt}, nil
		}
	}
	return s, fmt.Errorf("media: illegal zip event %s in state %s", e.kind, s.phase)
}

func (s zipState) expired(now time.Time) bool {
	return s.phase != ZipPreparing && !now.Before(s.expireAt)
}

// ZipStatus is what clients see of a zip job.
type ZipStatus struct {
	Phase    ZipPhase
	ID       string
	Done     int
	Total    int
	Included int
	Skipped  int
	Size     int64
}

// zipEntry is one item of a zip: its position in the share and its id.
type zipEntry struct {
	pos int
	id  string
}

// zipKey identifies a job for joining: the share, its listing and the
// selection ("" for the whole share).
type zipKey struct {
	shareID, listing, selection string
}

type zipJob struct {
	id      string
	key     zipKey
	entries []zipEntry
	// settled orders settled selection jobs for retention; 0 while preparing.
	settled uint64
	st      zipState
}

// maxSelectionZips is how many settled selection zips a share keeps; the
// oldest is dropped when another settles.
const maxSelectionZips = 5

type zips struct {
	m  *Service
	mu sync.Mutex
	// by id, and by key for joining
	byID  map[string]*zipJob
	byKey map[zipKey]*zipJob
	// settles counts settled jobs, ordering them for retention.
	settles uint64
}

func newZips(m *Service) *zips {
	return &zips{m: m, byID: map[string]*zipJob{}, byKey: map[zipKey]*zipJob{}}
}

// ErrZipUnavailable: the share has no downloads.
var ErrZipUnavailable = errors.New("media: zip unavailable")

// ZipAvailable reports whether a share offers a zip.
func ZipAvailable(sh *share.Share) bool { return sh.AllowDownload && len(sh.Order) > 0 }

func (z *zips) fire(j *zipJob, e zipEvent) {
	next, err := zipTransition(j.st, e)
	if err != nil {
		z.m.o.Logger.Error("media: zip state machine", "error", err)
		return
	}
	j.st = next
}

func (j *zipJob) status() ZipStatus {
	total := len(j.entries)
	st := ZipStatus{Phase: j.st.phase, ID: j.id, Done: total, Total: total}
	switch j.st.phase {
	case ZipPreparing:
		st.Done = j.st.done
	case ZipReady:
		st.Included = len(j.st.archive.handles)
		st.Skipped = len(j.st.archive.missing)
		st.Size = j.st.archive.layout.size
	}
	return st
}

// StartZip creates or joins the zip job for the share's current listing and
// the items in ids; no ids means the whole share. An id outside the share is
// ErrNotFound.
func (m *Service) StartZip(sh *share.Share, ids []string) (ZipStatus, error) {
	if !ZipAvailable(sh) {
		return ZipStatus{}, ErrZipUnavailable
	}
	entries, selection, err := zipSelection(sh, ids)
	if err != nil {
		return ZipStatus{}, err
	}
	key := zipKey{shareID: sh.ID, listing: sh.ListingHash, selection: selection}
	z := m.zips
	z.mu.Lock()
	defer z.mu.Unlock()
	z.expireLocked()
	if j := z.byKey[key]; j != nil && j.st.phase != ZipFailed {
		return j.status(), nil
	}
	preparing := 0
	for _, j := range z.byID {
		if j.st.phase == ZipPreparing {
			preparing++
		}
	}
	if preparing >= m.o.ZipJobs {
		return ZipStatus{}, ErrBusy
	}
	idb := make([]byte, 16)
	if _, err := rand.Read(idb); err != nil {
		return ZipStatus{}, err
	}
	j := &zipJob{id: hex.EncodeToString(idb), key: key, entries: entries, st: zipState{phase: ZipPreparing}}
	z.byID[j.id] = j
	z.byKey[key] = j
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		z.prepare(j, sh)
	}()
	return j.status(), nil
}

// zipSelection resolves ids to entries in share order and the selection key:
// "" for the whole share, else the hex SHA-256 of the sorted, deduplicated ids.
func zipSelection(sh *share.Share, ids []string) ([]zipEntry, string, error) {
	if len(ids) == 0 {
		entries := make([]zipEntry, len(sh.Order))
		for i, id := range sh.Order {
			entries[i] = zipEntry{pos: i, id: id}
		}
		return entries, "", nil
	}
	pos := make(map[string]int, len(sh.Order))
	for i, id := range sh.Order {
		pos[id] = i
	}
	seen := make(map[string]bool, len(ids))
	entries := make([]zipEntry, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		i, ok := pos[id]
		if !ok {
			return nil, "", ErrNotFound
		}
		entries = append(entries, zipEntry{pos: i, id: id})
	}
	slices.SortFunc(entries, func(a, b zipEntry) int { return a.pos - b.pos })
	sorted := slices.Sorted(maps.Keys(seen))
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return entries, hex.EncodeToString(sum[:]), nil
}

// ZipStatusOf polls a zip job of this share.
func (m *Service) ZipStatusOf(sh *share.Share, id string) (ZipStatus, error) {
	z := m.zips
	z.mu.Lock()
	defer z.mu.Unlock()
	z.expireLocked()
	j := z.byID[id]
	if j == nil || j.key.shareID != sh.ID {
		return ZipStatus{}, ErrNotFound
	}
	return j.status(), nil
}

// OpenZip opens a ready zip for download. Close the reader when done.
func (m *Service) OpenZip(sh *share.Share, id string) (io.ReadSeekCloser, error) {
	z := m.zips
	z.mu.Lock()
	defer z.mu.Unlock()
	z.expireLocked()
	j := z.byID[id]
	if j == nil || j.key.shareID != sh.ID || j.st.phase != ZipReady {
		return nil, ErrNotFound
	}
	return j.st.archive.layout.open(), nil
}

func entryName(i, total int) string {
	width := max(3, len(fmt.Sprint(total)))
	return fmt.Sprintf("%0*d", width, i+1)
}

// prepare runs every entry's download job (at most ZIP_CONCURRENCY at a
// time), pins the results and lays out the archive. Entries are named after
// their position in the share.
func (z *zips) prepare(j *zipJob, sh *share.Share) {
	m := z.m
	type res struct {
		h   *cache.Handle
		crc uint32
	}
	results := make([]res, len(j.entries))
	sem := make(chan struct{}, max(1, m.o.ZipConcurrency))
	var wg sync.WaitGroup
	for i, e := range j.entries {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			h, _ := z.entry(sh, e.id)
			var crc uint32
			if h != nil {
				var err error
				if crc, err = fileCRC(h.V.Path()); err != nil {
					h.Release()
					h = nil
				}
			}
			results[i] = res{h: h, crc: crc}
			z.mu.Lock()
			z.fire(j, zipEvent{kind: zipEntryDone})
			z.mu.Unlock()
		}()
	}
	wg.Wait()

	var sources []zipSource
	var handles []*cache.Handle
	var missing []string
	for i, r := range results {
		name := entryName(j.entries[i].pos, len(sh.Order))
		if r.h == nil {
			missing = append(missing, name)
			continue
		}
		handles = append(handles, r.h)
		sources = append(sources, zipSource{name: name + "." + r.h.V.Ext(), path: r.h.V.Path(), size: r.h.V.Size(), crc: r.crc})
	}
	if len(missing) > 0 {
		body := []byte("These items couldn't be included:\n" + strings.Join(missing, "\n") + "\n")
		sources = append(sources, zipSource{name: "MISSING.txt", data: body, size: int64(len(body)), crc: crc32.ChecksumIEEE(body)})
	}

	z.mu.Lock()
	defer z.mu.Unlock()
	defer z.settledLocked(j)
	expireAt := m.o.Clock.Now().Add(m.o.ZipTTL)
	if len(handles) == 0 || m.base.Err() != nil {
		for _, h := range handles {
			h.Release()
		}
		z.fire(j, zipEvent{kind: zipAllFailed, expireAt: expireAt})
		return
	}
	archive := &zipArchive{handles: handles, missing: missing, layout: layoutZip(sources)}
	z.fire(j, zipEvent{kind: zipAllDone, archive: archive, expireAt: expireAt})
}

// settledLocked records that j settled and, for a selection job, keeps at
// most maxSelectionZips settled selection jobs of its share by dropping the
// oldest.
func (z *zips) settledLocked(j *zipJob) {
	if j.st.phase == ZipPreparing || z.byID[j.id] != j {
		return
	}
	z.settles++
	j.settled = z.settles
	if j.key.selection == "" {
		return
	}
	var settled []*zipJob
	for _, o := range z.byID {
		if o.key.shareID == j.key.shareID && o.key.selection != "" && o.settled != 0 {
			settled = append(settled, o)
		}
	}
	if len(settled) <= maxSelectionZips {
		return
	}
	slices.SortFunc(settled, func(a, b *zipJob) int { return cmp.Compare(a.settled, b.settled) })
	for _, o := range settled[:len(settled)-maxSelectionZips] {
		z.dropLocked(o)
	}
}

// entry gets one item's download variant, waiting while the queue is full.
func (z *zips) entry(sh *share.Share, id string) (*cache.Handle, error) {
	m := z.m
	for {
		h, err := m.Get(m.base, sh, id, sanitize.VariantDownload)
		if !errors.Is(err, ErrBusy) {
			return h, err
		}
		select {
		case <-m.base.Done():
			return nil, m.base.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// expireLocked drops jobs past ZIP_TTL and releases their pins.
func (z *zips) expireLocked() {
	now := z.m.o.Clock.Now()
	for _, j := range z.byID {
		if j.st.expired(now) {
			z.dropLocked(j)
		}
	}
}

// dropLocked releases a settled job's pins and forgets it.
func (z *zips) dropLocked(j *zipJob) {
	j.release()
	delete(z.byID, j.id)
	if z.byKey[j.key] == j {
		delete(z.byKey, j.key)
	}
}

func (z *zips) closeAll() {
	z.mu.Lock()
	defer z.mu.Unlock()
	for _, j := range z.byID {
		j.release()
	}
	z.byID = map[string]*zipJob{}
	z.byKey = map[zipKey]*zipJob{}
}

// release unpins a ready job's files; the job must be dropped with it.
func (j *zipJob) release() {
	if j.st.phase == ZipReady {
		for _, h := range j.st.archive.handles {
			h.Release()
		}
	}
}
