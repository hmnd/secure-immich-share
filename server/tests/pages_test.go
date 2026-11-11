package tests

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/hmnd/secure-immich-share/server/internal/config"
	fake "github.com/hmnd/secure-immich-share/server/tests/fakes/immich"
	"github.com/hmnd/secure-immich-share/server/tests/harness"
)

var (
	scriptTag   = regexp.MustCompile(`<script[^>]*>`)
	shareDataJS = regexp.MustCompile(`(?s)<script id="share-data" type="application/json">(.*?)</script>`)
)

type pageData struct {
	Kind  string     `json:"kind"`
	Ref   string     `json:"ref"`
	Share *shareView `json:"share"`
	Photo *int       `json:"photo"`
}

func readPage(t *testing.T, r harness.Response) (string, pageData) {
	t.Helper()
	page := string(r.Body)
	if ct := r.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("%s: content type %q", r.Request.URL, ct)
	}
	var d pageData
	if m := shareDataJS.FindStringSubmatch(page); m != nil {
		if err := json.Unmarshal([]byte(m[1]), &d); err != nil {
			t.Fatalf("share data: %v", err)
		}
	}
	return page, d
}

func TestSharePage(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	r := v.Get(t, "/share/"+fake.PublicKey)
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("page: %d %q", r.StatusCode, r.Header.Get("Cache-Control"))
	}
	page, d := readPage(t, r)
	if !strings.Contains(page, "<title>Summer 2024</title>") || !strings.Contains(page, "<h1>Summer 2024</h1>") {
		t.Fatalf("title missing: %s", page)
	}
	if d.Kind != "key" || d.Ref != fake.PublicKey || len(d.Share.Items) != 6 || d.Photo != nil {
		t.Fatalf("share data: %+v", d)
	}
	for _, it := range d.Share.Items {
		link := `href="/share/` + fake.PublicKey + `?photo=` + it.ID + `"`
		if !strings.Contains(page, link) || !strings.Contains(page, `src="`+it.ThumbURL+`"`) {
			t.Errorf("tile for %s missing", it.ID)
		}
	}
	for _, s := range identifying {
		if strings.Contains(page, s) {
			t.Errorf("page contains %q", s)
		}
	}
	for _, tag := range scriptTag.FindAllString(page, -1) {
		if tag != `<script id="share-data" type="application/json">` && !strings.Contains(tag, ` src="`) {
			t.Errorf("inline script %s", tag)
		}
	}
	if strings.Contains(page, `class="viewer"`) {
		t.Error("viewer without ?photo=")
	}
}

func TestPhotoPage(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	_, d := readPage(t, v.Get(t, "/share/"+fake.PublicKey))
	items := d.Share.Items
	base := "/share/" + fake.PublicKey
	media := "/media/key/" + fake.PublicKey + "/"

	page, d := readPage(t, v.Get(t, base+"?photo="+items[0].ID))
	if d.Photo == nil || *d.Photo != 0 {
		t.Fatalf("photo index: %v", d.Photo)
	}
	for _, want := range []string{
		`class="viewer"`, `src="` + items[0].PreviewURL + `"`, `1 / 6`, ` July 2024 at 18:00 (UTC-7)</p>`,
		`href="` + base + `#p-` + items[0].ID + `"`,
		`href="` + base + `?photo=` + items[1].ID + `" aria-label="Next"`,
		`href="` + media + items[0].ID + `/full"`, `href="` + media + items[0].ID + `/download" download`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("first photo lacks %s", want)
		}
	}
	if strings.Contains(page, `aria-label="Previous"`) {
		t.Error("first photo has a previous link")
	}

	page, _ = readPage(t, v.Get(t, base+"?photo="+fake.VideoID))
	if !strings.Contains(page, `<video src="`+media+fake.VideoID+`/video"`) {
		t.Errorf("video page: %s", page)
	}
	page, _ = readPage(t, v.Get(t, base+"?photo="+fake.LiveID))
	if !strings.Contains(page, `href="`+media+fake.LiveID+`/video" aria-label="Play live photo"`) {
		t.Error("live photo without its motion link")
	}
	for _, id := range []string{fake.TrashedID, fake.OutsideID, "nope"} {
		if r := v.Get(t, base+"?photo="+id); r.StatusCode != 200 || strings.Contains(string(r.Body), `class="viewer"`) {
			t.Errorf("hidden photo %s: %d", id, r.StatusCode)
		}
	}
	if strings.Contains(page, "Jane at home") {
		t.Error("description with SHOW_DESCRIPTIONS=false")
	}

	e2 := harness.Start(t, harness.Options{Config: func(c *config.Config) { c.ShowDescriptions = true }})
	r := e2.Visitor().Get(t, base+"?photo="+fake.PhotoID)
	if !strings.Contains(string(r.Body), "Jane at home") {
		t.Error("description missing with SHOW_DESCRIPTIONS=true")
	}
}

func TestLockedPage(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{Config: func(c *config.Config) { c.TrustedProxies = trustedLoopback() }})
	v := e.Visitor()
	v.IP = "198.51.100.7"
	path := "/share/" + fake.PasswordKey
	r := v.Get(t, path)
	page, d := readPage(t, r)
	if r.StatusCode != 200 || !strings.Contains(page, `<form method="post">`) || d.Ref != "" {
		t.Fatalf("locked page: %d %s", r.StatusCode, page)
	}
	if strings.Contains(page, "Private") || strings.Contains(page, fake.PhotoID) {
		t.Fatal("locked page shows the share")
	}

	form := func(password string, headers ...string) harness.Response {
		return v.Form(t, path, url.Values{"password": {password}}, headers...)
	}
	for _, hdr := range [][]string{
		{"Sec-Fetch-Site", "cross-site"},
		{"Sec-Fetch-Site", "same-site"},
		{"Sec-Fetch-Site", "", "Origin", "https://evil.example"},
	} {
		if r := form(fake.SharePassword, hdr...); r.StatusCode != 403 {
			t.Errorf("%v: %d", hdr, r.StatusCode)
		}
	}
	if n := e.Immich.Count("POST", "/api/shared-links/login"); n != 0 {
		t.Fatalf("cross-site forms reached Immich %d times", n)
	}

	r = form("wrong")
	if r.StatusCode != 401 || !strings.Contains(string(r.Body), "Wrong password") {
		t.Fatalf("wrong password: %d %s", r.StatusCode, r.Body)
	}
	r = form(fake.SharePassword)
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != path {
		t.Fatalf("unlock: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	cookie := harness.UnlockCookie(t, r)
	page, d = readPage(t, v.Get(t, path, cookie...))
	if !strings.Contains(page, "<h1>Private</h1>") || len(d.Share.Items) == 0 {
		t.Fatalf("unlocked page: %s", page)
	}
	if strings.Contains(page, `og:image`) {
		t.Error("unlocked page has an og:image")
	}
	// Posting to a public share's page just goes back to it.
	if r := v.Form(t, "/share/"+fake.PublicKey, url.Values{"password": {"x"}}); r.StatusCode != http.StatusSeeOther {
		t.Errorf("public share form: %d", r.StatusCode)
	}

	for range 5 {
		form("guess")
	}
	r = form(fake.SharePassword)
	if r.StatusCode != 429 || r.Header.Get("Retry-After") != "60" || !strings.Contains(string(r.Body), "Try again in 60s") {
		t.Fatalf("throttled: %d %q %s", r.StatusCode, r.Header.Get("Retry-After"), r.Body)
	}
}

func TestMessagePages(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	for _, c := range []struct {
		path   string
		status int
		text   string
	}{
		{"/", 200, "Nothing to see here"},
		{"/nope", 404, "Page not found"},
		{"/share/" + fake.Key(99), 404, "doesn’t exist or has expired"},
		{"/t/not-a-token", 404, "doesn’t exist or has expired"},
		{"/.vite/manifest.json", 404, "Page not found"},
	} {
		r := v.Get(t, c.path)
		page, _ := readPage(t, r)
		if r.StatusCode != c.status || !strings.Contains(page, c.text) {
			t.Errorf("%s: %d %s", c.path, r.StatusCode, page)
		}
	}
	e.Immich.SetDown(true)
	r := v.Get(t, "/s/"+fake.PublicSlug)
	page, _ := readPage(t, r)
	if r.StatusCode != 503 || !strings.Contains(page, "temporarily unavailable") ||
		!strings.Contains(page, `href="/s/`+fake.PublicSlug+`">Retry`) {
		t.Fatalf("unavailable: %d %s", r.StatusCode, page)
	}
}

func TestPageEscaping(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	name := `<script>alert(1)</script>`
	desc := `"><img src=x onerror=alert(2)></script><script>alert(3)</script>`
	e.Immich.UpdateShare(fake.PublicShareID, func(s *fake.Share) { s.AlbumName, s.AlbumDesc = name, desc })
	r := e.Visitor().Get(t, "/share/"+fake.PublicKey)
	page, d := readPage(t, r)
	for _, raw := range []string{"<script>alert", "<img src=x"} {
		if strings.Contains(page, raw) {
			t.Errorf("page contains %q unescaped", raw)
		}
	}
	if n, m := strings.Count(page, "<script"), strings.Count(page, "</script>"); n != m {
		t.Errorf("%d script tags, %d closing tags", n, m)
	}
	if d.Share == nil || d.Share.Title == nil || *d.Share.Title != name || *d.Share.Description != desc {
		t.Fatalf("share data: %+v", d.Share)
	}
	if !strings.Contains(page, "<title>&lt;script&gt;alert(1)&lt;/script&gt;</title>") {
		t.Errorf("title not escaped: %s", page)
	}
}
