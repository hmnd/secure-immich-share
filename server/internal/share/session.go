package share

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/linkref"
)

// CookiePrefix starts every unlock cookie name; the rest is hash8 of the
// Immich share id, so unlocking through any link kind unlocks the share
// for all of them.
const CookiePrefix = "sis_u_"

// SessionTTL is how long an unlock lasts.
const SessionTTL = 24 * time.Hour

// maxSessions bounds how many unlock cookies a request may present.
const maxSessions = 16

// Session is the content of an unlock cookie.
type Session struct {
	ShareID string    `json:"s"`
	Token   string    `json:"t"`
	Exp     time.Time `json:"e"`
}

// CookieName is the cookie a share's session lives in.
func CookieName(shareID string) string { return CookiePrefix + linkref.Hash8(shareID) }

// Sealer encrypts and decrypts unlock cookies with COOKIE_KEY.
type Sealer struct {
	aead cipher.AEAD
}

func NewSealer(key []byte) (*Sealer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

const cookieAAD = "sis-unlock"

func (k *Sealer) Seal(s Session) (string, error) {
	plain, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append(nonce, k.aead.Seal(nil, nonce, plain, []byte(cookieAAD))...)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts a cookie value; expired or tampered cookies fail.
func (k *Sealer) Open(v string, now time.Time) (Session, error) {
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil || len(raw) < k.aead.NonceSize()+k.aead.Overhead() {
		return Session{}, errors.New("share: bad cookie")
	}
	plain, err := k.aead.Open(nil, raw[:k.aead.NonceSize()], raw[k.aead.NonceSize():], []byte(cookieAAD))
	if err != nil {
		return Session{}, errors.New("share: bad cookie")
	}
	var s Session
	if err := json.Unmarshal(plain, &s); err != nil || s.ShareID == "" || s.Token == "" {
		return Session{}, errors.New("share: bad cookie")
	}
	if !now.Before(s.Exp) {
		return Session{}, errors.New("share: expired cookie")
	}
	return s, nil
}

// Sessions decrypts the request's unlock cookies (at most maxSessions);
// cookies that don't open are ignored.
func (k *Sealer) Sessions(r *http.Request, now time.Time) []Session {
	var out []Session
	for _, c := range r.Cookies() {
		if !strings.HasPrefix(c.Name, CookiePrefix) {
			continue
		}
		if len(out) == maxSessions {
			break
		}
		s, err := k.Open(c.Value, now)
		if err != nil || CookieName(s.ShareID) != c.Name {
			continue
		}
		out = append(out, s)
	}
	return out
}
