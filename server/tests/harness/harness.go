// Package harness runs the real app on a random port against the fake
// Immich, behind a proxy that validates every /api/ response against the
// committed api/openapi.json.
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/app"
	"github.com/hmnd/secure-immich-share/server/internal/clock"
	"github.com/hmnd/secure-immich-share/server/internal/config"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize/sanitizetest"
	"github.com/hmnd/secure-immich-share/server/internal/share"
	fake "github.com/hmnd/secure-immich-share/server/tests/fakes/immich"
)

// LinkKey is the LINK_KEYS secret used by default (token links on).
var LinkKey = config.LinkKey{ID: 1, Secret: bytes.Repeat([]byte{0x42}, 32), Active: true}

// AssetScript is the islands' script in WebBuild.
const AssetScript = "/assets/islands-T3st.js"

// WebBuild stands in for the embedded web build.
var WebBuild = fstest.MapFS{
	".vite/manifest.json":     {Data: []byte(`{"src/islands.ts": {"file": "assets/islands-T3st.js", "css": ["assets/islands-T3st.css"]}}`)},
	"assets/islands-T3st.js":  {Data: []byte("export {};\n")},
	"assets/islands-T3st.css": {Data: []byte("body {}\n")},
}

// Options customize an environment.
type Options struct {
	// Config edits the configuration before the app starts.
	Config func(*config.Config)
	// ImmichDown starts the app with Immich unreachable.
	ImmichDown bool
}

// Env is one running app with its fakes.
type Env struct {
	BaseURL string
	Immich  *fake.Immich
	App     *app.App
	Config  config.Config
	Clock   *clock.Fake
	Runner  *sanitize.Runner
	Logs    *Logs

	cancel context.CancelFunc
	done   chan error
	conf   *conformance
	once   sync.Once
}

// Logs captures the app's log output.
type Logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *Logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// String returns everything logged so far.
func (l *Logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// Start runs the app for the test.
func Start(t *testing.T, opts Options) *Env {
	t.Helper()
	tools := sanitizetest.LookTools(t)
	c := clock.NewFake(time.Now())
	f := fake.New()
	f.SetClock(c)
	t.Cleanup(f.Close)
	f.Seed(t)
	f.SetDown(opts.ImmichDown)

	appLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + proxyLn.Addr().String()
	cfg := config.Config{
		ImmichURL: f.URL(), PublicURL: base,
		ImmichKeyLinks: true, ImmichSlugLinks: true, LinkKeys: []config.LinkKey{LinkKey},
		CookieKey: bytes.Repeat([]byte{0x24}, 32), ListenAddr: appLn.Addr().String(),
		CacheDir: t.TempDir(), CacheMaxBytes: 1 << 30, MaxSourceBytes: 1 << 30,
		SanitizeConcurrency: 4, SanitizeQueue: 16, SanitizeTimeout: time.Minute,
		ShareTTL: time.Minute, ShowDates: true, OGPreviews: true,
		PrewarmVideos: false, PrewarmConcurrency: 1, PrewarmTTL: 10 * time.Minute,
		FailedTTL: 10 * time.Minute, ZipJobs: 2, ZipConcurrency: 2, ZipTTL: 15 * time.Minute,
		ShutdownGrace: 5 * time.Second, LogLevel: "debug",
	}
	if opts.Config != nil {
		opts.Config(&cfg)
	}
	logs := &Logs{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	runner := &sanitize.Runner{Exiftool: tools.Exiftool, FFmpeg: tools.FFmpeg}
	ctx, cancel := context.WithCancel(context.Background())
	a, err := app.New(ctx, cfg, app.Options{Clock: c, Listener: appLn, Logger: logger, Runner: runner,
		Assets: WebBuild, ProbeInterval: time.Hour})
	if err != nil {
		cancel()
		t.Fatalf("app: %v", err)
	}
	router, err := contractRouter()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	conf := &conformance{router: router}
	proxy := startContractProxy(proxyLn, appLn.Addr().String(), conf)
	e := &Env{
		BaseURL: base, Immich: f, App: a, Config: cfg, Clock: c, Logs: logs, cancel: cancel,
		done: make(chan error, 1), conf: conf,
		Runner: &sanitize.Runner{Exiftool: tools.Exiftool, FFmpeg: tools.FFmpeg, TmpDir: t.TempDir(), KeepDates: cfg.ShowDates},
	}
	go func() { e.done <- a.Run(ctx) }()
	t.Cleanup(func() {
		e.Stop(t)
		_ = proxy.Close()
		for _, v := range conf.Violations() {
			t.Errorf("contract: %s", v)
		}
	})
	Eventually(t, 5*time.Second, func() bool {
		st := a.State()
		return st == app.StateReady || st == app.StateDegraded
	}, "app never started")
	return e
}

// Stop cancels the app's context and waits for it to drain and exit.
func (e *Env) Stop(t testing.TB) {
	e.once.Do(func() {
		e.cancel()
		select {
		case err := <-e.done:
			if err != nil {
				t.Errorf("app run: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("app didn't stop")
		}
	})
}

// Eventually polls cond until it holds.
func Eventually(t testing.TB, timeout time.Duration, cond func() bool, msg string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s: %s", timeout, fmt.Sprintf(msg, args...))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Visitor is a browser: a cookie jar and the app's origin.
type Visitor struct {
	e      *Env
	Client *http.Client
	// IP is sent as X-Forwarded-For when the app trusts the proxy.
	IP string
}

// Visitor returns a new browser.
func (e *Env) Visitor() *Visitor {
	jar, _ := cookiejar.New(nil)
	return &Visitor{e: e, Client: &http.Client{Jar: jar, Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Response is a read response.
type Response struct {
	*http.Response
	Body []byte
}

// JSON decodes the body.
func (r Response) JSON(t testing.TB, dst any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, dst); err != nil {
		t.Fatalf("decode %s: %v: %s", r.Request.URL.Path, err, r.Body)
	}
}

// Code is the error envelope's code.
func (r Response) Code() string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &env)
	return env.Error.Code
}

// Do sends a request like the web client; body (if not nil) is
// JSON-encoded. headers are name, value pairs overriding the defaults; an
// empty value removes the header and "Host" sets the request's host.
func (v *Visitor) Do(t testing.TB, method, path string, body any, headers ...string) Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, v.e.BaseURL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Origin", v.e.BaseURL)
	}
	return v.send(t, req, headers)
}

// Get is Do(GET).
func (v *Visitor) Get(t testing.TB, path string, headers ...string) Response {
	t.Helper()
	return v.Do(t, http.MethodGet, path, nil, headers...)
}

// Post is Do(POST).
func (v *Visitor) Post(t testing.TB, path string, body any, headers ...string) Response {
	t.Helper()
	return v.Do(t, http.MethodPost, path, body, headers...)
}

// Form submits an HTML form like a browser on a no-referrer page:
// Sec-Fetch-Site same-origin and Origin null, unless headers override them.
func (v *Visitor) Form(t testing.TB, path string, values url.Values, headers ...string) Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, v.e.BaseURL+path, strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", "null")
	return v.send(t, req, headers)
}

func (v *Visitor) send(t testing.TB, req *http.Request, headers []string) Response {
	t.Helper()
	for i := 0; i+1 < len(headers); i += 2 {
		switch {
		case strings.EqualFold(headers[i], "Host"):
			req.Host = headers[i+1]
		case headers[i+1] == "":
			req.Header.Del(headers[i])
		default:
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	if v.IP != "" {
		req.Header.Set("X-Forwarded-For", v.IP)
	}
	resp, err := v.Client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return Response{Response: resp, Body: data}
}

// SaveBody writes body to a temp file and returns its path.
func SaveBody(t testing.TB, body []byte, ext string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "body-*."+strings.TrimPrefix(ext, "."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(body); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return f.Name()
}

// UnlockCookie is the unlock cookie r sets, as a Cookie header for Get and
// Do. Unlock cookies are Secure, so the jar never sends them over http.
func UnlockCookie(t testing.TB, r Response) []string {
	t.Helper()
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, share.CookiePrefix) && c.MaxAge > 0 {
			return []string{"Cookie", c.Name + "=" + c.Value}
		}
	}
	t.Fatalf("no unlock cookie: %d %s", r.StatusCode, r.Body)
	return nil
}
