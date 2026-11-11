package share_test

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/share"
)

const shareID = "33333333-3333-4333-8333-333333333333"

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func TestSessions(t *testing.T) {
	t.Parallel()
	k, err := share.NewSealer(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s := share.Session{ShareID: shareID, Token: "tok", Exp: t0.Add(time.Hour)}
	v, err := k.Seal(s)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := k.Open(v, t0); err != nil || got.Token != "tok" {
		t.Fatalf("open: %+v %v", got, err)
	}
	if _, err := k.Open(v, t0.Add(time.Hour)); err == nil {
		t.Error("expired cookie opened")
	}
	if _, err := k.Open(v[:len(v)-2]+"AA", t0); err == nil {
		t.Error("tampered cookie opened")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Add("Cookie", share.CookieName(shareID)+"="+v)
	r.Header.Add("Cookie", share.CookieName("other")+"="+v) // wrong name for its content
	r.Header.Add("Cookie", "unrelated=1")
	if got := k.Sessions(r, t0); len(got) != 1 {
		t.Fatalf("sessions: %+v", got)
	}
}
