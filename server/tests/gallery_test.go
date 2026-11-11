package tests

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hmnd/secure-immich-share/server/internal/app"
	"github.com/hmnd/secure-immich-share/server/internal/config"
	"github.com/hmnd/secure-immich-share/server/internal/httpapi/contract"
	"github.com/hmnd/secure-immich-share/server/internal/linkref"
	fake "github.com/hmnd/secure-immich-share/server/tests/fakes/immich"
	"github.com/hmnd/secure-immich-share/server/tests/harness"
)

type shareView struct {
	Kind          string      `json:"kind"`
	Title         *string     `json:"title"`
	Description   *string     `json:"description"`
	AllowDownload bool        `json:"allowDownload"`
	ZipAvailable  bool        `json:"zipAvailable"`
	CoverID       *string     `json:"coverId"`
	Items         []shareItem `json:"items"`
}

type shareItem struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Ratio      float64  `json:"ratio"`
	Thumbhash  *string  `json:"thumbhash"`
	TakenAt    *string  `json:"takenAt"`
	ThumbURL   string   `json:"thumbUrl"`
	PreviewURL string   `json:"previewUrl"`
	Variants   []string `json:"variants"`
}

type shareResult struct {
	Status string
	Share  *shareView
}

// pagePath is the share page of a link.
func pagePath(kind, ref string) string {
	prefix := map[string]string{"key": "/share/", "slug": "/s/", "token": "/t/"}[kind]
	if prefix == "" {
		prefix = "/" + kind + "/"
	}
	return prefix + url.PathEscape(ref)
}

// getShare loads a share page and reads the listing it embeds for the
// islands: "ready" with the share, or "locked" with the password form.
func getShare(t *testing.T, v *harness.Visitor, kind, ref string, headers ...string) (harness.Response, shareResult) {
	t.Helper()
	r := v.Get(t, pagePath(kind, ref), headers...)
	var out shareResult
	if r.StatusCode == http.StatusOK {
		page, d := readPage(t, r)
		switch {
		case d.Share != nil:
			out = shareResult{Status: "ready", Share: d.Share}
		case strings.Contains(page, `<form method="post">`):
			out.Status = "locked"
		}
	}
	return r, out
}

// identifying is text from the fixtures' metadata and Immich's asset JSON
// that must never reach a visitor.
var identifying = []string{"49.2827", "123.1207", "Vancouver", "Jane", "iPhone", "Apple", "IMG_", "/upload/", "originalFileName"}

func TestPublicGallery(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	r, res := getShare(t, v, "key", fake.PublicKey)
	if r.StatusCode != 200 || res.Status != "ready" || res.Share == nil {
		t.Fatalf("getShare: %d %s", r.StatusCode, r.Body)
	}
	sh := res.Share
	if *sh.Title != "Summer 2024" || !sh.ZipAvailable || len(sh.Items) != 6 {
		t.Fatalf("share: %+v", sh)
	}
	for _, s := range identifying {
		if strings.Contains(string(r.Body), s) {
			t.Errorf("response contains %q", s)
		}
	}
	for _, it := range sh.Items {
		if it.ID == fake.TrashedID || it.ID == fake.LockedID || it.ID == fake.MotionID {
			t.Errorf("hidden asset %s listed", it.ID)
		}
		if it.TakenAt == nil || len(*it.TakenAt) != 10 {
			t.Errorf("%s takenAt %v", it.ID, it.TakenAt)
		}
		if !strings.HasPrefix(it.ThumbURL, "/media/key/"+fake.PublicKey+"/"+it.ID+"/thumb") {
			t.Errorf("thumbUrl %s", it.ThumbURL)
		}
	}
}

func TestDatesAndDescriptions(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{Config: func(c *config.Config) { c.ShowDates = false; c.ShowDescriptions = true }})
	v := e.Visitor()
	_, res := getShare(t, v, "key", fake.PublicKey)
	for _, it := range res.Share.Items {
		if it.TakenAt != nil {
			t.Fatalf("takenAt with SHOW_DATES=false: %v", *it.TakenAt)
		}
	}
	r := v.Get(t, "/api/shares/key/"+fake.PublicKey+"/assets/"+fake.PhotoID)
	var d contract.AssetDetail
	r.JSON(t, &d)
	if d.Description == nil || *d.Description != "Jane at home" || d.TakenAt != nil {
		t.Fatalf("asset detail: %s", r.Body)
	}

	e2 := harness.Start(t, harness.Options{})
	r = e2.Visitor().Get(t, "/api/shares/key/"+fake.PublicKey+"/assets/"+fake.PhotoID)
	r.JSON(t, &d)
	if d.Description != nil || d.TakenAt == nil || *d.TakenAt != "2024-07-01" {
		t.Fatalf("default asset detail: %s", r.Body)
	}
	e2.Immich.ResetRequests()
	if r := e2.Visitor().Get(t, "/api/shares/key/"+fake.PublicKey+"/assets/"+fake.OutsideID); r.StatusCode != 404 {
		t.Fatalf("asset outside share: %d", r.StatusCode)
	}
	if n := len(e2.Immich.Requests()); n != 0 {
		t.Fatalf("asset outside share reached Immich %d times", n)
	}
}

func TestLinkKinds(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	token := mint(t, e, linkref.Target{Kind: linkref.TargetKey, Value: fake.PublicKey})
	slugToken := mint(t, e, linkref.Target{Kind: linkref.TargetSlug, Value: fake.PublicSlug})

	_, byKey := getShare(t, v, "key", fake.PublicKey)
	for _, c := range []struct{ kind, ref string }{{"slug", fake.PublicSlug}, {"token", token}, {"token", slugToken}} {
		r, res := getShare(t, v, c.kind, c.ref)
		if r.StatusCode != 200 || res.Share == nil {
			t.Fatalf("%s: %d %s", c.kind, r.StatusCode, r.Body)
		}
		if len(res.Share.Items) != len(byKey.Share.Items) || *res.Share.Title != *byKey.Share.Title {
			t.Errorf("%s view differs", c.kind)
		}
		for i, it := range res.Share.Items {
			if it.ID != byKey.Share.Items[i].ID ||
				it.ThumbURL != "/media/"+c.kind+"/"+url.PathEscape(c.ref)+"/"+it.ID+"/thumb" {
				t.Errorf("%s item %d: %+v", c.kind, i, it)
			}
		}
	}

	// Tampered tokens and malformed refs: 404 before Immich.
	e.Immich.ResetRequests()
	bad := []struct{ kind, ref string }{
		{"token", token[:len(token)-2] + "AA"},
		{"token", "not-a-token"},
		{"key", fake.Key(99)[:20]},        // truncated
		{"key", fake.Key(99) + "A"},       // too long
		{"key", "+" + fake.Key(99)[1:]},   // not base64url
		{"key", strings.Repeat("a", 100)}, // hex keys are Immich's, not ours
		{"key", "has spaces"},
		{"slug", "a/b"},
		{"slug", "a b"},
		{"slug", "a\x00b"},
		{"slug", strings.Repeat("x", 129)},
		{"slug", "\xff"},
		{"other", "x"},
	}
	for _, c := range bad {
		if r, _ := getShare(t, v, c.kind, c.ref); r.StatusCode != 404 {
			t.Errorf("%s/%q: %d %s", c.kind, c.ref, r.StatusCode, r.Body)
		}
	}
	if r := v.Get(t, "/media/other/x/"+fake.PhotoID+"/thumb"); r.StatusCode != 404 {
		t.Errorf("unknown kind media: %d", r.StatusCode)
	}
	if n := len(e.Immich.Requests()); n != 0 {
		t.Errorf("malformed links reached Immich %d times", n)
	}
	if r, _ := getShare(t, v, "key", fake.Key(99)); r.StatusCode != 404 {
		t.Errorf("unknown key: %d", r.StatusCode)
	}
}

func TestDisabledKinds(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{Config: func(c *config.Config) { c.ImmichKeyLinks = false; c.ImmichSlugLinks = false }})
	v := e.Visitor()
	token := mint(t, e, linkref.Target{Kind: linkref.TargetKey, Value: fake.PublicKey})
	e.Immich.ResetRequests()
	for _, c := range []struct{ kind, ref string }{{"key", fake.PublicKey}, {"slug", fake.PublicSlug}} {
		if r, _ := getShare(t, v, c.kind, c.ref); r.StatusCode != 404 {
			t.Errorf("disabled %s: %d", c.kind, r.StatusCode)
		}
		if r := v.Get(t, "/media/"+c.kind+"/"+c.ref+"/"+fake.PhotoID+"/thumb"); r.StatusCode != 404 {
			t.Errorf("disabled %s media: %d", c.kind, r.StatusCode)
		}
	}
	if n := len(e.Immich.Requests()); n != 0 {
		t.Errorf("disabled kinds reached Immich %d times", n)
	}

	// Token-only: nothing the visitor receives contains the key or slug.
	r, res := getShare(t, v, "token", token)
	if res.Share == nil {
		t.Fatalf("token: %d %s", r.StatusCode, r.Body)
	}
	responses := []harness.Response{r}
	for _, it := range res.Share.Items[:2] {
		responses = append(responses, v.Get(t, it.ThumbURL))
	}
	responses = append(responses, v.Get(t, "/t/"+token))
	for _, resp := range responses {
		dump := string(resp.Body) + headerDump(resp.Header)
		if strings.Contains(dump, fake.PublicKey) || strings.Contains(dump, fake.PublicSlug) {
			t.Errorf("%s leaks the Immich key or slug", resp.Request.URL.Path)
		}
	}
}

func headerDump(h http.Header) string {
	var b strings.Builder
	for k, vs := range h {
		b.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
	}
	return b.String()
}

func mint(t *testing.T, e *harness.Env, target linkref.Target) string {
	t.Helper()
	ring, err := linkref.NewKeyring(e.Config.LinkKeys)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := ring.Mint(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tok, target.Value) || len(tok) > 112 {
		t.Fatalf("token %q", tok)
	}
	return tok
}

func TestRevocationAndEviction(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	if r, _ := getShare(t, v, "key", fake.PublicKey); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	e.Immich.UpdateShare(fake.PublicShareID, func(s *fake.Share) { s.Revoked = true })
	// Within SHARE_TTL the cached listing still answers…
	if r, _ := getShare(t, v, "key", fake.PublicKey); r.StatusCode != 200 {
		t.Fatalf("within TTL: %d", r.StatusCode)
	}
	// …and stops once it passes.
	e.Clock.Advance(e.Config.ShareTTL)
	if r, _ := getShare(t, v, "key", fake.PublicKey); r.StatusCode != 404 {
		t.Fatalf("after TTL: %d", r.StatusCode)
	}

	// A 403 from Immich on a media fetch evicts at once.
	e.Immich.UpdateShare(fake.PublicShareID, func(s *fake.Share) { s.Revoked = false })
	if r, _ := getShare(t, v, "key", fake.PublicKey); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	e.Immich.FailNext("/assets/"+fake.HEICID+"/thumbnail", 403)
	if r := v.Get(t, "/media/key/"+fake.PublicKey+"/"+fake.HEICID+"/thumb"); r.StatusCode != 404 {
		t.Fatalf("thumb after 403: %d", r.StatusCode)
	}
	e.Immich.UpdateShare(fake.PublicShareID, func(s *fake.Share) { s.Revoked = true })
	if r, _ := getShare(t, v, "key", fake.PublicKey); r.StatusCode != 404 {
		t.Fatalf("share still cached after a 403: %d", r.StatusCode)
	}
}

func TestDegraded(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	if r, _ := getShare(t, v, "key", fake.PublicKey); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	e.Immich.SetDown(true)
	if st := e.App.Probe(context.Background()); st != app.StateDegraded {
		t.Fatalf("state %s", st)
	}
	h := v.Get(t, "/healthz")
	if h.StatusCode != 200 || !strings.Contains(string(h.Body), "degraded") {
		t.Fatalf("healthz: %d %s", h.StatusCode, h.Body)
	}
	if r, _ := getShare(t, v, "key", fake.PublicKey); r.StatusCode != 200 {
		t.Fatalf("cached share while degraded: %d", r.StatusCode)
	}
	if r, _ := getShare(t, v, "slug", fake.PublicSlug); r.StatusCode != 503 || !strings.Contains(string(r.Body), "temporarily unavailable") {
		t.Fatalf("uncached share while degraded: %d %s", r.StatusCode, r.Body)
	}
	e.Immich.SetDown(false)
	if st := e.App.Probe(context.Background()); st != app.StateReady {
		t.Fatalf("state after recovery %s", st)
	}
	if h := v.Get(t, "/healthz"); h.StatusCode != 200 || !strings.Contains(string(h.Body), `"ready"`) {
		t.Fatalf("healthz after recovery: %d %s", h.StatusCode, h.Body)
	}
}
