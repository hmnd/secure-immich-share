// Package immich is the JSON client for the Immich shared-link API. It only
// ever authenticates with a share key or slug (plus the password-share
// token); it never holds an API key. Raw media bytes are fetched by
// internal/media/internal/fetch, not here.
//
// Types here are Immich's and must never reach a visitor: internal/share
// maps them onto allowlisted views.
package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/linkref"
)

var (
	// ErrGone: the share doesn't exist, expired or was revoked, or Immich
	// refused access (401/403/404 other than a password prompt).
	ErrGone = errors.New("immich: share gone")
	// ErrPasswordRequired: a password share without a valid token.
	ErrPasswordRequired = errors.New("immich: password required")
	// ErrInvalidPassword: login with a wrong password.
	ErrInvalidPassword = errors.New("immich: invalid password")
	// ErrNotPasswordProtected: login on a share without a password.
	ErrNotPasswordProtected = errors.New("immich: share has no password")
	// ErrUpstream: Immich unreachable or failing.
	ErrUpstream = errors.New("immich: upstream error")
)

// linkCookie is the password-share token cookie.
const linkCookie = "immich_shared_link_token"

// Auth addresses a share, with the token for unlocked password shares.
type Auth struct {
	Target linkref.Target
	Token  string
}

// Apply sets the share headers (never query parameters, so keys stay out
// of Immich's access logs) and the token cookie.
func (a Auth) Apply(h http.Header) {
	switch a.Target.Kind {
	case linkref.TargetKey:
		h.Set("x-immich-share-key", a.Target.Value)
	case linkref.TargetSlug:
		h.Set("x-immich-share-slug", a.Target.Value)
	}
	if a.Token != "" {
		// Express decodes cookie values with decodeURIComponent.
		h.Set("Cookie", linkCookie+"="+url.QueryEscape(a.Token))
	}
}

// Client talks to one Immich server.
type Client struct {
	base string // …/api
	http *http.Client
}

// New returns a client for baseURL (without /api).
func New(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{base: strings.TrimRight(baseURL, "/") + "/api", http: hc}
}

// SharedLink is the subset of SharedLinkResponseDto this service reads.
type SharedLink struct {
	ID            string     `json:"id"`
	Type          string     `json:"type"` // ALBUM | INDIVIDUAL
	Description   *string    `json:"description"`
	ExpiresAt     *time.Time `json:"expiresAt"`
	AllowDownload bool       `json:"allowDownload"`
	ShowMetadata  bool       `json:"showMetadata"`
	Password      *string    `json:"password"`
	Assets        []Asset    `json:"assets"`
	Album         *Album     `json:"album"`
}

// Album is the subset of AlbumResponseDto this service reads.
type Album struct {
	ID                    string  `json:"id"`
	AlbumName             string  `json:"albumName"`
	Description           string  `json:"description"`
	AlbumThumbnailAssetID *string `json:"albumThumbnailAssetId"`
}

// Asset is the subset of AssetResponseDto this service reads.
type Asset struct {
	ID               string    `json:"id"`
	Type             string    `json:"type"` // IMAGE | VIDEO | AUDIO | OTHER
	OriginalMimeType string    `json:"originalMimeType"`
	Checksum         string    `json:"checksum"`
	UpdatedAt        time.Time `json:"updatedAt"`
	LocalDateTime    string    `json:"localDateTime"`
	FileCreatedAt    string    `json:"fileCreatedAt"`
	LivePhotoVideoID *string   `json:"livePhotoVideoId"`
	Thumbhash        *string   `json:"thumbhash"`
	Width            *int      `json:"width"`
	Height           *int      `json:"height"`
	IsTrashed        bool      `json:"isTrashed"`
	Visibility       string    `json:"visibility"`
	IsEdited         bool      `json:"isEdited"`
	ExifInfo         *struct {
		Description *string `json:"description"`
	} `json:"exifInfo"`
}

// Bucket is one month of the timeline.
type Bucket struct {
	TimeBucket string `json:"timeBucket"`
	Count      int    `json:"count"`
}

// BucketAssets is TimeBucketAssetResponseDto: parallel arrays.
type BucketAssets struct {
	ID               []string  `json:"id"`
	Ratio            []float64 `json:"ratio"`
	Visibility       []string  `json:"visibility"`
	IsTrashed        []bool    `json:"isTrashed"`
	IsImage          []bool    `json:"isImage"`
	Thumbhash        []*string `json:"thumbhash"`
	FileCreatedAt    []string  `json:"fileCreatedAt"`
	LocalOffsetHours []float64 `json:"localOffsetHours"`
	LivePhotoVideoID []*string `json:"livePhotoVideoId"`
}

// Len is the number of assets, or -1 when the arrays disagree.
func (b BucketAssets) Len() int {
	n := len(b.ID)
	for _, l := range []int{len(b.Ratio), len(b.Visibility), len(b.IsTrashed), len(b.IsImage),
		len(b.Thumbhash), len(b.FileCreatedAt), len(b.LocalOffsetHours), len(b.LivePhotoVideoID)} {
		if l != n {
			return -1
		}
	}
	return n
}

// GetMine is GET /shared-links/me: the share's settings, and for
// individual shares its assets. A password share without a valid token is
// ErrPasswordRequired.
func (c *Client) GetMine(ctx context.Context, a Auth) (SharedLink, error) {
	var out SharedLink
	err := c.getJSON(ctx, a, "/shared-links/me", nil, &out)
	return out, err
}

// Login is POST /shared-links/login and returns the share token.
func (c *Client) Login(ctx context.Context, target linkref.Target, password string) (string, error) {
	body, err := json.Marshal(map[string]string{"password": password})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/shared-links/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	Auth{Target: target}.Apply(req.Header)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUpstream, Redact(err))
	}
	defer resp.Body.Close()
	msg := ReadMessage(resp.Body)
	switch {
	case resp.StatusCode == http.StatusUnauthorized && msg == "Invalid password":
		return "", ErrInvalidPassword
	case resp.StatusCode == http.StatusBadRequest && msg == "Shared link is not password protected":
		return "", ErrNotPasswordProtected
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
	default:
		return "", classify(resp.StatusCode, msg)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == linkCookie && ck.Value != "" {
			v, err := url.QueryUnescape(ck.Value)
			if err != nil {
				return "", fmt.Errorf("%w: token cookie: %w", ErrUpstream, err)
			}
			// Immich merges tokens of several shares with commas; we never
			// send any, so there is exactly one.
			v, _, _ = strings.Cut(v, ",")
			return v, nil
		}
	}
	return "", fmt.Errorf("%w: login without token cookie", ErrUpstream)
}

// Buckets lists an album's timeline months.
func (c *Client) Buckets(ctx context.Context, a Auth, albumID string) ([]Bucket, error) {
	var out []Bucket
	q := url.Values{"albumId": {albumID}}
	err := c.getJSON(ctx, a, "/timeline/buckets", q, &out)
	return out, err
}

// Bucket returns one timeline month of an album.
func (c *Client) Bucket(ctx context.Context, a Auth, albumID, bucket string) (BucketAssets, error) {
	var out BucketAssets
	q := url.Values{"albumId": {albumID}, "timeBucket": {bucket}}
	if err := c.getJSON(ctx, a, "/timeline/bucket", q, &out); err != nil {
		return out, err
	}
	if out.Len() < 0 {
		return out, fmt.Errorf("%w: timeline bucket arrays differ in length", ErrUpstream)
	}
	return out, nil
}

// Asset is GET /assets/{id}.
func (c *Client) Asset(ctx context.Context, a Auth, id string) (Asset, error) {
	var out Asset
	err := c.getJSON(ctx, a, "/assets/"+url.PathEscape(id), nil, &out)
	return out, err
}

// Ping is the unauthenticated GET /server/ping (lifecycle probe).
func (c *Client) Ping(ctx context.Context) error {
	var out struct {
		Res string `json:"res"`
	}
	if err := c.getJSON(ctx, Auth{}, "/server/ping", nil, &out); err != nil {
		return err
	}
	if out.Res != "pong" {
		return fmt.Errorf("%w: unexpected ping response", ErrUpstream)
	}
	return nil
}

// Version is the unauthenticated GET /server/version.
func (c *Client) Version(ctx context.Context) (string, error) {
	var out struct {
		Major, Minor, Patch int
	}
	if err := c.getJSON(ctx, Auth{}, "/server/version", nil, &out); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d.%d", out.Major, out.Minor, out.Patch), nil
}

const maxJSONBytes = 64 << 20

func (c *Client) getJSON(ctx context.Context, a Auth, path string, q url.Values, dst any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	a.Apply(req.Header)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUpstream, Redact(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return classify(resp.StatusCode, ReadMessage(resp.Body))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(dst); err != nil {
		return fmt.Errorf("%w: decode %s: %w", ErrUpstream, path, err)
	}
	return nil
}

// classify maps an Immich error status onto the client errors. Immich
// answers 401 both for a bad key and for a password prompt, and 400 for
// missing access; only the message tells them apart.
func classify(status int, msg string) error {
	switch {
	case status == http.StatusUnauthorized && msg == "Password required":
		return ErrPasswordRequired
	case status == http.StatusUnauthorized, status == http.StatusForbidden, status == http.StatusNotFound,
		status == http.StatusBadRequest && IsNoAccess(msg):
		return ErrGone
	}
	return fmt.Errorf("%w: status %d", ErrUpstream, status)
}

// Redact drops the request URL from client errors: logs never carry
// Immich URLs.
func Redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// IsNoAccess reports whether an Immich 400 message is its access-check
// failure ("Not found or no asset.view access").
func IsNoAccess(msg string) bool { return strings.HasPrefix(msg, "Not found or no ") }

// ReadMessage reads Immich's error body ({"message": "…"}); message may
// also be a list of validation errors.
func ReadMessage(r io.Reader) string {
	var body struct {
		Message json.RawMessage `json:"message"`
	}
	if json.NewDecoder(io.LimitReader(r, 64<<10)).Decode(&body) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(body.Message, &s) == nil {
		return s
	}
	return ""
}
