// Package app wires the components together and owns the server lifecycle:
// startup checks, the HTTP listener, the Immich probe and draining.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/clock"
	"github.com/hmnd/secure-immich-share/server/internal/config"
	"github.com/hmnd/secure-immich-share/server/internal/httpapi"
	"github.com/hmnd/secure-immich-share/server/internal/immich"
	"github.com/hmnd/secure-immich-share/server/internal/linkref"
	"github.com/hmnd/secure-immich-share/server/internal/media"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/ratelimit"
	"github.com/hmnd/secure-immich-share/server/internal/share"
	"github.com/hmnd/secure-immich-share/server/internal/webassets"
)

// Options customizes the app (used by cmd and the test harness).
type Options struct {
	Clock clock.Clock
	// Listener overrides LISTEN_ADDR (tests listen on a random port).
	Listener net.Listener
	Logger   *slog.Logger
	// HTTPClient makes Immich JSON calls.
	HTTPClient *http.Client
	// Runner overrides the exiftool/ffmpeg locations.
	Runner *sanitize.Runner
	// Assets overrides the embedded web build.
	Assets fs.FS
	// ProbeInterval is how often Immich is probed (default 30s).
	ProbeInterval time.Duration
}

type App struct {
	cfg    config.Config
	logger *slog.Logger
	immich *immich.Client
	media  *media.Service

	life     lifecycle
	listener net.Listener
	handler  *httpapi.Server
	probe    time.Duration
}

// New validates the environment (external tools, cache directory)
// and assembles every component.
func New(ctx context.Context, cfg config.Config, opts Options) (*App, error) {
	if opts.Clock == nil {
		opts.Clock = clock.Real{}
	}
	if opts.Logger == nil {
		opts.Logger = NewLogger(cfg.LogLevel)
	}
	if opts.Assets == nil {
		opts.Assets = webassets.FS()
	}
	if opts.ProbeInterval <= 0 {
		opts.ProbeInterval = 30 * time.Second
	}
	runner := opts.Runner
	if runner == nil {
		runner = &sanitize.Runner{}
	}
	exifVer, ffVer, err := runner.CheckTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	links, err := linkref.NewParser(cfg)
	if err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	sealer, err := share.NewSealer(cfg.CookieKey)
	if err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	client := immich.New(cfg.ImmichURL, opts.HTTPClient)
	shares := share.New(share.Options{
		Client: client, Clock: opts.Clock, Logger: opts.Logger, TTL: cfg.ShareTTL,
		Throttle: ratelimit.NewThrottle(opts.Clock),
	})
	m, err := media.New(media.Options{
		ImmichURL: cfg.ImmichURL, MaxSourceBytes: cfg.MaxSourceBytes, CacheDir: cfg.CacheDir,
		CacheMaxBytes: cfg.CacheMaxBytes, Concurrency: cfg.SanitizeConcurrency, Queue: cfg.SanitizeQueue,
		Timeout: cfg.SanitizeTimeout, FailedTTL: cfg.FailedTTL, PrewarmVideos: cfg.PrewarmVideos,
		PrewarmImages: cfg.PrewarmImages, PrewarmConcurrency: cfg.PrewarmConcurrency, PrewarmTTL: cfg.PrewarmTTL,
		ZipJobs: cfg.ZipJobs, ZipConcurrency: cfg.ZipConcurrency, ZipTTL: cfg.ZipTTL,
		Runner: runner, Shares: shares, Clock: opts.Clock, Logger: opts.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("app: cache: %w", err)
	}
	opts.Logger.Info("starting", "exiftool", exifVer, "ffmpeg", ffVer, "cache_entries", m.Stats().Entries)
	a := &App{
		cfg: cfg, logger: opts.Logger, immich: client, media: m,
		life: lifecycle{state: StateStarting}, listener: opts.Listener, probe: opts.ProbeInterval,
	}
	a.handler = httpapi.NewServer(httpapi.Deps{
		Cfg: cfg, Clock: opts.Clock, Logger: opts.Logger, Assets: opts.Assets,
		Health: a.health,
		Links:  links, Shares: shares, Media: m, Sealer: sealer,
	})
	return a, nil
}

func (a *App) State() State { return a.life.get() }

func (a *App) health() httpapi.Health {
	switch a.life.get() {
	case StateReady:
		return httpapi.HealthReady
	case StateDegraded:
		return httpapi.HealthDegraded
	}
	return httpapi.HealthUnavailable
}

// Probe checks Immich once and moves between ready and degraded.
func (a *App) Probe(ctx context.Context) State {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ev := EventProbeOK
	if err := a.immich.Ping(ctx); err != nil {
		ev = EventProbeFailed
	}
	prev := a.life.get()
	next := a.life.fire(ev)
	if prev != next {
		a.logger.Warn("immich reachability changed", "state", next)
	}
	return next
}

// Run serves until ctx is cancelled, then drains.
func (a *App) Run(ctx context.Context) error {
	if a.cfg.ImmichKeyLinks || a.cfg.ImmichSlugLinks {
		a.logger.Warn("Immich key and slug links are on: visitors hold Immich share keys, so Immich must be unreachable for them")
	}
	if a.cfg.ViteDev {
		a.logger.Warn("VITE_DEV is on: pages load their scripts from the Vite dev server and break without it; never set it in production")
	}
	ln := a.listener
	if ln == nil {
		var err error
		if ln, err = net.Listen("tcp", a.cfg.ListenAddr); err != nil {
			a.media.Close(0)
			return fmt.Errorf("app: listen: %w", err)
		}
	}
	srv := &http.Server{
		Handler:           a.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	version, verr := a.immich.Version(vctx)
	cancel()
	if verr != nil {
		a.life.fire(EventStartedDown)
		a.logger.Warn("immich unreachable at startup", "error", verr.Error())
	} else {
		a.life.fire(EventStarted)
	}
	a.logger.Info("listening", "addr", ln.Addr().String(), "state", a.life.get(), "immich", version)

	ticker := time.NewTicker(a.probe)
	defer ticker.Stop()
	for running := true; running; {
		select {
		case err := <-serveErr:
			a.media.Close(0)
			return fmt.Errorf("app: serve: %w", err)
		case <-ticker.C:
			a.Probe(ctx)
		case <-ctx.Done():
			running = false
		}
	}

	a.life.fire(EventSignal)
	a.logger.Info("draining", "grace", a.cfg.ShutdownGrace.String())
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), a.cfg.ShutdownGrace)
	defer cancelShutdown()
	// In-flight requests may be waiting for media jobs, so the jobs keep
	// running until the requests are done; media gets what is left of the
	// grace period.
	err := srv.Shutdown(shutdownCtx)
	if err != nil {
		_ = srv.Close()
	}
	deadline, _ := shutdownCtx.Deadline()
	a.media.Close(time.Until(deadline))
	a.life.fire(EventDrained)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("app: shutdown: %w", err)
	}
	return nil
}

// NewLogger is the JSON logger at the configured level.
func NewLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}
