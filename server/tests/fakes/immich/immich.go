// Package immich is a fake Immich server for the shared-link API: share
// info, password login, album timelines, asset detail and media endpoints,
// with Immich's access rules and status codes. It records every request so
// tests can assert what reached Immich. Runnable standalone via
// tests/cmd/fake-immich.
package immich

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/clock"
)

// Asset is a fixture asset. File fields are paths served for that size;
// empty means Immich has no such file.
type Asset struct {
	ID               string
	Type             string // IMAGE | VIDEO
	Mime             string // originalMimeType
	Checksum         string
	UpdatedAt        time.Time
	LocalDateTime    time.Time
	Description      string
	Thumbhash        string
	Width, Height    int
	LivePhotoVideoID string
	Trashed          bool
	Visibility       string // timeline (default), archive, hidden, locked
	Edited           bool

	Original, Fullsize, Preview, Thumbnail, Playback string
}

// Share is a fixture shared link.
type Share struct {
	ID            string
	Key           string // base64url
	Slug          string
	Password      string
	Album         bool
	AlbumID       string
	AlbumName     string
	AlbumDesc     string
	Description   *string
	ExpiresAt     *time.Time
	AllowDownload bool
	ShowMetadata  bool
	AssetIDs      []string
	Revoked       bool
}

// Request is one recorded request.
type Request struct {
	Method, Path string
	Query        url.Values
	Header       http.Header
}

// Immich is the fake server.
type Immich struct {
	srv   *httptest.Server
	clock clock.Clock

	mu        sync.Mutex
	shares    map[string]*Share // by ID
	assets    map[string]*Asset
	requests  []Request
	down      bool
	failNext  map[string]int           // path prefix → status for the next request
	delayNext map[string]time.Duration // path prefix → delay of the next request
	held      chan struct{}
}

// New starts an empty fake.
func New() *Immich {
	f := NewUnstarted()
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// NewUnstarted builds a fake whose Handler the caller serves.
func NewUnstarted() *Immich {
	return &Immich{clock: clock.Real{}, shares: map[string]*Share{}, assets: map[string]*Asset{},
		failNext: map[string]int{}, delayNext: map[string]time.Duration{}}
}

// SetClock sets the time share expiry is checked against.
func (f *Immich) SetClock(c clock.Clock) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = c
}

// Handler serves the fake's API.
func (f *Immich) Handler() http.Handler { return http.HandlerFunc(f.serve) }

// URL is the base URL (without /api).
func (f *Immich) URL() string { return f.srv.URL }

// Close stops the server.
func (f *Immich) Close() { f.srv.Close() }

// AddAsset adds or replaces an asset.
func (f *Immich) AddAsset(a Asset) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.Visibility == "" {
		a.Visibility = "timeline"
	}
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	}
	if a.LocalDateTime.IsZero() {
		a.LocalDateTime = time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	}
	cp := a
	f.assets[a.ID] = &cp
}

// AddShare adds or replaces a share.
func (f *Immich) AddShare(s Share) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := s
	cp.AssetIDs = slices.Clone(s.AssetIDs)
	f.shares[s.ID] = &cp
}

// UpdateShare edits a share in place.
func (f *Immich) UpdateShare(id string, edit func(*Share)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(f.shares[id])
}

// UpdateAsset edits an asset in place.
func (f *Immich) UpdateAsset(id string, edit func(*Asset)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(f.assets[id])
}

// SetDown makes every request fail with 503 (true) or restores service.
func (f *Immich) SetDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// Hold blocks every media response until release is called. Requests are
// recorded before they block.
func (f *Immich) Hold() (release func()) {
	ch := make(chan struct{})
	f.mu.Lock()
	f.held = ch
	f.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			if f.held == ch {
				f.held = nil
			}
			f.mu.Unlock()
			close(ch)
		})
	}
}

// FailNext makes the next request whose path starts with prefix (after
// /api) answer status.
func (f *Immich) FailNext(prefix string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext[prefix] = status
}

// DelayNext delays the next request whose path starts with prefix (after
// /api) by d.
func (f *Immich) DelayNext(prefix string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delayNext[prefix] = d
}

// Requests returns the recorded requests.
func (f *Immich) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// Count returns how many recorded requests match method and path prefix.
func (f *Immich) Count(method, prefix string) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return n
}

// ResetRequests clears the request log.
func (f *Immich) ResetRequests() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}

// Token is the password-share token Immich derives (asToken).
func Token(shareID, password string) string {
	sum := sha256.Sum256([]byte(shareID + "-" + password))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg, "error": http.StatusText(status), "statusCode": status})
}

func (f *Immich) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone()})
	down := f.down
	path := strings.TrimPrefix(r.URL.Path, "/api")
	fail := 0
	for prefix, status := range f.failNext {
		if strings.HasPrefix(path, prefix) {
			fail = status
			delete(f.failNext, prefix)
			break
		}
	}
	var wait time.Duration
	for prefix, d := range f.delayNext {
		if strings.HasPrefix(path, prefix) {
			wait = d
			delete(f.delayNext, prefix)
			break
		}
	}
	held := f.held
	f.mu.Unlock()
	time.Sleep(wait)

	if down {
		writeErr(w, http.StatusServiceUnavailable, "down")
		return
	}
	if fail != 0 {
		writeErr(w, fail, "injected failure")
		return
	}
	switch {
	case path == "/server/ping":
		writeJSON(w, 200, map[string]string{"res": "pong"})
	case path == "/server/version":
		writeJSON(w, 200, map[string]int{"major": 3, "minor": 4, "patch": 0})
	case path == "/shared-links/me" && r.Method == http.MethodGet:
		f.withShare(w, r, f.me)
	case path == "/shared-links/login" && r.Method == http.MethodPost:
		f.withShare(w, r, f.login)
	case path == "/timeline/buckets":
		f.withShare(w, r, f.buckets)
	case path == "/timeline/bucket":
		f.withShare(w, r, f.bucket)
	case strings.HasPrefix(path, "/assets/"):
		rest := strings.TrimPrefix(path, "/assets/")
		if _, media, _ := strings.Cut(rest, "/"); media != "" {
			if held != nil {
				<-held
			}
		}
		f.withShare(w, r, func(w http.ResponseWriter, r *http.Request, s *Share) {
			f.asset(w, r, s, rest)
		})
	default:
		writeErr(w, 404, "Not Found")
	}
}

// withShare resolves the share from the key/slug header or query, as
// AuthService.validate does.
func (f *Immich) withShare(w http.ResponseWriter, r *http.Request, h func(http.ResponseWriter, *http.Request, *Share)) {
	key := r.Header.Get("x-immich-share-key")
	if key == "" {
		key = r.URL.Query().Get("key")
	}
	slug := r.Header.Get("x-immich-share-slug")
	if slug == "" {
		slug = r.URL.Query().Get("slug")
	}
	f.mu.Lock()
	var share *Share
	for _, s := range f.shares {
		if (key != "" && s.Key == key) || (key == "" && slug != "" && s.Slug == slug) {
			share = s
		}
	}
	valid := share != nil && !share.Revoked && (share.ExpiresAt == nil || share.ExpiresAt.After(f.clock.Now()))
	var cp Share
	if valid {
		cp = *share
		cp.AssetIDs = slices.Clone(share.AssetIDs)
	}
	f.mu.Unlock()
	switch {
	case key == "" && slug == "":
		writeErr(w, 401, "Authentication required")
	case !valid && key != "":
		writeErr(w, 401, "Invalid share key")
	case !valid:
		writeErr(w, 401, "Invalid share slug")
	default:
		h(w, r, &cp)
	}
}

func tokensFrom(r *http.Request) []string {
	c, err := r.Cookie("immich_shared_link_token")
	if err != nil {
		return nil
	}
	v, err := url.QueryUnescape(c.Value)
	if err != nil {
		return nil
	}
	return strings.Split(v, ",")
}

func (f *Immich) me(w http.ResponseWriter, r *http.Request, s *Share) {
	if s.Password != "" && !slices.Contains(tokensFrom(r), Token(s.ID, s.Password)) {
		writeErr(w, 401, "Password required")
		return
	}
	writeJSON(w, 200, f.shareJSON(s))
}

func (f *Immich) login(w http.ResponseWriter, r *http.Request, s *Share) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "Bad Request")
		return
	}
	if s.Password == "" {
		writeErr(w, 400, "Shared link is not password protected")
		return
	}
	if body.Password != s.Password {
		writeErr(w, 401, "Invalid password")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "immich_shared_link_token", Value: url.QueryEscape(Token(s.ID, s.Password)),
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400,
	})
	writeJSON(w, 201, f.shareJSON(s))
}

func (f *Immich) shareJSON(s *Share) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]any{
		"id": s.ID, "description": s.Description, "expiresAt": s.ExpiresAt, "key": s.Key, "slug": nilIfEmpty(s.Slug),
		"allowDownload": s.AllowDownload, "showMetadata": s.ShowMetadata, "allowUpload": false,
		"password": nilIfEmpty(s.Password), "userId": "00000000-0000-4000-8000-000000000001",
		"createdAt": "2024-05-01T00:00:00.000Z", "assets": []any{},
	}
	if s.Album {
		out["type"] = "ALBUM"
		var thumb any
		if len(s.AssetIDs) > 0 {
			thumb = s.AssetIDs[0]
		}
		out["album"] = map[string]any{
			"id": s.AlbumID, "albumName": s.AlbumName, "description": s.AlbumDesc,
			"albumThumbnailAssetId": thumb, "assetCount": len(s.AssetIDs),
		}
	} else {
		out["type"] = "INDIVIDUAL"
		assets := []any{}
		for _, id := range s.AssetIDs {
			if a := f.assets[id]; a != nil {
				assets = append(assets, assetJSON(a))
			}
		}
		out["assets"] = assets
	}
	return out
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func assetJSON(a *Asset) map[string]any {
	var live any
	if a.LivePhotoVideoID != "" {
		live = a.LivePhotoVideoID
	}
	return map[string]any{
		"id": a.ID, "type": a.Type, "originalMimeType": a.Mime, "checksum": a.Checksum,
		"updatedAt": a.UpdatedAt.Format(time.RFC3339Nano), "localDateTime": a.LocalDateTime.Format("2006-01-02T15:04:05.000Z"),
		"fileCreatedAt":    a.LocalDateTime.Add(7 * time.Hour).Format(time.RFC3339Nano),
		"livePhotoVideoId": live, "thumbhash": nilIfEmpty(a.Thumbhash), "width": a.Width, "height": a.Height,
		"isTrashed": a.Trashed, "visibility": a.Visibility, "isEdited": a.Edited,
		"originalFileName": "IMG_" + a.ID[:4] + ".HEIC", "originalPath": "/upload/library/jane/IMG.HEIC",
		"ownerId": "00000000-0000-4000-8000-000000000001",
		"exifInfo": map[string]any{
			"description": a.Description, "latitude": 49.2827, "longitude": -123.1207, "city": "Vancouver",
			"make": "Apple", "model": "iPhone 15 Pro",
		},
	}
}

// albumAssets returns the share's album assets as Immich's timeline sees
// them: everything in the album, including trashed and locked ones, so
// the proxy's own filtering is exercised.
func (f *Immich) albumAssets(s *Share) []*Asset {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*Asset
	for _, id := range s.AssetIDs {
		if a := f.assets[id]; a != nil {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LocalDateTime.After(out[j].LocalDateTime) })
	return out
}

func bucketOf(t time.Time) string { return t.Format("2006-01") + "-01" }

func (f *Immich) buckets(w http.ResponseWriter, r *http.Request, s *Share) {
	if !s.Album || r.URL.Query().Get("albumId") != s.AlbumID {
		writeErr(w, 400, "Not found or no album.read access")
		return
	}
	counts := map[string]int{}
	for _, a := range f.albumAssets(s) {
		counts[bucketOf(a.LocalDateTime)]++
	}
	keys := slices.Sorted(maps.Keys(counts))
	slices.Reverse(keys)
	out := []map[string]any{}
	for _, k := range keys {
		out = append(out, map[string]any{"timeBucket": k, "count": counts[k]})
	}
	writeJSON(w, 200, out)
}

func (f *Immich) bucket(w http.ResponseWriter, r *http.Request, s *Share) {
	q := r.URL.Query()
	if !s.Album || q.Get("albumId") != s.AlbumID {
		writeErr(w, 400, "Not found or no album.read access")
		return
	}
	want := strings.TrimSuffix(q.Get("timeBucket"), "T00:00:00.000Z")
	cols := map[string][]any{}
	for _, k := range []string{"id", "ownerId", "ratio", "isFavorite", "visibility", "isTrashed", "isImage",
		"thumbhash", "createdAt", "fileCreatedAt", "localOffsetHours", "duration", "projectionType", "livePhotoVideoId",
		"city", "country", "latitude", "longitude"} {
		cols[k] = []any{}
	}
	for _, a := range f.albumAssets(s) {
		if bucketOf(a.LocalDateTime) != want {
			continue
		}
		ratio := 1.0
		if a.Height > 0 {
			ratio = float64(a.Width) / float64(a.Height)
		}
		var live any
		if a.LivePhotoVideoID != "" {
			live = a.LivePhotoVideoID
		}
		row := map[string]any{
			"id": a.ID, "ownerId": "00000000-0000-4000-8000-000000000001", "ratio": ratio, "isFavorite": false,
			"visibility": a.Visibility, "isTrashed": a.Trashed, "isImage": a.Type == "IMAGE",
			"thumbhash": nilIfEmpty(a.Thumbhash), "createdAt": a.UpdatedAt.Format(time.RFC3339),
			"fileCreatedAt": a.LocalDateTime.Add(7 * time.Hour).Format("2006-01-02T15:04:05.999"), "localOffsetHours": -7.0,
			"duration": nil, "projectionType": nil, "livePhotoVideoId": live,
			"city": "Vancouver", "country": "Canada", "latitude": 49.2827, "longitude": -123.1207,
		}
		for k := range cols {
			cols[k] = append(cols[k], row[k])
		}
	}
	writeJSON(w, 200, cols)
}

// accessible implements checkSharedLinkAccess: the share's assets and the
// motion parts of its live photos.
func (f *Immich) accessible(s *Share, id string) *Asset {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, member := range s.AssetIDs {
		a := f.assets[member]
		if a == nil {
			continue
		}
		if member == id {
			return a
		}
		if a.LivePhotoVideoID == id {
			return f.assets[id]
		}
	}
	return nil
}

func webSupported(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "image/webp", "image/gif", "image/avif":
		return true
	}
	return false
}

func (f *Immich) asset(w http.ResponseWriter, r *http.Request, s *Share, rest string) {
	id, sub, _ := strings.Cut(rest, "/")
	a := f.accessible(s, id)
	switch sub {
	case "":
		if a == nil {
			writeErr(w, 400, "Not found or no asset.read access")
			return
		}
		f.mu.Lock()
		body := assetJSON(a)
		f.mu.Unlock()
		writeJSON(w, 200, body)
	case "original":
		if a == nil || !s.AllowDownload {
			writeErr(w, 400, "Not found or no asset.download access")
			return
		}
		f.serveFile(w, r, a.Original)
	case "thumbnail":
		if a == nil {
			writeErr(w, 400, "Not found or no asset.view access")
			return
		}
		switch size := r.URL.Query().Get("size"); size {
		case "", "thumbnail":
			f.serveFile(w, r, a.Thumbnail)
		case "preview":
			f.serveFile(w, r, a.Preview)
		case "fullsize":
			q := r.URL.Query()
			switch {
			case webSupported(a.Mime) && !a.Edited:
				q.Del("size")
				http.Redirect(w, r, "original?"+q.Encode(), http.StatusFound)
			case a.Fullsize == "":
				q.Set("size", "preview")
				http.Redirect(w, r, r.URL.Path+"?"+q.Encode(), http.StatusFound)
			default:
				f.serveFile(w, r, a.Fullsize)
			}
		default:
			writeErr(w, 400, "Invalid size")
		}
	case "video/playback":
		if a == nil {
			writeErr(w, 400, "Not found or no asset.view access")
			return
		}
		if a.Type != "VIDEO" {
			writeErr(w, 404, "Asset not found or asset is not a video")
			return
		}
		f.serveFile(w, r, a.Playback)
	default:
		writeErr(w, 404, "Not Found")
	}
}

func (f *Immich) serveFile(w http.ResponseWriter, r *http.Request, path string) {
	if path == "" {
		writeErr(w, 404, "Asset media not found")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeErr(w, 500, fmt.Sprintf("open: %v", err))
		return
	}
	defer file.Close()
	w.Header().Set("Cache-Control", "private, max-age=86400, no-transform")
	http.ServeContent(w, r, "", time.Time{}, file)
}
