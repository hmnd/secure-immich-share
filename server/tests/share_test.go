package tests

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/clock"
	"github.com/hmnd/secure-immich-share/server/internal/config"
	"github.com/hmnd/secure-immich-share/server/internal/linkref"
	fake "github.com/hmnd/secure-immich-share/server/tests/fakes/immich"
	"github.com/hmnd/secure-immich-share/server/tests/harness"
)

func itemsByID(sh *shareView) map[string]shareItem {
	out := map[string]shareItem{}
	for _, it := range sh.Items {
		out[it.ID] = it
	}
	return out
}

func TestAlbumListing(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	// 23:30 local on 1 July is 2 July in UTC.
	e.Immich.UpdateAsset(fake.PhotoID, func(a *fake.Asset) { a.LocalDateTime = time.Date(2024, 7, 1, 23, 30, 0, 0, time.UTC) })
	e.Immich.UpdateAsset(fake.HEICID, func(a *fake.Asset) { a.Thumbhash = "1QcSHQRnh493V4dIh4eXh1h4kJUI" })
	r, res := getShare(t, e.Visitor(), "key", fake.PublicKey)
	if res.Share == nil {
		t.Fatalf("share: %d %s", r.StatusCode, r.Body)
	}
	sh := res.Share
	if sh.Kind != "album" || sh.CoverID == nil || *sh.CoverID != fake.PhotoID {
		t.Fatalf("share: %+v", sh)
	}
	items := itemsByID(sh)
	for id, want := range map[string]struct{ kind, date string }{
		fake.PhotoID: {"image", "2024-07-01T23:30:00-07:00"},
		fake.VideoID: {"video", "2024-07-03T18:00:00-07:00"},
		fake.LiveID:  {"live", "2024-07-04T18:00:00-07:00"},
	} {
		it := items[id]
		if it.Kind != want.kind || it.TakenAt == nil || *it.TakenAt != want.date {
			t.Errorf("%s: %+v", id, it)
		}
	}
	if r := items[fake.PhotoID].Ratio; r < 1.33 || r > 1.34 {
		t.Errorf("ratio %v", r)
	}
	page := string(r.Body)
	if th := items[fake.HEICID].Thumbhash; th == nil || *th != "1QcSHQRnh493V4dIh4eXh1h4kJUI" ||
		!strings.Contains(page, `data-thumbhash="1QcSHQRnh493V4dIh4eXh1h4kJUI"`) {
		t.Errorf("thumbhash: %v", th)
	}
}

func TestIndividualListing(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	r, res := getShare(t, e.Visitor(), "key", fake.IndividualKey)
	if res.Share == nil {
		t.Fatalf("share: %d %s", r.StatusCode, r.Body)
	}
	sh := res.Share
	if sh.Kind != "individual" || sh.Title == nil || *sh.Title != "Our summer" || len(sh.Items) != 2 ||
		sh.Items[0].ID != fake.PhotoID || sh.Items[1].ID != fake.VideoID || *sh.CoverID != fake.PhotoID {
		t.Fatalf("share: %+v", sh)
	}
	if it := sh.Items[1]; it.Kind != "video" || it.TakenAt == nil || *it.TakenAt != "2024-07-03T18:00:00-07:00" {
		t.Errorf("video: %+v", it)
	}
	if !strings.Contains(string(r.Body), "<h1>Our summer</h1>") {
		t.Error("title missing from the page")
	}
	for _, s := range identifying {
		if strings.Contains(string(r.Body), s) {
			t.Errorf("page contains %q", s)
		}
	}
}

func TestShareExpiry(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	exp := e.Clock.Now().Add(time.Hour)
	e.Immich.UpdateShare(fake.PublicShareID, func(s *fake.Share) { s.ExpiresAt = &exp })
	// Immich's clock stays before the expiry: the app enforces it on its own.
	e.Immich.SetClock(clock.NewFake(e.Clock.Now()))
	v := e.Visitor()
	if r, _ := getShare(t, v, "key", fake.PublicKey); r.StatusCode != 200 {
		t.Fatalf("before expiry: %d", r.StatusCode)
	}
	e.Clock.Set(exp)
	if r, _ := getShare(t, v, "key", fake.PublicKey); r.StatusCode != 404 {
		t.Fatalf("after expiry: %d", r.StatusCode)
	}
}

func TestUnlockExpiry(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	cookie := harness.UnlockCookie(t, unlock(t, v, "key", fake.PasswordKey, fake.SharePassword))
	e.Clock.Advance(24*time.Hour - time.Second)
	if _, res := getShare(t, v, "key", fake.PasswordKey, cookie...); res.Status != "ready" {
		t.Fatalf("within 24h: %+v", res)
	}
	e.Clock.Advance(time.Second)
	if _, res := getShare(t, v, "key", fake.PasswordKey, cookie...); res.Status != "locked" {
		t.Fatalf("after 24h: %+v", res)
	}
}

func TestPasswordLinkKinds(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	tok := mint(t, e, linkref.Target{Kind: linkref.TargetSlug, Value: fake.PasswordSlug})
	for _, c := range []struct{ kind, ref string }{{"slug", fake.PasswordSlug}, {"token", tok}} {
		if _, res := getShare(t, v, c.kind, c.ref); res.Status != "locked" {
			t.Fatalf("%s: %+v", c.kind, res)
		}
	}
	// ?photo= without the cookie shows the form, not the photo.
	page := string(v.Get(t, pagePath("slug", fake.PasswordSlug)+"?photo="+fake.PhotoID).Body)
	if !strings.Contains(page, `<form method="post">`) || strings.Contains(page, `class="viewer"`) || strings.Contains(page, fake.PhotoID) {
		t.Fatalf("locked ?photo=: %s", page)
	}
	r := unlock(t, v, "token", tok, fake.SharePassword)
	if r.StatusCode != 303 || r.Header.Get("Location") != pagePath("token", tok) {
		t.Fatalf("unlock via token: %d %q", r.StatusCode, r.Header.Get("Location"))
	}
	cookie := harness.UnlockCookie(t, r)
	for _, c := range []struct{ kind, ref string }{{"slug", fake.PasswordSlug}, {"token", tok}, {"key", fake.PasswordKey}} {
		if _, res := getShare(t, v, c.kind, c.ref, cookie...); res.Status != "ready" {
			t.Errorf("%s after unlock: %+v", c.kind, res)
		}
	}
}

func TestAssetDetailAccess(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{Config: func(c *config.Config) { c.ShowDescriptions = true }})
	v := e.Visitor()
	getShare(t, v, "key", fake.PublicKey)
	e.Immich.ResetRequests()
	base := "/api/shares/key/" + fake.PublicKey + "/assets/"
	for _, id := range []string{fake.TrashedID, fake.LockedID, fake.MotionID, fake.OutsideID, "nope"} {
		if r := v.Get(t, base+url.PathEscape(id)); r.StatusCode != 404 {
			t.Errorf("%s: %d", id, r.StatusCode)
		}
	}
	if n := e.Immich.Count("GET", "/api/assets/"); n != 0 {
		t.Fatalf("details outside the share reached Immich %d times", n)
	}
	// The motion part of a live photo is reachable through its still.
	if st := prepare(t, v, "key", fake.PublicKey, fake.LiveID, "video"); st.State != "ready" {
		t.Fatalf("live video: %+v", st)
	}
	if n := e.Immich.Count("GET", "/api/assets/"+fake.MotionID); n == 0 {
		t.Fatal("motion part never fetched")
	}
}

// TestImmichAddressing: share keys and slugs go to Immich in headers only,
// never in query strings (Immich's access logs keep those).
func TestImmichAddressing(t *testing.T) {
	t.Parallel()
	e := harness.Start(t, harness.Options{})
	v := e.Visitor()
	getShare(t, v, "key", fake.PublicKey)
	getShare(t, v, "slug", fake.PublicSlug)
	unlock(t, v, "key", fake.PasswordKey, fake.SharePassword)
	v.Get(t, "/media/key/"+fake.PublicKey+"/"+fake.PhotoID+"/thumb")
	byKey, bySlug := 0, 0
	for _, r := range e.Immich.Requests() {
		for _, secret := range []string{fake.PublicKey, fake.PasswordKey, fake.PublicSlug} {
			if strings.Contains(r.Query.Encode(), url.QueryEscape(secret)) {
				t.Errorf("%s %s carries a share address in its query", r.Method, r.Path)
			}
		}
		if r.Header.Get("x-immich-share-key") != "" {
			byKey++
		}
		if r.Header.Get("x-immich-share-slug") == fake.PublicSlug {
			bySlug++
		}
	}
	if byKey == 0 || bySlug == 0 {
		t.Fatalf("no requests addressed by header: key %d, slug %d", byKey, bySlug)
	}
}
