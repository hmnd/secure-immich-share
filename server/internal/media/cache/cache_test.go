package cache_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/clock"
	"github.com/hmnd/secure-immich-share/server/internal/media/cache"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize/sanitizetest"
)

type rig struct {
	c      *cache.Cache
	clock  *clock.Fake
	runner *sanitize.Runner
	dir    string
}

func open(t *testing.T, dir string, mod func(*cache.Options)) *rig {
	t.Helper()
	tools := sanitizetest.LookTools(t)
	if dir == "" {
		dir = t.TempDir()
	}
	r := &rig{clock: clock.NewFake(time.Unix(1_700_000_000, 0)), dir: dir}
	r.runner = &sanitize.Runner{Exiftool: tools.Exiftool, FFmpeg: tools.FFmpeg, TmpDir: filepath.Join(dir, "tmp")}
	o := cache.Options{
		Dir: dir, MaxBytes: 1 << 30, Concurrency: 2, Queue: 4, PrewarmConcurrency: 1,
		FailedTTL: 10 * time.Minute, Pipeline: "test-1", Runner: r.runner, Clock: r.clock,
	}
	if mod != nil {
		mod(&o)
	}
	c, err := cache.Open(o)
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	t.Cleanup(func() { c.Close(time.Second) })
	return r
}

func key(n int) cache.Key {
	return cache.KeyFor(fmt.Sprint("asset-", n), "sum", time.Unix(0, 0), sanitize.VariantThumb, "test-1")
}

// stripJob produces a verified JPEG; it counts runs and can be held.
func (r *rig) stripJob(t *testing.T, runs *atomic.Int32, hold <-chan struct{}) cache.Job {
	src := sanitizetest.Clean(t, "clean.jpg")
	return func(ctx context.Context, _ func()) cache.Result {
		runs.Add(1)
		if hold != nil {
			select {
			case <-hold:
			case <-ctx.Done():
				return cache.Result{Fail: sanitize.FailTool}
			}
		}
		switch o := r.runner.Process(ctx, src, sanitize.OpStrip, sanitize.Taken{}).(type) {
		case sanitize.Verified:
			return cache.Result{Verified: o}
		default:
			t.Errorf("strip: %v", o)
			return cache.Result{Fail: sanitize.FailTool}
		}
	}
}

// waitCtx reports when Get starts waiting for a job: Get only calls Done
// right before it blocks.
type waitCtx struct {
	context.Context
	waiting chan<- struct{}
	once    sync.Once
}

func (c *waitCtx) Done() <-chan struct{} {
	c.once.Do(func() { c.waiting <- struct{}{} })
	return c.Context.Done()
}

func TestSingleFlight(t *testing.T) {
	t.Parallel()
	r := open(t, "", nil)
	var runs atomic.Int32
	hold := make(chan struct{})
	job := r.stripJob(t, &runs, hold)
	waiting := make(chan struct{})
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			h, err := r.c.Get(&waitCtx{Context: context.Background(), waiting: waiting}, key(1), job)
			if err != nil {
				t.Error(err)
				return
			}
			h.Release()
		})
	}
	for range 5 {
		<-waiting
	}
	close(hold)
	wg.Wait()
	if runs.Load() != 1 {
		t.Fatalf("job ran %d times", runs.Load())
	}
	if st := r.c.Peek(key(1)); st.Phase != cache.Committed {
		t.Fatalf("state %s", st.Phase)
	}
}

func TestLeavingDoesNotCancel(t *testing.T) {
	t.Parallel()
	r := open(t, "", nil)
	var runs atomic.Int32
	hold := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan struct{})
	go func() { <-waiting; cancel() }()
	wctx := &waitCtx{Context: ctx, waiting: waiting}
	if _, err := r.c.Get(wctx, key(1), r.stripJob(t, &runs, hold)); !errors.Is(err, context.Canceled) {
		t.Fatalf("get: %v", err)
	}
	close(hold)
	r.c.Wait(context.Background(), key(1))
	if st := r.c.Peek(key(1)); st.Phase != cache.Committed {
		t.Fatalf("job didn't finish after the visitor left: %s", st.Phase)
	}
}

func TestEvictionSkipsReaders(t *testing.T) {
	t.Parallel()
	var size int64
	r := open(t, "", func(o *cache.Options) { o.MaxBytes = 1 }) // every commit overflows
	var runs atomic.Int32
	job := r.stripJob(t, &runs, nil)
	h1, err := r.c.Get(context.Background(), key(1), job)
	if err != nil {
		t.Fatal(err)
	}
	size = h1.V.Size()
	h2, err := r.c.Get(context.Background(), key(2), job)
	if err != nil {
		t.Fatal(err)
	}
	// Both are held: neither may be evicted, even over the limit.
	if st := r.c.Counters(); st.Entries != 2 || st.Bytes != 2*size {
		t.Fatalf("stats with readers: %+v", st)
	}
	if _, err := os.Stat(h1.V.Path()); err != nil {
		t.Fatalf("held file removed: %v", err)
	}
	h1.Release()
	if r.c.Peek(key(1)).Phase != cache.Absent {
		t.Fatal("released file not evicted")
	}
	if _, err := os.Stat(h2.V.Path()); err != nil {
		t.Fatalf("held file removed: %v", err)
	}
	h2.Release()
	if st := r.c.Counters(); st.Entries != 0 || st.Bytes != 0 {
		t.Fatalf("stats after release: %+v", st)
	}
}

func TestRecovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := open(t, dir, nil)
	var runs atomic.Int32
	job := r.stripJob(t, &runs, nil)
	var paths []string
	for i := 1; i <= 3; i++ {
		h, err := r.c.Get(context.Background(), key(i), job)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, h.V.Path())
		h.Release()
	}
	r.c.Close(time.Second)
	// Junk left by a crash.
	if err := os.WriteFile(filepath.Join(dir, "tmp-leftover"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "tmp"), 0o750)
	_ = os.WriteFile(filepath.Join(dir, "tmp", "partial.jpg"), []byte("x"), 0o600)
	// Entry 2: tampered after commit, sidecar size updated to match, so
	// only re-verification can catch it.
	data, _ := os.ReadFile(paths[1])
	data = append(data, []byte("GPS 49.28,-123.12")...)
	_ = os.WriteFile(paths[1], data, 0o600)
	meta2 := filepath.Join(filepath.Dir(paths[1]), string(key(2))+".json")
	_ = os.WriteFile(meta2, []byte(fmt.Sprintf(`{"kind":"jpeg","size":%d,"pipeline":"test-1"}`, len(data))), 0o600)

	r2 := open(t, dir, func(o *cache.Options) { o.Pipeline = "test-1" })
	if _, err := os.Stat(filepath.Join(dir, "tmp", "partial.jpg")); !os.IsNotExist(err) {
		t.Error("tmp not emptied")
	}
	if _, err := os.Stat(filepath.Join(dir, "tmp-leftover")); !os.IsNotExist(err) {
		t.Error("stray file kept")
	}
	h, ok := r2.c.Acquire(context.Background(), key(1))
	if !ok {
		t.Fatal("recovered entry 1 missing")
	}
	h.Release()
	if _, ok := r2.c.Acquire(context.Background(), key(2)); ok {
		t.Fatal("tampered entry served")
	}
	if _, err := os.Stat(paths[1]); !os.IsNotExist(err) {
		t.Error("tampered file kept")
	}

	r2.c.Close(time.Second)
	r3 := open(t, dir, func(o *cache.Options) { o.Pipeline = "test-2" })
	if st := r3.c.Counters(); st.Entries != 0 {
		t.Fatalf("entries from another pipeline recovered: %+v", st)
	}
}

// A stale sidecar is visited before data whose extension sorts after
// "json" (mp4, png, webp); removing both then must not fail recovery.
func TestRecoveryStaleBeforeData(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	k := string(key(1))
	sub := filepath.Join(dir, k[:2])
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(sub, k+".mp4")
	meta := filepath.Join(sub, k+".json")
	_ = os.WriteFile(data, []byte("x"), 0o600)
	_ = os.WriteFile(meta, []byte(`{"kind":"mp4","size":1,"pipeline":"test-0"}`), 0o600)

	r := open(t, dir, nil)
	if st := r.c.Counters(); st.Entries != 0 {
		t.Fatalf("stale entry recovered: %+v", st)
	}
	for _, p := range []string{data, meta} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s kept", filepath.Base(p))
		}
	}
}

func TestPrewarmYieldsToVisitors(t *testing.T) {
	t.Parallel()
	r := open(t, "", func(o *cache.Options) { o.Concurrency = 1; o.Queue = 4 })
	var order []int
	var mu sync.Mutex
	hold := make(chan struct{})
	mk := func(n int, wait bool) cache.Job {
		var runs atomic.Int32
		inner := r.stripJob(t, &runs, nil)
		return func(ctx context.Context, step func()) cache.Result {
			if wait {
				<-hold
			}
			mu.Lock()
			order = append(order, n)
			mu.Unlock()
			return inner(ctx, step)
		}
	}
	// Slot taken by visitor job 1; then prewarm 2 and visitor 3 queue.
	if _, err := r.c.Start(key(1), cache.Visitor, mk(1, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Start(key(2), cache.Prewarm, mk(2, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Start(key(3), cache.Visitor, mk(3, false)); err != nil {
		t.Fatal(err)
	}
	// Visitor queue now 1 of 4; a second visitor makes it half full and
	// prewarm requests are skipped.
	if _, err := r.c.Start(key(4), cache.Visitor, mk(4, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Start(key(5), cache.Prewarm, mk(5, false)); !errors.Is(err, cache.ErrSkipped) {
		t.Fatalf("prewarm under load: %v", err)
	}
	close(hold)
	for _, n := range []int{1, 2, 3, 4} {
		r.c.Wait(context.Background(), key(n))
	}
	r.c.Wait(context.Background(), key(2))
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(order) != "[1 3 4 2]" {
		t.Fatalf("run order %v: visitors must run before prewarm", order)
	}
}

func TestPrewarmUpgrade(t *testing.T) {
	t.Parallel()
	r := open(t, "", func(o *cache.Options) { o.Concurrency = 1 })
	hold := make(chan struct{})
	var runs atomic.Int32
	if _, err := r.c.Start(key(1), cache.Visitor, r.stripJob(t, &runs, hold)); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.c.Start(key(2), cache.Prewarm, r.stripJob(t, &runs, nil)); st.Priority != cache.Prewarm {
		t.Fatalf("prewarm state %+v", st)
	}
	if st, _ := r.c.Start(key(2), cache.Visitor, nil); st.Priority != cache.Visitor || st.Phase != cache.Queued {
		t.Fatalf("upgraded state %+v", st)
	}
	if st := r.c.Counters(); st.VisitorQueue != 1 || st.PrewarmQueue != 0 {
		t.Fatalf("queues after upgrade: %+v", st)
	}
	close(hold)
	h, err := r.c.Get(context.Background(), key(2), nil)
	if err != nil {
		t.Fatal(err)
	}
	h.Release()
}

func TestCloseDrains(t *testing.T) {
	t.Parallel()
	r := open(t, "", func(o *cache.Options) { o.Concurrency = 1 })
	hold := make(chan struct{})
	var runs atomic.Int32
	if _, err := r.c.Start(key(1), cache.Visitor, r.stripJob(t, &runs, hold)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Start(key(2), cache.Visitor, r.stripJob(t, &runs, nil)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r.c.Close(100 * time.Millisecond)
	if time.Since(start) > 5*time.Second {
		t.Fatal("close didn't cancel the running job")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "tmp")); !os.IsNotExist(err) {
		t.Error("tmp left after close")
	}
	if st := r.c.Peek(key(1)); st.Phase != cache.Absent {
		t.Errorf("running job after shutdown: %s", st.Phase)
	}
	if runs.Load() != 1 {
		t.Errorf("queued job ran during shutdown")
	}
	if _, err := r.c.Start(key(3), cache.Visitor, nil); !errors.Is(err, cache.ErrClosed) {
		t.Errorf("start after close: %v", err)
	}
}
