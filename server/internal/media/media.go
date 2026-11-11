// Package media is the only path from Immich bytes to a visitor: it checks
// what a visitor may get, runs the source chain through the sanitizer as a
// cache job, and hands out sanitize.Verified files. It also runs zip jobs
// and prewarm.
package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/clock"
	"github.com/hmnd/secure-immich-share/server/internal/immich"
	"github.com/hmnd/secure-immich-share/server/internal/linkref"
	"github.com/hmnd/secure-immich-share/server/internal/media/cache"
	"github.com/hmnd/secure-immich-share/server/internal/media/internal/fetch"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/share"
)

// PipelineVersion is part of every cache key: bump it whenever the
// sanitizer's commands, allowlist or chains change.
const PipelineVersion = "3"

var (
	// ErrNotFound: the asset or variant isn't available to this visitor, or
	// its job failed (the uniform 404).
	ErrNotFound = errors.New("media: not found")
	// ErrBusy: the sanitize queue is full.
	ErrBusy = errors.New("media: busy")
	// ErrUnavailable: Immich is unreachable.
	ErrUnavailable = errors.New("media: upstream unavailable")
)

type Options struct {
	ImmichURL      string
	MaxSourceBytes int64
	CacheDir       string
	CacheMaxBytes  int64

	Concurrency        int
	Queue              int
	Timeout            time.Duration
	FailedTTL          time.Duration
	PrewarmVideos      bool
	PrewarmImages      bool
	PrewarmConcurrency int
	PrewarmTTL         time.Duration
	ZipJobs            int
	ZipConcurrency     int
	ZipTTL             time.Duration

	Runner *sanitize.Runner // the tool paths to use
	Shares *share.Service
	Clock  clock.Clock
	Logger *slog.Logger
}

type Service struct {
	o       Options
	cache   *cache.Cache
	fetch   *fetch.Fetcher
	runner  *sanitize.Runner
	base    context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	zips    *zips
	prewarm *prewarmer
}

// New opens the cache (cleaning tmp/ and recovering committed entries).
func New(o Options) (*Service, error) {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Timeout <= 0 {
		o.Timeout = sanitize.DefaultTimeout
	}
	// o.Runner only names the tools; the service's runner works in the
	// cache's tmp/ with one idle exiftool per sanitize slot.
	runner := &sanitize.Runner{
		Exiftool: o.Runner.Exiftool, FFmpeg: o.Runner.FFmpeg,
		TmpDir: filepath.Join(o.CacheDir, "tmp"), Timeout: o.Timeout, Workers: o.Concurrency,
	}
	c, err := cache.Open(cache.Options{
		Dir: o.CacheDir, MaxBytes: o.CacheMaxBytes, Concurrency: o.Concurrency, Queue: o.Queue,
		PrewarmConcurrency: o.PrewarmConcurrency, FailedTTL: o.FailedTTL, Pipeline: PipelineVersion,
		Runner: runner, Clock: o.Clock, Logger: o.Logger,
	})
	if err != nil {
		return nil, err
	}
	f, err := fetch.New(o.ImmichURL, nil, o.MaxSourceBytes)
	if err != nil {
		return nil, err
	}
	m := &Service{o: o, cache: c, fetch: f, runner: runner}
	m.base, m.cancel = context.WithCancel(context.Background())
	m.zips = newZips(m)
	m.prewarm = newPrewarmer(o.Logger)
	return m, nil
}

// Close stops background work and drains the cache.
func (m *Service) Close(grace time.Duration) {
	m.cancel()
	m.cache.Close(grace)
	m.wg.Wait()
	m.zips.closeAll()
	m.runner.Close()
}

func (m *Service) Stats() cache.Stats { return m.cache.Stats() }

// Allowed reports whether a visitor may get variant v of item it.
func Allowed(sh *share.Share, it share.Item, v sanitize.Variant) bool {
	switch v {
	case sanitize.VariantThumb, sanitize.VariantPreview:
		return true
	case sanitize.VariantFull:
		return sh.AllowDownload && it.Kind != share.ItemVideo
	case sanitize.VariantDownload:
		return sh.AllowDownload
	case sanitize.VariantVideo:
		return it.Kind == share.ItemVideo || it.Kind == share.ItemLive
	}
	return false
}

// onDemand are the variants a visitor prepares before fetching them.
var onDemand = []sanitize.Variant{sanitize.VariantFull, sanitize.VariantVideo, sanitize.VariantDownload}

// Variants lists the on-demand variants a visitor may request for an item.
func Variants(sh *share.Share, it share.Item) []sanitize.Variant {
	var out []sanitize.Variant
	for _, v := range onDemand {
		if Allowed(sh, it, v) {
			out = append(out, v)
		}
	}
	return out
}

// ListingVersion identifies an item's thumb and preview from listing data
// alone (Immich regenerates the thumbhash with them), so their URLs can
// change with their bytes. It is "" when the item has no thumbhash.
func ListingVersion(it share.Item) string {
	if it.Thumbhash == nil {
		return ""
	}
	h := sha256.New()
	for _, part := range []string{PipelineVersion, *it.Thumbhash, strconv.FormatFloat(it.Ratio, 'g', -1, 64)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// plan is what a request resolves to: the cache key and the job.
type plan struct {
	key cache.Key
	job cache.Job
}

func (m *Service) plan(ctx context.Context, sh *share.Share, assetID string, v sanitize.Variant) (plan, error) {
	it, ok := sh.Item(assetID)
	if !ok || !Allowed(sh, it, v) {
		return plan{}, ErrNotFound
	}
	still, err := m.o.Shares.Detail(ctx, sh, assetID)
	if err != nil {
		return plan{}, mapShareErr(err)
	}
	source, live := still, it.Kind == share.ItemLive
	if v == sanitize.VariantVideo && live {
		if source, err = m.o.Shares.Detail(ctx, sh, it.LiveVideoID); err != nil {
			return plan{}, mapShareErr(err)
		}
	}
	chain := sanitize.Chain(still.Format, live, still.Edited, v)
	if len(chain) == 0 {
		return plan{}, ErrNotFound
	}
	key := cache.KeyFor(source.ID, source.Checksum, source.UpdatedAt, v, PipelineVersion)
	return plan{key: key, job: m.job(sh, assetID, it.LiveVideoID, v, chain)}, nil
}

func mapShareErr(err error) error {
	if errors.Is(err, share.ErrUnavailable) {
		return ErrUnavailable
	}
	return ErrNotFound
}

func (m *Service) job(sh *share.Share, assetID, motionID string, v sanitize.Variant, chain []sanitize.Step) cache.Job {
	auth := sh.Auth()
	shareID := sh.ID
	return func(ctx context.Context, nextStep func()) cache.Result {
		fetched := map[sanitize.Source]string{}
		defer func() {
			for _, p := range fetched {
				_ = os.Remove(p)
			}
		}()
		for i := 0; i < len(chain); {
			step := chain[i]
			outcome := m.attempt(ctx, auth, assetID, motionID, step, fetched)
			dec := sanitize.Next(i, len(chain), outcome)
			if r, ok := outcome.(sanitize.Rejected); ok {
				m.o.Logger.Warn("media: verification rejected", "asset", linkref.Hash8(assetID), "variant", v,
					"step", step.String(), "offending", r.Tags)
			} else if err, ok := outcome.(error); ok {
				m.o.Logger.Warn("media: step failed", "asset", linkref.Hash8(assetID), "variant", v,
					"step", step.String(), "error", err.Error())
			}
			if dec.Evict {
				m.o.Shares.Evict(shareID)
			}
			switch {
			case dec.Commit:
				return cache.Result{Verified: outcome.(sanitize.Verified)}
			case dec.Fail != "":
				return cache.Result{Fail: dec.Fail}
			}
			nextStep()
			i = dec.NextStep
		}
		return cache.Result{Fail: sanitize.FailRejected}
	}
}

// attempt fetches the step's source (once per job) and processes it, each
// bounded by the sanitize timeout.
func (m *Service) attempt(ctx context.Context, auth immich.Auth, assetID, motionID string, step sanitize.Step, fetched map[sanitize.Source]string) sanitize.Outcome {
	in, ok := fetched[step.Source]
	if !ok {
		var err error
		if in, err = sanitize.TempName(m.cache.TmpDir(), "src-*"); err != nil {
			return sanitize.ToolError{Kind: sanitize.ToolCrash, Err: err}
		}
		id := assetID
		if step.Source == sanitize.SourceMotion {
			id = motionID
		}
		fctx, cancel := context.WithTimeout(ctx, m.o.Timeout)
		err = m.fetch.Fetch(fctx, auth, id, step.Source, in)
		cancel()
		if err != nil {
			var se sanitize.SourceError
			if errors.As(err, &se) {
				return se
			}
			return sanitize.SourceError{Kind: sanitize.SourceUpstream, Err: err}
		}
		fetched[step.Source] = in
	}
	return m.runner.Process(ctx, in, step.Op)
}

// Get returns the verified file for a variant, waiting for its job. The
// caller must Release the handle.
func (m *Service) Get(ctx context.Context, sh *share.Share, assetID string, v sanitize.Variant) (*cache.Handle, error) {
	p, err := m.plan(ctx, sh, assetID, v)
	if err != nil {
		return nil, err
	}
	h, err := m.cache.Get(ctx, p.key, p.job)
	switch {
	case err == nil:
		return h, nil
	case errors.Is(err, cache.ErrBusy):
		return nil, ErrBusy
	case errors.Is(err, cache.ErrFailed), errors.Is(err, cache.ErrClosed):
		return nil, ErrNotFound
	}
	return nil, err
}

// Phase is the state reported by Prepare.
type Phase string

const (
	PhaseReady      Phase = "ready"
	PhaseQueued     Phase = "queued"
	PhaseProcessing Phase = "processing"
	PhaseBusy       Phase = "busy"
	PhaseFailed     Phase = "failed"
)

// How long Prepare asks clients to wait before polling again.
const (
	retryPending = time.Second
	retryBusy    = 2 * time.Second
	retryClosing = 5 * time.Second
)

// Status is the state of a variant's job.
type Status struct {
	Phase      Phase
	RetryAfter time.Duration
}

// Prepare starts or joins a variant's job at visitor priority without
// waiting.
func (m *Service) Prepare(ctx context.Context, sh *share.Share, assetID string, v sanitize.Variant) (Status, error) {
	p, err := m.plan(ctx, sh, assetID, v)
	if err != nil {
		return Status{}, err
	}
	st, err := m.cache.Start(p.key, cache.Visitor, p.job)
	switch {
	case errors.Is(err, cache.ErrBusy):
		return Status{Phase: PhaseBusy, RetryAfter: retryBusy}, nil
	case errors.Is(err, cache.ErrClosed):
		return Status{Phase: PhaseBusy, RetryAfter: retryClosing}, nil
	case err != nil:
		return Status{}, err
	}
	switch st.Phase {
	case cache.Committed, cache.Recovered:
		return Status{Phase: PhaseReady}, nil
	case cache.Queued:
		return Status{Phase: PhaseQueued, RetryAfter: retryPending}, nil
	case cache.Running:
		return Status{Phase: PhaseProcessing, RetryAfter: retryPending}, nil
	}
	return Status{Phase: PhaseFailed}, nil
}

func itemsOf(sh *share.Share) []share.Item {
	out := make([]share.Item, 0, len(sh.Order))
	for _, id := range sh.Order {
		out = append(out, sh.Items[id])
	}
	return out
}
