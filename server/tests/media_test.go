package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/config"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize/sanitizetest"
	fake "github.com/hmnd/secure-immich-share/server/tests/fakes/immich"
	"github.com/hmnd/secure-immich-share/server/tests/harness"
)

type mediaStatus struct {
	State        string `json:"state"`
	URL          string `json:"url"`
	RetryAfterMs int64  `json:"retryAfterMs"`
}

// prepare polls prepareMedia until it settles.
func prepare(t *testing.T, v *harness.Visitor, kind, ref, id, variant string, headers ...string) mediaStatus {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		st, code := preparePost(t, v, kind, ref, id, variant, headers...)
		if code != 200 {
			return mediaStatus{State: "http_" + strconv.Itoa(code)}
		}
		if st.State == "ready" || st.State == "failed" {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("prepare %s %s never settled: %+v", id, variant, st)
		}
		time.Sleep(time.Duration(min(st.RetryAfterMs, 200)) * time.Millisecond)
	}
}

// preparePost calls prepareMedia once.
func preparePost(t *testing.T, v *harness.Visitor, kind, ref, id, variant string, headers ...string) (mediaStatus, int) {
	t.Helper()
	r := v.Post(t, "/api/shares/"+kind+"/"+ref+"/assets/"+id+"/media/"+variant+"/prepare", nil, headers...)
	var st mediaStatus
	if r.StatusCode == 200 {
		r.JSON(t, &st)
	}
	return st, r.StatusCode
}

// verifies fails the test when the bytes don't pass the verifier.
func verifies(t *testing.T, e *harness.Env, name string, body []byte) {
	t.Helper()
	p := harness.SaveBody(t, body, "bin")
	if _, err := e.Runner.Reverify(context.Background(), p); err != nil {
		t.Errorf("%s: received bytes fail verification: %v", name, err)
	}
	for _, s := range identifying {
		if bytes.Contains(body, []byte(s)) {
			t.Errorf("%s: bytes contain %q", name, s)
		}
	}
}

// TestEveryVariantVerifies fetches every variant of every item over HTTP,
// re-runs the verifier on the received bytes and checks which source each
// variant was built from.
func TestEveryVariantVerifies(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	_, res := getShare(t, v, "key", fake.PublicKey)
	contentType := map[string]string{
		fake.HEICID + "/download":  "image/heic", // the stripped original
		fake.HEICID + "/full":      "image/jpeg", // Immich's fullsize
		fake.LiveID + "/download":  "image/heic",
		fake.LiveID + "/full":      "image/jpeg",
		fake.LiveID + "/video":     "video/mp4", // the remuxed motion part
		fake.RawID + "/full":       "image/jpeg",
		fake.RawID + "/download":   "image/jpeg", // RAW is never stripped
		fake.PhotoID + "/full":     "image/jpeg",
		fake.PhotoID + "/download": "image/jpeg",
		fake.VideoID + "/download": "video/mp4", // the remuxed playback file
		fake.VideoID + "/video":    "video/mp4",
	}
	want, served := 0, 0
	for _, it := range res.Share.Items {
		urls := map[string]string{"thumb": it.ThumbURL, "preview": it.PreviewURL}
		for _, variant := range it.Variants {
			st := prepare(t, v, "key", fake.PublicKey, it.ID, variant)
			if it.ID == fake.BrokenID {
				if st.State != "failed" {
					t.Errorf("broken %s: %+v", variant, st)
				}
				continue
			}
			if st.State != "ready" {
				t.Errorf("%s %s: %+v", it.ID, variant, st)
				continue
			}
			urls[variant] = st.URL
		}
		if it.ID != fake.BrokenID {
			want += 2 + len(it.Variants)
		}
		for variant, u := range urls {
			r := v.Get(t, u)
			if it.ID == fake.BrokenID {
				if r.StatusCode != 404 || r.Code() != "not_found" {
					t.Errorf("broken %s: %d", u, r.StatusCode)
				}
				continue
			}
			if r.StatusCode != 200 {
				t.Errorf("%s: %d %s", u, r.StatusCode, r.Body)
				continue
			}
			served++
			verifies(t, e, u, r.Body)
			if ct, ok := contentType[it.ID+"/"+variant]; ok && r.Header.Get("Content-Type") != ct {
				t.Errorf("%s %s: Content-Type %q, want %q", it.ID, variant, r.Header.Get("Content-Type"), ct)
			}
			cacheControl := "private, max-age=300"
			if strings.Contains(u, "?v=") {
				cacheControl = "private, max-age=2592000, immutable"
			}
			if r.Header.Get("ETag") == "" || r.Header.Get("Cache-Control") != cacheControl {
				t.Errorf("%s: caching headers %v", u, r.Header)
			}
			if variant == "download" && !strings.HasPrefix(r.Header.Get("Content-Disposition"), `attachment; filename="00`) {
				t.Errorf("%s: disposition %q", u, r.Header.Get("Content-Disposition"))
			}
		}
	}
	if want == 0 || served != want {
		t.Fatalf("served %d of %d files", served, want)
	}
	// The live photo's video comes from its motion part; a video's download
	// is its playback file, never the original.
	if n := e.Immich.Count("GET", "/api/assets/"+fake.MotionID+"/video/playback"); n == 0 {
		t.Error("live video not built from the motion part")
	}
	if n := e.Immich.Count("GET", "/api/assets/"+fake.VideoID+"/original"); n != 0 {
		t.Errorf("video original fetched %d times", n)
	}
	if n := e.Immich.Count("GET", "/api/assets/"+fake.RawID+"/original"); n != 0 {
		t.Errorf("RAW original fetched %d times", n)
	}
}

// TestVersionedMediaCaching: thumbs and previews of items with a thumbhash
// get versioned URLs, cacheable for long only at the current version.
func TestVersionedMediaCaching(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	_, res := getShare(t, v, "key", fake.PublicKey)
	items := map[string]shareItem{}
	for _, it := range res.Share.Items {
		items[it.ID] = it
	}
	heic, photo := items[fake.HEICID], items[fake.PhotoID]
	if !strings.Contains(heic.ThumbURL, "?v=") || !strings.Contains(heic.PreviewURL, "?v=") {
		t.Fatalf("item with a thumbhash has unversioned URLs: %+v", heic)
	}
	if strings.Contains(photo.ThumbURL, "?") || strings.Contains(photo.PreviewURL, "?") {
		t.Errorf("item without a thumbhash has versioned URLs: %+v", photo)
	}
	path, _, _ := strings.Cut(heic.ThumbURL, "?")
	for u, want := range map[string]string{
		heic.ThumbURL:     "private, max-age=2592000, immutable",
		heic.PreviewURL:   "private, max-age=2592000, immutable",
		path:              "private, max-age=300",
		path + "?v=stale": "private, max-age=300",
		photo.ThumbURL:    "private, max-age=300",
	} {
		r := v.Get(t, u)
		if r.StatusCode != 200 || r.Header.Get("Cache-Control") != want {
			t.Errorf("%s: %d Cache-Control %q, want %q", u, r.StatusCode, r.Header.Get("Cache-Control"), want)
		}
	}
}

func TestMediaAccess(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	// No downloads: full and download are refused, and no original is fetched.
	nd := "/media/key/" + fake.NoDownloadKey + "/"
	for _, variant := range []string{"full", "download"} {
		if r := v.Get(t, nd+fake.PhotoID+"/"+variant); r.StatusCode != 404 {
			t.Errorf("no-download %s: %d", variant, r.StatusCode)
		}
		if st := prepare(t, v, "key", fake.NoDownloadKey, fake.PhotoID, variant); st.State != "http_404" {
			t.Errorf("no-download prepare %s: %+v", variant, st)
		}
	}
	if r := v.Post(t, "/api/shares/key/"+fake.NoDownloadKey+"/zip", nil); r.StatusCode != 404 {
		t.Errorf("no-download zip: %d", r.StatusCode)
	}
	if n := e.Immich.Count("GET", "/api/assets/"+fake.PhotoID+"/original"); n != 0 {
		t.Errorf("original fetched %d times", n)
	}
	_, res := getShare(t, v, "key", fake.NoDownloadKey)
	for _, it := range res.Share.Items {
		for _, variant := range it.Variants {
			if variant == "full" || variant == "download" {
				t.Errorf("no-download share lists %v", it.Variants)
			}
		}
	}
	if r := v.Get(t, nd+fake.PhotoID+"/preview"); r.StatusCode != 200 {
		t.Errorf("no-download preview: %d", r.StatusCode)
	}
}

// TestOutsideAndHidden: assets outside the share, hidden or unknown are
// refused for every variant without a single request to Immich.
func TestOutsideAndHidden(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	getShare(t, v, "key", fake.PublicKey)
	e.Immich.ResetRequests()
	base := "/media/key/" + fake.PublicKey + "/"
	ids := []string{fake.OutsideID, fake.TrashedID, fake.LockedID, fake.MotionID, "a0000000-0000-4000-8000-0000000000ff"}
	for _, id := range ids {
		for _, variant := range []string{"thumb", "preview", "full", "download", "video"} {
			if r := v.Get(t, base+id+"/"+variant); r.StatusCode != 404 {
				t.Errorf("%s %s: %d", id, variant, r.StatusCode)
			}
			if _, code := preparePost(t, v, "key", fake.PublicKey, id, variant); code != 404 {
				t.Errorf("%s %s prepare: %d", id, variant, code)
			}
		}
	}
	for _, r := range e.Immich.Requests() {
		if !strings.HasPrefix(r.Path, "/api/server/") {
			t.Errorf("reached Immich: %s %s", r.Method, r.Path)
		}
	}
}

func TestFailedJobMemoryAndEdits(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	base := "/media/key/" + fake.PublicKey + "/"
	thumb := base + fake.BrokenID + "/thumb"
	brokenFetches := func() int { return e.Immich.Count("GET", "/api/assets/"+fake.BrokenID+"/thumbnail") }
	for range 3 {
		if r := v.Get(t, thumb); r.StatusCode != 404 {
			t.Fatalf("broken thumb: %d", r.StatusCode)
		}
	}
	if n := brokenFetches(); n != 1 {
		t.Fatalf("failing asset fetched %d times within FAILED_TTL", n)
	}

	// An Immich outage is remembered for about 30 s only.
	photo := base + fake.PhotoID + "/thumb"
	photoFetches := func() int { return e.Immich.Count("GET", "/api/assets/"+fake.PhotoID+"/thumbnail") }
	e.Immich.FailNext("/assets/"+fake.PhotoID+"/thumbnail", 503)
	if r := v.Get(t, photo); r.StatusCode != 404 {
		t.Fatalf("thumb during outage: %d", r.StatusCode)
	}
	if r := v.Get(t, photo); r.StatusCode != 404 || photoFetches() != 1 {
		t.Fatalf("outage not remembered: %d, %d fetches", r.StatusCode, photoFetches())
	}
	e.Clock.Advance(30 * time.Second)
	if r := v.Get(t, photo); r.StatusCode != 200 || photoFetches() != 2 {
		t.Fatalf("not retried after the outage: %d, %d fetches", r.StatusCode, photoFetches())
	}

	preview := base + fake.HEICID + "/preview"
	first := v.Get(t, preview)
	e.Immich.UpdateAsset(fake.HEICID, func(a *fake.Asset) {
		a.UpdatedAt = a.UpdatedAt.Add(time.Hour)
		a.Preview = a.Thumbnail // a different file: the edit
	})
	e.Clock.Advance(3 * time.Minute)
	second := v.Get(t, preview)
	if second.StatusCode != 200 || second.Header.Get("ETag") == first.Header.Get("ETag") || bytes.Equal(first.Body, second.Body) {
		t.Fatal("an edit in Immich didn't produce a new cache entry")
	}

	// FAILED_TTL after the failure, the job runs again.
	e.Clock.Advance(e.Config.FailedTTL)
	_ = v.Get(t, thumb)
	if n := brokenFetches(); n != 2 {
		t.Fatalf("not retried after FAILED_TTL (%d fetches)", n)
	}
}

func TestPrepareStates(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{Config: func(c *config.Config) { c.SanitizeConcurrency = 1; c.SanitizeQueue = 1 }})
	v := e.Visitor()
	getShare(t, v, "key", fake.PublicKey)
	release := e.Immich.Hold()
	defer release()
	post := func(id, variant string) mediaStatus {
		st, code := preparePost(t, v, "key", fake.PublicKey, id, variant)
		if code != 200 {
			t.Fatalf("prepare %s %s: %d", id, variant, code)
		}
		return st
	}
	if st := post(fake.VideoID, "video"); st.State != "processing" {
		t.Fatalf("first: %+v", st)
	}
	if st := post(fake.LiveID, "video"); st.State != "queued" {
		t.Fatalf("second: %+v", st)
	}
	if st := post(fake.PhotoID, "download"); st.State != "busy" || st.RetryAfterMs <= 0 {
		t.Fatalf("third: %+v", st)
	}
	// Joining a queued job is not new work, so it isn't busy.
	if st := post(fake.LiveID, "video"); st.State != "queued" {
		t.Fatalf("second again: %+v", st)
	}
	release()
	for _, id := range []string{fake.VideoID, fake.LiveID} {
		if st := prepare(t, v, "key", fake.PublicKey, id, "video"); st.State != "ready" {
			t.Fatalf("%s video: %+v", id, st)
		}
	}
	if st := prepare(t, v, "key", fake.PublicKey, fake.BrokenID, "full"); st.State != "failed" {
		t.Fatalf("broken full: %+v", st)
	}
}

func TestMediaRange(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	u := "/media/key/" + fake.PublicKey + "/" + fake.VideoID + "/video"
	whole := v.Get(t, u)
	if whole.StatusCode != 200 {
		t.Fatalf("video: %d", whole.StatusCode)
	}
	r := v.Get(t, u, "Range", "bytes=0-99")
	if r.StatusCode != 206 || !bytes.Equal(r.Body, whole.Body[:100]) ||
		r.Header.Get("Content-Range") != "bytes 0-99/"+strconv.Itoa(len(whole.Body)) {
		t.Fatalf("range: %d %d %s", r.StatusCode, len(r.Body), r.Header.Get("Content-Range"))
	}
	if r.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("content type %s", r.Header.Get("Content-Type"))
	}
}

// TestSourceFallback: a source over MAX_SOURCE_BYTES and a source the
// verifier rejects both fall back to the next step of the chain.
func TestSourceFallback(t *testing.T) {
	t.Parallel()
	derivative := sanitizetest.Clean(t, "derivative.jpg")
	st, err := os.Stat(derivative)
	if err != nil {
		t.Fatal(err)
	}
	e := harness.Start(t, harness.Options{Config: func(c *config.Config) { c.MaxSourceBytes = st.Size() }})
	const (
		bigID      = "a0000000-0000-4000-8000-0000000000b1"
		rejectedID = "a0000000-0000-4000-8000-0000000000b2"
	)
	key := fake.Key(9)
	junk := filepath.Join(t.TempDir(), "junk")
	if err := os.WriteFile(junk, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	for id, original := range map[string]string{
		bigID:      sanitizetest.ByName(t, "pixel-motion").Path,
		rejectedID: junk,
	} {
		e.Immich.AddAsset(fake.Asset{ID: id, Type: "IMAGE", Mime: "image/heic", Checksum: id, Width: 320, Height: 240,
			Original: original, Fullsize: derivative, Preview: derivative, Thumbnail: derivative})
	}
	e.Immich.AddShare(fake.Share{ID: "b0000000-0000-4000-8000-0000000000b1", Key: key, AllowDownload: true,
		AssetIDs: []string{bigID, rejectedID}})
	v := e.Visitor()
	for _, id := range []string{bigID, rejectedID} {
		r := v.Get(t, "/media/key/"+key+"/"+id+"/download")
		if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/jpeg" {
			t.Fatalf("%s download: %d %s", id, r.StatusCode, r.Header.Get("Content-Type"))
		}
		verifies(t, e, id, r.Body)
		fullsize := 0
		for _, req := range e.Immich.Requests() {
			if req.Path == "/api/assets/"+id+"/thumbnail" && req.Query.Get("size") == "fullsize" {
				fullsize++
			}
		}
		if e.Immich.Count("GET", "/api/assets/"+id+"/original") != 1 || fullsize != 1 {
			t.Errorf("%s: original then fullsize not fetched once each", id)
		}
	}
	if !strings.Contains(e.Logs.String(), "media: verification rejected") {
		t.Error("rejected original not logged")
	}
}

// TestEditedImage: an edited image's original lacks the edits, so full and
// download come from Immich's fullsize render and the original is never
// fetched. Dates are off so the render is served byte for byte.
func TestEditedImage(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{Config: func(c *config.Config) { c.ShowDates = false }})
	const id = "a0000000-0000-4000-8000-0000000000e1"
	key := fake.Key(10)
	render := sanitizetest.Clean(t, "derivative.webp")
	want, err := os.ReadFile(render)
	if err != nil {
		t.Fatal(err)
	}
	e.Immich.AddAsset(fake.Asset{ID: id, Type: "IMAGE", Mime: "image/jpeg", Checksum: id, Width: 320, Height: 240,
		Edited: true, Original: sanitizetest.ByName(t, "jpeg-gps").Path, Fullsize: render,
		Preview: sanitizetest.Clean(t, "derivative.jpg"), Thumbnail: sanitizetest.Clean(t, "derivative.jpg")})
	e.Immich.AddShare(fake.Share{ID: "b0000000-0000-4000-8000-0000000000e1", Key: key, AllowDownload: true,
		AssetIDs: []string{id}})
	v := e.Visitor()
	for _, variant := range []string{"full", "download"} {
		r := v.Get(t, "/media/key/"+key+"/"+id+"/"+variant)
		if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/webp" || !bytes.Equal(r.Body, want) {
			t.Fatalf("%s: %d %s, not the fullsize render", variant, r.StatusCode, r.Header.Get("Content-Type"))
		}
		verifies(t, e, variant, r.Body)
	}
	fullsize := 0
	for _, req := range e.Immich.Requests() {
		if req.Path == "/api/assets/"+id+"/thumbnail" && req.Query.Get("size") == "fullsize" {
			fullsize++
		}
	}
	if fullsize != 2 {
		t.Errorf("fullsize fetched %d times, want once per variant", fullsize)
	}
	if n := e.Immich.Count("GET", "/api/assets/"+id+"/original"); n != 0 {
		t.Errorf("edited original fetched %d times", n)
	}
}

// TestDateTaken: with SHOW_DATES, full-size images and downloads carry the
// date taken from Immich (local time and offset for images, UTC for videos)
// so phone galleries sort saved files by it; thumbs, previews and playback
// stay undated, and without SHOW_DATES everything does.
func TestDateTaken(t *testing.T) {
	t.Parallel()
	tools := sanitizetest.LookTools(t)
	want := map[string][2]string{
		fake.PhotoID: {"ExifIFD:DateTimeOriginal", "2024:07:01 18:00:00"},
		fake.HEICID:  {"ExifIFD:DateTimeOriginal", "2024:07:02 18:00:00"},
		fake.LiveID:  {"ExifIFD:DateTimeOriginal", "2024:07:04 18:00:00"},
		fake.RawID:   {"ExifIFD:DateTimeOriginal", "2024:07:05 18:00:00"},
		fake.VideoID: {"QuickTime:CreateDate", "2024:07:04 01:00:00"},
	}
	for _, showDates := range []bool{true, false} {
		t.Run("show_dates="+strconv.FormatBool(showDates), func(t *testing.T) {
			t.Parallel()
			e := harness.Start(t, harness.Options{Config: func(c *config.Config) { c.ShowDates = showDates }})
			v := e.Visitor()
			_, res := getShare(t, v, "key", fake.PublicKey)
			dated := 0
			for _, it := range res.Share.Items {
				w, ok := want[it.ID]
				if !ok {
					continue
				}
				urls := map[string]string{"thumb": it.ThumbURL, "preview": it.PreviewURL}
				for _, variant := range it.Variants {
					st := prepare(t, v, "key", fake.PublicKey, it.ID, variant)
					if st.State != "ready" {
						t.Errorf("%s %s: %+v", it.ID, variant, st)
						continue
					}
					urls[variant] = st.URL
				}
				for variant, u := range urls {
					r := v.Get(t, u)
					if r.StatusCode != 200 {
						t.Errorf("%s: %d", u, r.StatusCode)
						continue
					}
					verifies(t, e, u, r.Body)
					tags := exifTags(t, tools.Exiftool, harness.SaveBody(t, r.Body, "bin"))
					got := tags[w[0]]
					saved := variant == "full" || variant == "download"
					switch {
					case showDates && saved:
						if got != w[1] {
							t.Errorf("%s %s: %s %v, want %q", it.ID, variant, w[0], got, w[1])
						}
						if off := tags["ExifIFD:OffsetTimeOriginal"]; it.ID != fake.VideoID && off != "-07:00" {
							t.Errorf("%s %s: offset %v", it.ID, variant, off)
						}
						dated++
					case got != nil && got != "0000:00:00 00:00:00":
						t.Errorf("%s %s: dated %s %v", it.ID, variant, w[0], got)
					}
				}
			}
			if showDates && dated == 0 {
				t.Error("no saved files checked")
			}
		})
	}
}

func exifTags(t *testing.T, exiftool, p string) map[string]any {
	t.Helper()
	out, err := exec.Command(exiftool, "-j", "-a", "-G1", p).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var tags []map[string]any
	if err := json.Unmarshal(out, &tags); err != nil || len(tags) != 1 {
		t.Fatalf("exiftool json: %v", err)
	}
	return tags[0]
}
