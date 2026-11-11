package tests

import (
	"testing"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/config"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize/sanitizetest"
	fake "github.com/hmnd/secure-immich-share/server/tests/fakes/immich"
	"github.com/hmnd/secure-immich-share/server/tests/harness"
)

func prewarmEnv(t *testing.T) *harness.Env {
	t.Helper()
	return harness.Start(t, harness.Options{Config: func(c *config.Config) { c.PrewarmVideos = true }})
}

func playbackFetches(e *harness.Env, id string) int {
	return e.Immich.Count("GET", "/api/assets/"+id+"/video/playback")
}

// detailFetches counts asset detail requests (not its media).
func detailFetches(e *harness.Env, id string) int {
	n := 0
	for _, r := range e.Immich.Requests() {
		if r.Method == "GET" && r.Path == "/api/assets/"+id {
			n++
		}
	}
	return n
}

func TestPrewarm(t *testing.T) {
	t.Parallel()
	e := prewarmEnv(t)
	v := e.Visitor()
	getShare(t, v, "key", fake.PublicKey)
	harness.Eventually(t, 30*time.Second, func() bool {
		return playbackFetches(e, fake.VideoID) == 1 && playbackFetches(e, fake.MotionID) == 1
	}, "videos not prewarmed")
	for _, id := range []string{fake.VideoID, fake.LiveID} {
		if st := prepare(t, v, "key", fake.PublicKey, id, "video"); st.State != "ready" {
			t.Fatalf("%s: %+v", id, st)
		}
	}
	if n := e.Immich.Count("GET", "/api/assets/"+fake.PhotoID+"/thumbnail"); n != 0 {
		t.Errorf("images prewarmed without PREWARM_IMAGES (%d)", n)
	}

	// Within PREWARM_TTL the same listing schedules nothing, even once the
	// details it would plan with have expired.
	e.Clock.Advance(3 * time.Minute)
	e.Immich.ResetRequests()
	getShare(t, v, "key", fake.PublicKey)

	// A new listing schedules a run. Its new video is last in the listing,
	// so once it is fetched the run has planned every other item too.
	const newVideoID = "a0000000-0000-4000-8000-0000000000c1"
	derivative := sanitizetest.Clean(t, "derivative.jpg")
	e.Immich.AddAsset(fake.Asset{ID: newVideoID, Type: "VIDEO", Mime: "video/mp4", Checksum: "new-video",
		Width: 320, Height: 240, LocalDateTime: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
		Playback: sanitizetest.ByName(t, "mp4-gps").Path, Preview: derivative, Thumbnail: derivative})
	e.Immich.UpdateShare(fake.PublicShareID, func(s *fake.Share) { s.AssetIDs = append(s.AssetIDs, newVideoID) })
	e.Clock.Advance(3 * time.Minute)
	getShare(t, v, "key", fake.PublicKey)
	harness.Eventually(t, 30*time.Second, func() bool { return playbackFetches(e, newVideoID) == 1 },
		"new listing not prewarmed")
	// One run since the reset: the unchanged listing planned nothing.
	if n := detailFetches(e, fake.VideoID); n != 1 {
		t.Fatalf("video detail fetched %d times since the reset, want 1", n)
	}
	if n := playbackFetches(e, fake.VideoID); n != 0 {
		t.Fatalf("committed video fetched again (%d)", n)
	}
}

// TestPrewarmSkippedIsRetried: a run that skipped a video (Immich failed)
// returns to idle, so the next listing load runs it again.
func TestPrewarmSkippedIsRetried(t *testing.T) {
	t.Parallel()
	e := prewarmEnv(t)
	v := e.Visitor()
	e.Immich.FailNext("/assets/"+fake.VideoID, 503)
	getShare(t, v, "key", fake.PublicKey)
	harness.Eventually(t, 30*time.Second, func() bool {
		getShare(t, v, "key", fake.PublicKey)
		return playbackFetches(e, fake.VideoID) == 1
	}, "skipped prewarm not retried")
	if n := detailFetches(e, fake.VideoID); n != 2 {
		t.Fatalf("video detail fetched %d times, want the failure and the retry", n)
	}
}
