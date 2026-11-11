// Package fetch downloads raw asset bytes from Immich into the cache's tmp
// directory. Go's internal rule makes it importable only by internal/media,
// so no HTTP handler can stream Immich bytes to a visitor.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/hmnd/secure-immich-share/server/internal/immich"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
)

// Fetcher downloads from one Immich server.
type Fetcher struct {
	base     *url.URL // …/api
	client   *http.Client
	maxBytes int64
}

// New builds a fetcher. Redirects are followed only within IMMICH_URL's
// origin and /api prefix (fullsize → original), with the share headers
// kept.
func New(immichURL string, transport http.RoundTripper, maxBytes int64) (*Fetcher, error) {
	base, err := url.Parse(strings.TrimRight(immichURL, "/") + "/api/")
	if err != nil {
		return nil, err
	}
	f := &Fetcher{base: base, maxBytes: maxBytes}
	f.client = &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("fetch: too many redirects")
			}
			if req.URL.Scheme != base.Scheme || req.URL.Host != base.Host || !strings.HasPrefix(req.URL.Path, base.Path) {
				return errors.New("fetch: redirect outside Immich")
			}
			// Same host: Go keeps custom headers (the share key) on redirects.
			return nil
		},
	}
	return f, nil
}

// endpoint is the Immich path (relative to /api/) and query for a source.
func endpoint(id string, src sanitize.Source) (path, query string, err error) {
	id = url.PathEscape(id)
	switch src {
	case sanitize.SourceOriginal:
		return "assets/" + id + "/original", "", nil
	case sanitize.SourceFullsize:
		return "assets/" + id + "/thumbnail", "size=fullsize", nil
	case sanitize.SourcePreview:
		return "assets/" + id + "/thumbnail", "size=preview", nil
	case sanitize.SourceThumbnail:
		return "assets/" + id + "/thumbnail", "size=thumbnail", nil
	case sanitize.SourcePlayback, sanitize.SourceMotion:
		return "assets/" + id + "/video/playback", "", nil
	}
	return "", "", fmt.Errorf("fetch: unknown source %q", src)
}

// Fetch downloads asset id's src into dst. Failures are sanitize.SourceError.
// For SourceMotion, id is the live photo's motion asset.
func (f *Fetcher) Fetch(ctx context.Context, auth immich.Auth, id string, src sanitize.Source, dst string) error {
	p, q, err := endpoint(id, src)
	if err != nil {
		return sanitize.SourceError{Kind: sanitize.SourceUpstream, Err: err}
	}
	u := f.base.ResolveReference(&url.URL{Path: p, RawQuery: q})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return sanitize.SourceError{Kind: sanitize.SourceUpstream, Err: err}
	}
	auth.Apply(req.Header)
	resp, err := f.client.Do(req)
	if err != nil {
		return sanitize.SourceError{Kind: sanitize.SourceUpstream, Err: immich.Redact(err)}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return sanitize.SourceError{Kind: sanitize.SourceNotFound, Err: errors.New("404")}
	case http.StatusUnauthorized, http.StatusForbidden:
		return sanitize.SourceError{Kind: sanitize.SourceForbidden, Err: fmt.Errorf("%d", resp.StatusCode)}
	case http.StatusBadRequest:
		if immich.IsNoAccess(immich.ReadMessage(resp.Body)) {
			return sanitize.SourceError{Kind: sanitize.SourceForbidden, Err: errors.New("no access")}
		}
		return sanitize.SourceError{Kind: sanitize.SourceUpstream, Err: errors.New("400")}
	default:
		return sanitize.SourceError{Kind: sanitize.SourceUpstream, Err: fmt.Errorf("status %d", resp.StatusCode)}
	}
	if resp.ContentLength > f.maxBytes {
		return sanitize.SourceError{Kind: sanitize.SourceTooLarge, Err: fmt.Errorf("%d bytes", resp.ContentLength)}
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return sanitize.SourceError{Kind: sanitize.SourceUpstream, Err: err}
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, f.maxBytes+1))
	cerr := out.Close()
	switch {
	case err != nil:
		_ = os.Remove(dst)
		return sanitize.SourceError{Kind: sanitize.SourceUpstream, Err: immich.Redact(err)}
	case cerr != nil:
		_ = os.Remove(dst)
		return sanitize.SourceError{Kind: sanitize.SourceUpstream, Err: cerr}
	case n > f.maxBytes:
		_ = os.Remove(dst)
		return sanitize.SourceError{Kind: sanitize.SourceTooLarge, Err: fmt.Errorf("over %d bytes", f.maxBytes)}
	}
	return nil
}
