// Package linkref parses the public link forms into the Immich share they
// address: Immich keys (/share/{key}), slugs (/s/{slug}) and encrypted
// tokens (/t/{token}). Anything malformed, undecryptable or of a
// disabled kind is ErrNotFound, decided before any call to Immich.
package linkref

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hmnd/secure-immich-share/server/internal/config"
)

// Kind is a public link kind.
type Kind string

const (
	KindKey   Kind = "key"
	KindSlug  Kind = "slug"
	KindToken Kind = "token"
)

// TargetKind is how Immich is addressed.
type TargetKind string

const (
	TargetKey  TargetKind = "key"
	TargetSlug TargetKind = "slug"
)

// Target is an Immich share address: a base64url key or a slug.
type Target struct {
	Kind  TargetKind
	Value string
}

// ID identifies the target for caches and limits; it never contains the
// key itself.
func (t Target) ID() string {
	return hashHex(string(t.Kind)+"\x00"+t.Value, 16)
}

var ErrNotFound = errors.New("linkref: not found")

// immichKeyBytes is the length of an Immich share key (randomBytes(50)).
const immichKeyBytes = 50

const maxSlugBytes = 128

// Parser turns {kind, ref} path parameters into targets.
type Parser struct {
	KeyLinks  bool
	SlugLinks bool
	Ring      *Keyring // nil disables token links
}

func NewParser(cfg config.Config) (*Parser, error) {
	p := &Parser{KeyLinks: cfg.ImmichKeyLinks, SlugLinks: cfg.ImmichSlugLinks}
	if cfg.TokenLinks() {
		ring, err := NewKeyring(cfg.LinkKeys)
		if err != nil {
			return nil, err
		}
		p.Ring = ring
	}
	return p, nil
}

func (p *Parser) enabled(k Kind) bool {
	switch k {
	case KindKey:
		return p.KeyLinks
	case KindSlug:
		return p.SlugLinks
	case KindToken:
		return p.Ring != nil
	}
	return false
}

// Parse validates ref for kind and returns the Immich target.
func (p *Parser) Parse(kind Kind, ref string) (Target, error) {
	if !p.enabled(kind) {
		return Target{}, ErrNotFound
	}
	switch kind {
	case KindKey:
		if !ValidKey(ref) {
			return Target{}, ErrNotFound
		}
		return Target{Kind: TargetKey, Value: ref}, nil
	case KindSlug:
		if !ValidSlug(ref) {
			return Target{}, ErrNotFound
		}
		return Target{Kind: TargetSlug, Value: ref}, nil
	case KindToken:
		return p.Ring.Open(ref)
	}
	return Target{}, ErrNotFound
}

// ValidKey reports whether s is an unpadded base64url Immich share key.
func ValidKey(s string) bool {
	if len(s) != base64.RawURLEncoding.EncodedLen(immichKeyBytes) {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(b) == immichKeyBytes
}

// ValidSlug reports whether s is a slug Immich could have stored: 1-128
// bytes of printable UTF-8 without '/'.
func ValidSlug(s string) bool {
	if s == "" || len(s) > maxSlugBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '/' || r == '\\' || unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// ParseShareInput accepts what an owner pastes into `mint`: an Immich share
// URL (…/share/{key} or …/s/{slug}), a bare key or a bare slug.
func ParseShareInput(in string) (Target, error) {
	in = strings.TrimSpace(in)
	if i := strings.Index(in, "/share/"); i >= 0 {
		key := strings.Trim(in[i+len("/share/"):], "/")
		if ValidKey(key) {
			return Target{Kind: TargetKey, Value: key}, nil
		}
		return Target{}, fmt.Errorf("not an Immich share key: %q", key)
	}
	if i := strings.Index(in, "/s/"); i >= 0 {
		slug := strings.Trim(in[i+len("/s/"):], "/")
		if ValidSlug(slug) {
			return Target{Kind: TargetSlug, Value: slug}, nil
		}
		return Target{}, fmt.Errorf("not an Immich slug: %q", slug)
	}
	if ValidKey(in) {
		return Target{Kind: TargetKey, Value: in}, nil
	}
	if ValidSlug(in) {
		return Target{Kind: TargetSlug, Value: in}, nil
	}
	return Target{}, fmt.Errorf("not an Immich share URL, key or slug: %q", in)
}

// Hash8 is a short log-safe hash of an identifier.
func Hash8(s string) string { return hashHex(s, 4) }

// Hash is the hex SHA-256 of a secret, for comparing and keying it without
// keeping it.
func Hash(s string) string { return hashHex(s, sha256.Size) }

// hashHex is the hex of the first n bytes of the SHA-256 of s.
func hashHex(s string, n int) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:n])
}

// Token layout: version(1) || keyId(1) || nonce(12) || AES-256-GCM(payload).
// payload = kind(1) || key bytes or slug bytes. The header is
// authenticated as additional data together with "sis-link".
const (
	tokenVersion = 1
	payloadKey   = 'k'
	payloadSlug  = 's'
	tokenAAD     = "sis-link"
	maxTokenLen  = 512
)

// Keyring holds the LINK_KEYS: the active key mints, active and
// retired keys open.
type Keyring struct {
	aeads  map[byte]cipher.AEAD
	active byte
}

// NewKeyring builds a keyring; exactly one key must be active.
func NewKeyring(keys []config.LinkKey) (*Keyring, error) {
	k := &Keyring{aeads: map[byte]cipher.AEAD{}}
	actives := 0
	for _, key := range keys {
		block, err := aes.NewCipher(key.Secret)
		if err != nil {
			return nil, fmt.Errorf("linkref: key %d: %w", key.ID, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("linkref: key %d: %w", key.ID, err)
		}
		k.aeads[key.ID] = aead
		if key.Active {
			k.active = key.ID
			actives++
		}
	}
	if actives != 1 {
		return nil, fmt.Errorf("linkref: want exactly one active key, have %d", actives)
	}
	return k, nil
}

// Mint encrypts t with the active key.
func (k *Keyring) Mint(t Target) (string, error) {
	var payload []byte
	switch t.Kind {
	case TargetKey:
		raw, err := base64.RawURLEncoding.Strict().DecodeString(t.Value)
		if err != nil || len(raw) != immichKeyBytes {
			return "", errors.New("linkref: invalid Immich key")
		}
		payload = append([]byte{payloadKey}, raw...)
	case TargetSlug:
		if !ValidSlug(t.Value) {
			return "", errors.New("linkref: invalid slug")
		}
		payload = append([]byte{payloadSlug}, t.Value...)
	default:
		return "", errors.New("linkref: unknown target kind")
	}
	aead := k.aeads[k.active]
	header := []byte{tokenVersion, k.active}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append(append(header, nonce...), aead.Seal(nil, nonce, payload, aad(header))...)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts a token. Every failure is ErrNotFound.
func (k *Keyring) Open(token string) (Target, error) {
	if len(token) == 0 || len(token) > maxTokenLen {
		return Target{}, ErrNotFound
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) < 2 || raw[0] != tokenVersion {
		return Target{}, ErrNotFound
	}
	aead, ok := k.aeads[raw[1]]
	if !ok || len(raw) < 2+aead.NonceSize()+aead.Overhead()+1 {
		return Target{}, ErrNotFound
	}
	nonce := raw[2 : 2+aead.NonceSize()]
	payload, err := aead.Open(nil, nonce, raw[2+aead.NonceSize():], aad(raw[:2]))
	if err != nil || len(payload) < 2 {
		return Target{}, ErrNotFound
	}
	switch payload[0] {
	case payloadKey:
		if len(payload)-1 != immichKeyBytes {
			return Target{}, ErrNotFound
		}
		return Target{Kind: TargetKey, Value: base64.RawURLEncoding.EncodeToString(payload[1:])}, nil
	case payloadSlug:
		slug := string(payload[1:])
		if !ValidSlug(slug) {
			return Target{}, ErrNotFound
		}
		return Target{Kind: TargetSlug, Value: slug}, nil
	}
	return Target{}, ErrNotFound
}

func aad(header []byte) []byte {
	return append([]byte(tokenAAD), header...)
}
