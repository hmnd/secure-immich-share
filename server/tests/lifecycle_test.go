package tests

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/app"
	fake "github.com/hmnd/secure-immich-share/server/tests/fakes/immich"
	"github.com/hmnd/secure-immich-share/server/tests/harness"
)

func TestStartDegraded(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{ImmichDown: true})
	v := e.Visitor()
	if st := e.App.State(); st != app.StateDegraded {
		t.Fatalf("state %s", st)
	}
	if h := v.Get(t, "/healthz"); h.StatusCode != 200 || !strings.Contains(string(h.Body), `"degraded"`) {
		t.Fatalf("healthz: %d %s", h.StatusCode, h.Body)
	}
	e.Immich.SetDown(false)
	e.App.Probe(context.Background())
	if h := v.Get(t, "/healthz"); h.StatusCode != 200 || !strings.Contains(string(h.Body), `"ready"`) {
		t.Fatalf("healthz after recovery: %d %s", h.StatusCode, h.Body)
	}
}

func TestDrain(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	e.Immich.DelayNext("/shared-links/me", 500*time.Millisecond)
	inflight := make(chan harness.Response, 1)
	go func() { inflight <- v.Get(t, "/s/"+fake.PublicSlug) }()
	harness.Eventually(t, 5*time.Second, func() bool {
		return e.Immich.Count("GET", "/api/shared-links/me") > 0
	}, "share never requested")
	// A media request whose job is still running when the drain starts.
	release := e.Immich.Hold()
	defer release()
	media := make(chan harness.Response, 1)
	go func() { media <- e.Visitor().Get(t, "/media/key/"+fake.PublicKey+"/"+fake.VideoID+"/video") }()
	harness.Eventually(t, 5*time.Second, func() bool {
		return e.Immich.Count("GET", "/api/assets/"+fake.VideoID+"/video/playback") > 0
	}, "video never requested")

	stopped := make(chan struct{})
	go func() { e.Stop(t); close(stopped) }()
	harness.Eventually(t, 5*time.Second, func() bool { return e.App.State() == app.StateDraining }, "never draining")
	if r := <-inflight; r.StatusCode != 200 {
		t.Fatalf("in-flight request during drain: %d", r.StatusCode)
	}
	release()
	if r := <-media; r.StatusCode != 200 || r.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("in-flight media request during drain: %d", r.StatusCode)
	}
	<-stopped
	if st := e.App.State(); st != app.StateStopped {
		t.Fatalf("state %s", st)
	}
	if st := e.App.Probe(context.Background()); st != app.StateStopped {
		t.Fatalf("probe after stop moved to %s", st)
	}
	if _, err := os.Stat(filepath.Join(e.Config.CacheDir, "tmp")); !os.IsNotExist(err) {
		t.Fatal("tmp/ left after drain")
	}
}
