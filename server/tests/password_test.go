package tests

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/config"
	"github.com/hmnd/secure-immich-share/server/internal/linkref"
	"github.com/hmnd/secure-immich-share/server/internal/share"
	fake "github.com/hmnd/secure-immich-share/server/tests/fakes/immich"
	"github.com/hmnd/secure-immich-share/server/tests/harness"
)

// unlock submits the password form of a link's page.
func unlock(t *testing.T, v *harness.Visitor, kind, ref, password string) harness.Response {
	t.Helper()
	return v.Form(t, pagePath(kind, ref), url.Values{"password": {password}})
}

func TestPasswordShare(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	r, res := getShare(t, v, "key", fake.PasswordKey)
	if r.StatusCode != 200 || res.Status != "locked" || res.Share != nil {
		t.Fatalf("locked: %d %s", r.StatusCode, r.Body)
	}
	if r.Header.Get("Cache-Control") != "no-store" {
		t.Error("locked response cacheable")
	}
	thumb := "/media/key/" + fake.PasswordKey + "/" + fake.PhotoID + "/thumb"
	if r := v.Get(t, thumb); r.StatusCode != 404 {
		t.Fatalf("locked media: %d", r.StatusCode)
	}
	if st := prepare(t, v, "key", fake.PasswordKey, fake.VideoID, "video"); st.State != "http_404" {
		t.Fatalf("locked prepare: %+v", st)
	}
	if r := unlock(t, v, "key", fake.PasswordKey, "wrong"); r.StatusCode != 401 || !strings.Contains(string(r.Body), "Wrong password") {
		t.Fatalf("wrong password: %d %s", r.StatusCode, r.Body)
	}
	r = unlock(t, v, "key", fake.PasswordKey, fake.SharePassword)
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("unlock: %d %s", r.StatusCode, r.Body)
	}
	var ck *http.Cookie
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, share.CookiePrefix) {
			ck = c
		}
	}
	if ck == nil || !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteLaxMode || ck.MaxAge != 86400 || ck.Path != "/" {
		t.Fatalf("cookie: %+v", ck)
	}
	if strings.Contains(ck.Value, fake.Token(fake.PasswordShareID, fake.SharePassword)) {
		t.Fatal("cookie holds the Immich token in clear")
	}
	hdr := harness.UnlockCookie(t, r)
	if _, res := getShare(t, v, "key", fake.PasswordKey, hdr...); res.Status != "ready" {
		t.Fatalf("unlocked: %+v", res)
	}
	m := v.Get(t, thumb, hdr...)
	if m.StatusCode != 200 || m.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("unlocked media: %d %s", m.StatusCode, m.Header.Get("Cache-Control"))
	}
	// Unlocking via the key unlocks the same share through a token link.
	tok := mint(t, e, linkref.Target{Kind: linkref.TargetKey, Value: fake.PasswordKey})
	if _, res := getShare(t, v, "token", tok, hdr...); res.Status != "ready" {
		t.Fatalf("via token: %+v", res)
	}
	// Another visitor without the cookie stays locked.
	if _, res := getShare(t, e.Visitor(), "key", fake.PasswordKey); res.Status != "locked" {
		t.Fatal("cookie-less visitor got the listing")
	}
}

func TestPasswordChange(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	a, b := e.Visitor(), e.Visitor()
	old := harness.UnlockCookie(t, unlock(t, a, "key", fake.PasswordKey, fake.SharePassword))
	e.Immich.UpdateShare(fake.PasswordShareID, func(s *fake.Share) { s.Password = "new-password" })
	fresh := harness.UnlockCookie(t, unlock(t, b, "key", fake.PasswordKey, "new-password"))
	if _, res := getShare(t, b, "key", fake.PasswordKey, fresh...); res.Status != "ready" {
		t.Fatalf("new password: %+v", res)
	}
	r, res := getShare(t, a, "key", fake.PasswordKey, old...)
	if res.Status != "locked" {
		t.Fatalf("old cookie after the change: %s", r.Body)
	}
	cleared := false
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, share.CookiePrefix) && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("stale cookie not cleared")
	}
	if _, res := getShare(t, b, "key", fake.PasswordKey, fresh...); res.Status != "ready" {
		t.Fatalf("new cookie after the stale check: %+v", res)
	}
}

func TestUnlockThrottle(t *testing.T) {
	t.Parallel()
	start := func(t *testing.T) (*harness.Env, *harness.Visitor) {
		e := harness.Start(t, harness.Options{Config: func(c *config.Config) { c.TrustedProxies = trustedLoopback() }})
		v := e.Visitor()
		v.IP = "198.51.100.66"
		return e, v
	}
	// fail5 makes five wrong guesses and returns the Retry-After of the
	// attempt after them.
	fail5 := func(t *testing.T, v *harness.Visitor) string {
		t.Helper()
		for i := range 5 {
			if r := unlock(t, v, "key", fake.PasswordKey, "guess"); r.StatusCode != 401 {
				t.Fatalf("guess %d: %d", i, r.StatusCode)
			}
		}
		r := unlock(t, v, "key", fake.PasswordKey, fake.SharePassword)
		if r.StatusCode != 429 || !strings.Contains(string(r.Body), "Too many attempts") {
			t.Fatalf("after 5 failures: %d %s", r.StatusCode, r.Body)
		}
		return r.Header.Get("Retry-After")
	}
	wait := func(e *harness.Env, retryAfter string) {
		s, _ := strconv.Atoi(retryAfter)
		e.Clock.Advance(time.Duration(s) * time.Second)
	}

	t.Run("lockouts escalate", func(t *testing.T) {
		t.Parallel()
		e, attacker := start(t)
		for _, want := range []string{"60", "120", "300", "900", "900"} {
			got := fail5(t, attacker)
			if got != want {
				t.Fatalf("lockout %s, want %s", got, want)
			}
			wait(e, got)
		}
	})

	t.Run("lockouts hold across link kinds and spare others", func(t *testing.T) {
		t.Parallel()
		e, attacker := start(t)
		fail5(t, attacker)
		logins := e.Immich.Count("POST", "/api/shared-links/login")
		tok := mint(t, e, linkref.Target{Kind: linkref.TargetKey, Value: fake.PasswordKey})
		if r := unlock(t, attacker, "token", tok, fake.SharePassword); r.StatusCode != 429 {
			t.Fatalf("via token: %d", r.StatusCode)
		}
		if e.Immich.Count("POST", "/api/shared-links/login") != logins {
			t.Fatal("throttled attempts reached Immich")
		}
		if n := e.Immich.Count("GET", "/api/timeline"); n != 0 {
			t.Fatalf("wrong passwords loaded the listing %d times", n)
		}
		friend := e.Visitor()
		friend.IP = "203.0.113.5"
		if r := unlock(t, friend, "key", fake.PasswordKey, fake.SharePassword); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("other IP: %d", r.StatusCode)
		}
		e.Clock.Advance(time.Minute)
		if r := unlock(t, attacker, "key", fake.PasswordKey, fake.SharePassword); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("after lockout: %d %s", r.StatusCode, r.Body)
		}
	})

	t.Run("a correct password resets the level", func(t *testing.T) {
		t.Parallel()
		e, v := start(t)
		wait(e, fail5(t, v))
		if r := unlock(t, v, "key", fake.PasswordKey, fake.SharePassword); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("unlock: %d", r.StatusCode)
		}
		e.Clock.Advance(time.Minute)
		if got := fail5(t, v); got != "60" {
			t.Fatalf("lockout after a reset: %s", got)
		}
	})

	t.Run("failures spread over more than 10 minutes don't count", func(t *testing.T) {
		t.Parallel()
		e, v := start(t)
		for range 5 {
			unlock(t, v, "key", fake.PasswordKey, "guess")
			e.Clock.Advance(3 * time.Minute)
		}
		if r := unlock(t, v, "key", fake.PasswordKey, fake.SharePassword); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("unlock: %d", r.StatusCode)
		}
	})

	t.Run("an hour without failures drops one level", func(t *testing.T) {
		t.Parallel()
		e, v := start(t)
		wait(e, fail5(t, v))
		wait(e, fail5(t, v)) // L2
		e.Clock.Advance(time.Hour)
		if got := fail5(t, v); got != "120" {
			t.Fatalf("lockout after an hour: %s, want L2's 120", got)
		}
	})

	t.Run("five attempts a minute, even with the right password", func(t *testing.T) {
		t.Parallel()
		e, v := start(t)
		for i := range 5 {
			if r := unlock(t, v, "key", fake.PasswordKey, fake.SharePassword); r.StatusCode != http.StatusSeeOther {
				t.Fatalf("attempt %d: %d", i, r.StatusCode)
			}
		}
		r := unlock(t, v, "key", fake.PasswordKey, fake.SharePassword)
		if r.StatusCode != 429 || r.Header.Get("Retry-After") != "60" {
			t.Fatalf("6th attempt: %d %q", r.StatusCode, r.Header.Get("Retry-After"))
		}
		e.Clock.Advance(time.Minute)
		if r := unlock(t, v, "key", fake.PasswordKey, fake.SharePassword); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("after a minute: %d", r.StatusCode)
		}
	})

	t.Run("30 attempts a minute per share without locking anyone out", func(t *testing.T) {
		t.Parallel()
		e, _ := start(t)
		visitor := func(i int) *harness.Visitor {
			v := e.Visitor()
			v.IP = fmt.Sprintf("198.51.100.%d", i)
			return v
		}
		for i := range 30 {
			if r := unlock(t, visitor(i), "key", fake.PasswordKey, "guess"); r.StatusCode != 401 {
				t.Fatalf("attempt %d: %d", i, r.StatusCode)
			}
		}
		late := visitor(200)
		if r := unlock(t, late, "key", fake.PasswordKey, fake.SharePassword); r.StatusCode != 429 {
			t.Fatalf("31st attempt: %d", r.StatusCode)
		}
		if r := unlock(t, late, "key", fake.NoDownloadKey, "x"); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("another share: %d", r.StatusCode)
		}
		e.Clock.Advance(2 * time.Second)
		if r := unlock(t, late, "key", fake.PasswordKey, fake.SharePassword); r.StatusCode != http.StatusSeeOther {
			t.Fatalf("after the bucket refills: %d", r.StatusCode)
		}
	})
}

func TestUnlockThrottleIPv6(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{Config: func(c *config.Config) {
		c.TrustedProxies = trustedLoopback()
	}})
	attacker := e.Visitor()
	attacker.IP = "2001:db8:1:2::1"
	for range 5 {
		unlock(t, attacker, "key", fake.PasswordKey, "guess")
	}
	// The rest of the attacker's /64 is locked out too; other networks aren't.
	sibling := e.Visitor()
	sibling.IP = "2001:db8:1:2:ffff::7"
	if r := unlock(t, sibling, "key", fake.PasswordKey, fake.SharePassword); r.StatusCode != 429 {
		t.Fatalf("same /64: %d", r.StatusCode)
	}
	neighbour := e.Visitor()
	neighbour.IP = "2001:db8:1:3::1"
	if r := unlock(t, neighbour, "key", fake.PasswordKey, fake.SharePassword); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("other /64: %d", r.StatusCode)
	}
}
