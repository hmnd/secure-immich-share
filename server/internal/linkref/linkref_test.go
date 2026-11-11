package linkref

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/hmnd/secure-immich-share/server/internal/config"
)

func secret(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

var testKey = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, immichKeyBytes))

func ring(t *testing.T, keys ...config.LinkKey) *Keyring {
	t.Helper()
	k, err := NewKeyring(keys)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestTokenTamper(t *testing.T) {
	k := ring(t, config.LinkKey{ID: 1, Secret: secret(1), Active: true})
	tok, _ := k.Mint(Target{TargetKey, testKey})
	raw, _ := base64.RawURLEncoding.DecodeString(tok)
	for i := range raw {
		mut := bytes.Clone(raw)
		mut[i] ^= 0x01
		if _, err := k.Open(base64.RawURLEncoding.EncodeToString(mut)); !errors.Is(err, ErrNotFound) {
			t.Errorf("byte %d flipped: %v", i, err)
		}
	}
	for _, bad := range []string{"", "!!!", tok + "A", tok[:10], strings.Repeat("A", 600)} {
		if _, err := k.Open(bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("Open(%q): %v", bad, err)
		}
	}
}

// TestKeyStates: active mints and opens, retired only opens,
// removed opens nothing.
func TestKeyStates(t *testing.T) {
	old := config.LinkKey{ID: 1, Secret: secret(1), Active: true}
	before := ring(t, old)
	oldTok, _ := before.Mint(Target{TargetSlug, "s"})

	// Rotation step 1: new key active, old retired.
	rotated := ring(t, config.LinkKey{ID: 1, Secret: secret(1)}, config.LinkKey{ID: 2, Secret: secret(2), Active: true})
	if _, err := rotated.Open(oldTok); err != nil {
		t.Errorf("retired key no longer opens: %v", err)
	}
	newTok, _ := rotated.Mint(Target{TargetSlug, "s"})
	raw, _ := base64.RawURLEncoding.DecodeString(newTok)
	if raw[1] != 2 {
		t.Errorf("minted with key %d, want the active key 2", raw[1])
	}

	// Rotation step 2: old key removed.
	after := ring(t, config.LinkKey{ID: 2, Secret: secret(2), Active: true})
	if _, err := after.Open(oldTok); !errors.Is(err, ErrNotFound) {
		t.Errorf("removed key still opens: %v", err)
	}
	if _, err := after.Open(newTok); err != nil {
		t.Errorf("active key: %v", err)
	}

	// Same id, different secret: not found.
	other := ring(t, config.LinkKey{ID: 2, Secret: secret(9), Active: true})
	if _, err := other.Open(newTok); !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong secret opens: %v", err)
	}

	if _, err := NewKeyring([]config.LinkKey{{ID: 1, Secret: secret(1)}}); err == nil {
		t.Error("keyring without an active key")
	}
	if _, err := NewKeyring([]config.LinkKey{{ID: 1, Secret: secret(1), Active: true}, {ID: 2, Secret: secret(2), Active: true}}); err == nil {
		t.Error("keyring with two active keys")
	}
}

func TestParseShareInput(t *testing.T) {
	for in, want := range map[string]Target{
		"https://photos.lan/share/" + testKey: {TargetKey, testKey},
		testKey:                               {TargetKey, testKey},
		"https://photos.lan/s/summer/":        {TargetSlug, "summer"},
		"summer":                              {TargetSlug, "summer"},
	} {
		got, err := ParseShareInput(in)
		if err != nil || got != want {
			t.Errorf("%q: got %v %v", in, got, err)
		}
	}
}
