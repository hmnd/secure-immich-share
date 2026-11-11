package tests

import (
	"archive/zip"
	"bytes"
	"io"
	"slices"
	"strconv"
	"testing"
	"time"

	fake "github.com/hmnd/secure-immich-share/server/tests/fakes/immich"
	"github.com/hmnd/secure-immich-share/server/tests/harness"
)

type zipStatus struct {
	State    string `json:"state"`
	ZipID    string `json:"zipId"`
	URL      string `json:"url"`
	Done     int    `json:"done"`
	Total    int    `json:"total"`
	Included int    `json:"included"`
	Skipped  int    `json:"skipped"`
	Size     int64  `json:"size"`
}

func startZip(t *testing.T, v *harness.Visitor, key string, headers ...string) (zipStatus, int) {
	t.Helper()
	return postZip(t, v, key, nil, headers...)
}

// startSelectionZip starts a zip of the items in ids.
func startSelectionZip(t *testing.T, v *harness.Visitor, key string, ids ...string) (zipStatus, int) {
	t.Helper()
	return postZip(t, v, key, map[string][]string{"assetIds": ids})
}

func postZip(t *testing.T, v *harness.Visitor, key string, body any, headers ...string) (zipStatus, int) {
	t.Helper()
	r := v.Post(t, "/api/shares/key/"+key+"/zip", body, headers...)
	var st zipStatus
	if r.StatusCode == 200 {
		r.JSON(t, &st)
	}
	return st, r.StatusCode
}

func waitZip(t *testing.T, v *harness.Visitor, key, id string, headers ...string) zipStatus {
	t.Helper()
	var st zipStatus
	harness.Eventually(t, 60*time.Second, func() bool {
		v.Get(t, "/api/shares/key/"+key+"/zip/"+id, headers...).JSON(t, &st)
		return st.State != "preparing"
	}, "zip never ready")
	return st
}

func zipURL(key, id string) string { return "/media/key/" + key + "/zip/" + id + "/photos.zip" }

func TestZipDownload(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	release := e.Immich.Hold()
	defer release()
	st, code := startZip(t, v, fake.PublicKey)
	if code != 200 || st.State != "preparing" || st.Total != 6 {
		t.Fatalf("start: %d %+v", code, st)
	}
	if again, _ := startZip(t, v, fake.PublicKey); again.ZipID != st.ZipID {
		t.Fatalf("second start didn't join the job: %s vs %s", again.ZipID, st.ZipID)
	}
	if r := v.Get(t, zipURL(fake.PublicKey, st.ZipID)); r.StatusCode != 404 {
		t.Fatalf("download before ready: %d", r.StatusCode)
	}
	release()
	st = waitZip(t, v, fake.PublicKey, st.ZipID)
	if st.State != "ready" || st.Included != 5 || st.Skipped != 1 {
		t.Fatalf("ready: %+v", st)
	}
	if st.URL != zipURL(fake.PublicKey, st.ZipID) {
		t.Fatalf("url %s", st.URL)
	}
	z := v.Get(t, st.URL)
	if z.StatusCode != 200 || z.Header.Get("Content-Length") != strconv.FormatInt(st.Size, 10) || int64(len(z.Body)) != st.Size {
		t.Fatalf("download: %d len=%s size=%d", z.StatusCode, z.Header.Get("Content-Length"), st.Size)
	}
	if z.Header.Get("Content-Disposition") != `attachment; filename="photos.zip"` || z.Header.Get("Cache-Control") != "no-store" ||
		z.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("headers: %v", z.Header)
	}
	zr, err := zip.NewReader(bytes.NewReader(z.Body), int64(len(z.Body)))
	if err != nil {
		t.Fatal(err)
	}
	epoch := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc) // checks the CRC
		_ = rc.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if !f.Modified.Equal(epoch) {
			t.Errorf("%s: dated %v", f.Name, f.Modified)
		}
		if f.Name == "MISSING.txt" {
			if string(body) != "These items couldn't be included:\n001\n" {
				t.Errorf("MISSING.txt: %q", body)
			}
			continue
		}
		verifies(t, e, f.Name, body)
	}
	// Newest first: the broken asset (001) is missing; the DNG comes as
	// its fullsize JPEG.
	want := []string{"002.jpg", "003.heic", "004.mp4", "005.heic", "006.jpg", "MISSING.txt"}
	if !slices.Equal(names, want) {
		t.Fatalf("entries %v, want %v", names, want)
	}

	r := v.Get(t, st.URL, "Range", "bytes=100-149")
	if r.StatusCode != 206 || !bytes.Equal(r.Body, z.Body[100:150]) {
		t.Fatalf("range: %d %d bytes", r.StatusCode, len(r.Body))
	}

	// Another share can't poll or download this job.
	if r := v.Get(t, "/api/shares/key/"+fake.IndividualKey+"/zip/"+st.ZipID); r.StatusCode != 404 {
		t.Fatalf("zip status from another share: %d", r.StatusCode)
	}
	if r := v.Get(t, zipURL(fake.IndividualKey, st.ZipID)); r.StatusCode != 404 {
		t.Fatalf("zip download from another share: %d", r.StatusCode)
	}

	e.Clock.Advance(e.Config.ZipTTL)
	if r := v.Get(t, "/api/shares/key/"+fake.PublicKey+"/zip/"+st.ZipID); r.StatusCode != 404 {
		t.Fatalf("zip status after ZIP_TTL: %d", r.StatusCode)
	}
	if r := v.Get(t, st.URL); r.StatusCode != 404 {
		t.Fatalf("zip download after ZIP_TTL: %d", r.StatusCode)
	}
}

func TestZipPasswordShare(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	if _, code := startZip(t, v, fake.PasswordKey); code != 404 {
		t.Fatalf("zip on a locked share: %d", code)
	}
	// The jar skips Secure cookies over http; send it by hand.
	hdr := harness.UnlockCookie(t, unlock(t, v, "key", fake.PasswordKey, fake.SharePassword))
	st, code := startZip(t, v, fake.PasswordKey, hdr...)
	if code != 200 {
		t.Fatalf("zip after unlock: %d", code)
	}
	if st = waitZip(t, v, fake.PasswordKey, st.ZipID, hdr...); st.State != "ready" {
		t.Fatalf("ready: %+v", st)
	}
	if r := e.Visitor().Get(t, st.URL); r.StatusCode != 404 {
		t.Fatalf("download without the cookie: %d", r.StatusCode)
	}
	if r := v.Get(t, st.URL, hdr...); r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("download with the cookie: %d", r.StatusCode)
	}
}

// zipNames downloads a ready zip and lists its entry names.
func zipNames(t *testing.T, v *harness.Visitor, st zipStatus) ([]string, map[string][]byte) {
	t.Helper()
	z := v.Get(t, st.URL)
	if z.StatusCode != 200 {
		t.Fatalf("download: %d", z.StatusCode)
	}
	zr, err := zip.NewReader(bytes.NewReader(z.Body), int64(len(z.Body)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	bodies := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		names = append(names, f.Name)
		bodies[f.Name] = body
	}
	return names, bodies
}

func TestZipSelection(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	release := e.Immich.Hold()
	defer release()
	st, code := startSelectionZip(t, v, fake.PublicKey, fake.PhotoID, fake.BrokenID, fake.RawID, fake.PhotoID)
	if code != 200 || st.State != "preparing" || st.Total != 3 {
		t.Fatalf("start: %d %+v", code, st)
	}
	if again, _ := startSelectionZip(t, v, fake.PublicKey, fake.RawID, fake.BrokenID, fake.PhotoID); again.ZipID != st.ZipID {
		t.Fatalf("same selection didn't join the job: %s vs %s", again.ZipID, st.ZipID)
	}
	other, code := startSelectionZip(t, v, fake.PublicKey, fake.PhotoID)
	if code != 200 || other.ZipID == st.ZipID || other.Total != 1 {
		t.Fatalf("other selection: %d %+v", code, other)
	}
	release()

	st = waitZip(t, v, fake.PublicKey, st.ZipID)
	if st.State != "ready" || st.Included != 2 || st.Skipped != 1 {
		t.Fatalf("ready: %+v", st)
	}
	names, bodies := zipNames(t, v, st)
	// Share positions: the broken asset is 001, the DNG 002 and the JPEG 006.
	if want := []string{"002.jpg", "006.jpg", "MISSING.txt"}; !slices.Equal(names, want) {
		t.Fatalf("entries %v, want %v", names, want)
	}
	if got := string(bodies["MISSING.txt"]); got != "These items couldn't be included:\n001\n" {
		t.Errorf("MISSING.txt: %q", got)
	}
	for _, name := range names[:2] {
		verifies(t, e, name, bodies[name])
	}

	other = waitZip(t, v, fake.PublicKey, other.ZipID)
	if names, _ := zipNames(t, v, other); !slices.Equal(names, []string{"006.jpg"}) {
		t.Fatalf("single entry zip: %v", names)
	}
}

func TestZipSelectionNotFound(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	for name, body := range map[string]any{
		"outside the share": map[string][]string{"assetIds": {fake.PhotoID, fake.OutsideID}},
		"trashed":           map[string][]string{"assetIds": {fake.TrashedID}},
		"malformed":         map[string][]string{"assetIds": {"../" + fake.PhotoID}},
		"unknown field":     map[string]any{"assetIds": []string{fake.PhotoID}, "all": true},
		"not an object":     []string{fake.PhotoID},
	} {
		if _, code := postZip(t, v, fake.PublicKey, body); code != 404 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if _, code := startSelectionZip(t, v, fake.NoDownloadKey, fake.PhotoID); code != 404 {
		t.Errorf("share without downloads: %d", code)
	}
}

func TestZipSelectionRetention(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	whole, _ := startZip(t, v, fake.PublicKey)
	whole = waitZip(t, v, fake.PublicKey, whole.ZipID)
	selections := [][]string{
		{fake.PhotoID}, {fake.HEICID}, {fake.VideoID}, {fake.LiveID}, {fake.RawID}, {fake.PhotoID, fake.HEICID},
	}
	var ids []string
	for _, sel := range selections {
		st, code := startSelectionZip(t, v, fake.PublicKey, sel...)
		if code != 200 {
			t.Fatalf("start %v: %d", sel, code)
		}
		if st = waitZip(t, v, fake.PublicKey, st.ZipID); st.State != "ready" {
			t.Fatalf("%v: %+v", sel, st)
		}
		ids = append(ids, st.ZipID)
	}
	status := func(id string) int {
		return v.Get(t, "/api/shares/key/"+fake.PublicKey+"/zip/"+id).StatusCode
	}
	if code := status(ids[0]); code != 404 {
		t.Fatalf("oldest selection zip kept: %d", code)
	}
	if r := v.Get(t, zipURL(fake.PublicKey, ids[0])); r.StatusCode != 404 {
		t.Fatalf("oldest selection zip downloads: %d", r.StatusCode)
	}
	for _, id := range append([]string{whole.ZipID}, ids[1:]...) {
		if code := status(id); code != 200 {
			t.Fatalf("zip %s dropped: %d", id, code)
		}
	}
	// Starting the dropped selection again makes a new job.
	if st, _ := startSelectionZip(t, v, fake.PublicKey, selections[0]...); st.ZipID == ids[0] {
		t.Fatalf("dropped job rejoined")
	}
}
