package tests

import (
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/config"
	"github.com/hmnd/secure-immich-share/server/internal/linkref"
	fake "github.com/hmnd/secure-immich-share/server/tests/fakes/immich"
	"github.com/hmnd/secure-immich-share/server/tests/harness"
)

func trustedLoopback() []netip.Prefix {
	return []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
}

// checkSecurityHeaders asserts the headers every response carries.
func checkSecurityHeaders(t *testing.T, r harness.Response) {
	t.Helper()
	path, h := r.Request.URL.Path, r.Header
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "frame-ancestors 'none'", "script-src 'self'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("%s: CSP %q lacks %q", path, csp, want)
		}
	}
	for k, want := range map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"X-Robots-Tag":                 "noindex, nofollow",
	} {
		if h.Get(k) != want {
			t.Errorf("%s: %s = %q", path, k, h.Get(k))
		}
	}
	if h.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("%s: CORS header", path)
	}
	for _, want := range []string{"geolocation=()", "web-share=(self)", "screen-wake-lock=(self)"} {
		if !strings.Contains(h.Get("Permissions-Policy"), want) {
			t.Errorf("%s: Permissions-Policy %q lacks %q", path, h.Get("Permissions-Policy"), want)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	tok := mint(t, e, linkref.Target{Kind: linkref.TargetKey, Value: fake.PublicKey})
	for _, path := range []string{"/", "/share/" + fake.PublicKey, "/t/" + tok,
		"/api/shares/key/" + fake.PublicKey + "/assets/" + fake.PhotoID,
		"/media/key/" + fake.PublicKey + "/" + fake.PhotoID + "/thumb", "/api/nope", "/healthz", harness.AssetScript} {
		r := v.Get(t, path)
		checkSecurityHeaders(t, r)
		if r.Header.Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: HSTS over http", path)
		}
	}
	e.Immich.SetDown(true)
	r := v.Get(t, "/s/"+fake.PublicSlug)
	if r.StatusCode != 503 {
		t.Fatalf("unavailable: %d", r.StatusCode)
	}
	checkSecurityHeaders(t, r)

	https := harness.Start(t, harness.Options{Config: func(c *config.Config) { c.PublicURL = "https://photos.example.com" }})
	if hsts := https.Visitor().Get(t, "/").Header.Get("Strict-Transport-Security"); hsts != "max-age=31536000" {
		t.Errorf("HSTS with an https PUBLIC_URL: %q", hsts)
	}
	if r := v.Get(t, "/api/nope"); r.StatusCode != 404 || r.Code() != "not_found" {
		t.Errorf("unknown api path: %d %s", r.StatusCode, r.Body)
	}
	if r := v.Get(t, "/media/nope"); r.StatusCode != 404 || r.Code() != "not_found" {
		t.Errorf("unknown media path: %d", r.StatusCode)
	}
	if r := v.Do(t, "DELETE", "/somewhere", nil); r.StatusCode != 405 {
		t.Errorf("non-GET page: %d", r.StatusCode)
	}
}

func TestCSRF(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	path := "/api/shares/key/" + fake.PublicKey + "/zip"
	cases := [][]string{
		{"Sec-Fetch-Site", "cross-site", "Origin", "https://evil.example"},
		{"Sec-Fetch-Site", "same-site", "Origin", "null"},
		{"Sec-Fetch-Site", "", "Origin", "https://evil.example"},
		{"Sec-Fetch-Site", "", "Origin", "null"},
	}
	e.Immich.ResetRequests()
	for _, hdr := range cases {
		if r := v.Post(t, path, nil, hdr...); r.StatusCode != 403 || r.Code() != "csrf" {
			t.Errorf("%v: %d %s", hdr, r.StatusCode, r.Body)
		}
	}
	if n := len(e.Immich.Requests()); n != 0 {
		t.Errorf("CSRF-failing requests reached Immich %d times", n)
	}
	big := url.Values{"password": {strings.Repeat("x", 20<<10)}}
	if r := v.Form(t, "/share/"+fake.PasswordKey, big); r.StatusCode != 400 {
		t.Errorf("oversized form: %d", r.StatusCode)
	}
}

func TestOpenGraph(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	r := v.Get(t, "/share/"+fake.PublicKey, "Host", "evil.example")
	page := string(r.Body)
	wantImg := e.Config.PublicURL + "/media/key/" + fake.PublicKey + "/"
	if !strings.Contains(page, `og:title" content="Summer 2024"`) || !strings.Contains(page, `og:image" content="`+wantImg) {
		t.Fatalf("public OG: %s", page)
	}
	if strings.Contains(page, "evil.example") {
		t.Fatal("og built from the Host header")
	}
	r = v.Get(t, "/share/"+fake.PasswordKey)
	if strings.Contains(string(r.Body), "og:image") || strings.Contains(string(r.Body), "Private") {
		t.Fatalf("password share OG: %s", r.Body)
	}
	if r := v.Get(t, "/s/"+fake.PublicSlug); !strings.Contains(string(r.Body), "Summer 2024") {
		t.Fatal("slug page without OG")
	}
	tok := mint(t, e, linkref.Target{Kind: linkref.TargetKey, Value: fake.PublicKey})
	if r := v.Get(t, "/t/"+tok); !strings.Contains(string(r.Body), "/media/token/"+tok+"/") {
		t.Fatal("token page without its own og:image")
	}
}

func TestLogsNeverHoldKeys(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	const password = "zebra-Quokka-7731"
	e.Immich.UpdateShare(fake.PasswordShareID, func(s *fake.Share) { s.Password = password })
	v := e.Visitor()
	tok := mint(t, e, linkref.Target{Kind: linkref.TargetKey, Value: fake.PublicKey})
	getShare(t, v, "key", fake.PublicKey)
	getShare(t, v, "slug", fake.PublicSlug)
	getShare(t, v, "token", tok)
	v.Get(t, "/media/key/"+fake.PublicKey+"/"+fake.PhotoID+"/thumb")
	v.Get(t, "/media/token/"+tok+"/"+fake.BrokenID+"/thumb")
	unlock(t, v, "key", fake.PasswordKey, "wrong")
	unlock(t, v, "token", tok, password)
	cookie := harness.UnlockCookie(t, unlock(t, v, "key", fake.PasswordKey, password))
	getShare(t, v, "key", fake.PasswordKey, cookie...)
	var zip struct {
		ZipID string `json:"zipId"`
	}
	v.Post(t, "/api/shares/token/"+tok+"/zip", nil).JSON(t, &zip)
	v.Get(t, "/api/shares/token/"+tok+"/zip/"+zip.ZipID)
	logs := e.Logs.String()
	for _, route := range []string{"GET /share/{ref}", "POST /t/{ref}", "/api/shares/{kind}/{ref}/zip"} {
		if !strings.Contains(logs, route) {
			t.Errorf("no request log for %s", route)
		}
	}
	secrets := []string{fake.PublicKey, fake.PasswordKey, fake.PublicSlug, tok, password, e.Immich.URL(),
		fake.Token(fake.PasswordShareID, password), cookie[1]}
	for _, secret := range secrets {
		if strings.Contains(logs, secret) {
			t.Errorf("logs contain %q", secret)
		}
	}
}

func TestStaticAssets(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	if page := string(v.Get(t, "/share/"+fake.PublicKey).Body); !strings.Contains(page, `src="`+harness.AssetScript+`"`) {
		t.Fatalf("page doesn't load the islands: %s", page)
	}
	r := v.Get(t, harness.AssetScript)
	etag := r.Header.Get("ETag")
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || etag == "" ||
		!strings.HasPrefix(r.Header.Get("Content-Type"), "text/javascript") || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("asset: %d %v", r.StatusCode, r.Header)
	}
	if r := v.Do(t, "HEAD", harness.AssetScript, nil); r.StatusCode != 200 || len(r.Body) != 0 || r.Header.Get("ETag") != etag {
		t.Errorf("HEAD: %d %d bytes", r.StatusCode, len(r.Body))
	}
	if r := v.Get(t, harness.AssetScript, "If-None-Match", etag); r.StatusCode != 304 || len(r.Body) != 0 {
		t.Errorf("If-None-Match: %d", r.StatusCode)
	}
	for _, path := range []string{"/.vite/manifest.json", "/assets/nope.js", "/assets/", "/favicon.ico"} {
		if r := v.Get(t, path); r.StatusCode != 404 || !strings.Contains(string(r.Body), "Page not found") {
			t.Errorf("%s: %d", path, r.StatusCode)
		}
	}
}

func TestRequestLimits(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{Config: func(c *config.Config) { c.TrustedProxies = trustedLoopback() }})
	v := e.Visitor()
	v.IP = "198.51.100.7"
	for _, c := range []struct {
		path  string
		burst int
	}{
		{"/api/shares/key/" + fake.PublicKey + "/assets/nope", 120},
		{"/media/key/" + fake.PublicKey + "/nope/thumb", 400},
	} {
		for i := range c.burst {
			if r := v.Get(t, c.path); r.StatusCode != 404 {
				t.Fatalf("%s request %d: %d", c.path, i, r.StatusCode)
			}
		}
		r := v.Get(t, c.path)
		if r.StatusCode != 429 || r.Code() != "rate_limited" || r.Header.Get("Retry-After") == "" {
			t.Fatalf("%s over the burst: %d %s", c.path, r.StatusCode, r.Body)
		}
		other := e.Visitor()
		other.IP = "198.51.100.8"
		if r := other.Get(t, c.path); r.StatusCode != 404 {
			t.Fatalf("%s from another IP: %d", c.path, r.StatusCode)
		}
	}
	e.Clock.Advance(time.Second)
	if r := v.Get(t, "/media/key/"+fake.PublicKey+"/nope/thumb"); r.StatusCode != 404 {
		t.Fatalf("after a second: %d", r.StatusCode)
	}
}
