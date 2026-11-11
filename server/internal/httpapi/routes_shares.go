package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"

	"github.com/go-fuego/fuego"

	"github.com/hmnd/secure-immich-share/server/internal/httpapi/contract"
	"github.com/hmnd/secure-immich-share/server/internal/linkref"
	"github.com/hmnd/secure-immich-share/server/internal/media"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/ratelimit"
	"github.com/hmnd/secure-immich-share/server/internal/share"
)

func (s *Server) registerShares(api *fuego.Server) {
	// The share pages embed a ShareView for the islands; no route returns one.
	fuego.SchemaTagFromType(api.OpenAPI, contract.ShareView{})

	kind := linkKindParam()
	fuego.Get(api, "/api/shares/{kind}/{ref}/assets/{id}", s.getShareAsset, opID("getShareAsset"), tags("shares"), kind)
	fuego.Post(api, "/api/shares/{kind}/{ref}/assets/{id}/media/{variant}/prepare", s.prepareMedia,
		opID("prepareMedia"), tags("media"), kind, pathEnum("variant", contract.MediaVariants...))
	fuego.Post(api, "/api/shares/{kind}/{ref}/zip", s.startZip, opID("startZip"), tags("media"), kind,
		optionalBody(contract.StartZipRequest{}),
		retryAfter(http.StatusServiceUnavailable, "Too many zips in preparation"))
	fuego.Get(api, "/api/shares/{kind}/{ref}/zip/{zipId}", s.getZip, opID("getZip"), tags("media"), kind)
}

// linkKindParam documents {kind} as the link kinds.
func linkKindParam() fuego.RouteOption {
	return pathEnum("kind", string(linkref.KindKey), string(linkref.KindSlug), string(linkref.KindToken))
}

var (
	assetIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	zipIDPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// visit is a request's link and its resolution.
type visit struct {
	kind linkref.Kind
	ref  string
	res  share.Result
}

// resolve parses {kind}/{ref}, opens the visitor's unlock cookies and
// resolves the share. Stale cookies are cleared; anything involving a
// password share is no-store.
func (s *Server) resolve(w http.ResponseWriter, r *http.Request) (visit, *apiError) {
	return s.resolveRef(w, r, linkref.Kind(r.PathValue("kind")), r.PathValue("ref"))
}

func (s *Server) resolveRef(w http.ResponseWriter, r *http.Request, kind linkref.Kind, ref string) (visit, *apiError) {
	target, err := s.deps.Links.Parse(kind, ref)
	if err != nil {
		return visit{}, errNotFound()
	}
	now := s.deps.Clock.Now()
	res, err := s.deps.Shares.Resolve(r.Context(), target, s.deps.Sealer.Sessions(r, now))
	switch {
	case errors.Is(err, share.ErrUnavailable):
		return visit{}, errUnavailable()
	case err != nil:
		return visit{}, errNotFound()
	}
	for _, name := range res.Clear {
		setUnlockCookie(w, name, "", -1)
	}
	switch res.State {
	case share.StateLocked, share.StateUnlocked:
		w.Header().Set("Cache-Control", "no-store")
	}
	return visit{kind: kind, ref: ref, res: res}, nil
}

// shared resolves and requires access (public or unlocked); locked shares
// are not found.
func (s *Server) shared(w http.ResponseWriter, r *http.Request) (visit, *share.Share, *apiError) {
	v, aerr := s.resolve(w, r)
	if aerr != nil {
		return v, nil, aerr
	}
	switch v.res.State {
	case share.StatePublic, share.StateUnlocked:
		return v, v.res.Share, nil
	}
	return v, nil, errNotFound()
}

func (v visit) base() string {
	return "/media/" + string(v.kind) + "/" + url.PathEscape(v.ref)
}

func (v visit) mediaURL(id string, variant sanitize.Variant) string {
	return v.base() + "/" + id + "/" + string(variant)
}

func (s *Server) view(v visit, sh *share.Share) contract.ShareView {
	out := contract.ShareView{
		Kind: string(sh.Kind), Title: sh.Title, Description: sh.Description, ExpiresAt: sh.ExpiresAt,
		AllowDownload: sh.AllowDownload, ZipAvailable: media.ZipAvailable(sh), CoverID: sh.CoverID,
		Items: make([]contract.ShareItem, 0, len(sh.Order)),
	}
	for _, id := range sh.Order {
		it := sh.Items[id]
		item := contract.ShareItem{
			ID: id, Kind: string(it.Kind), Ratio: it.Ratio, Thumbhash: it.Thumbhash,
			ThumbURL: v.mediaURL(id, sanitize.VariantThumb), PreviewURL: v.mediaURL(id, sanitize.VariantPreview),
			Variants: []contract.MediaVariant{},
		}
		if s.deps.Cfg.ShowDates && it.TakenAt != "" {
			item.TakenAt = &it.TakenAt
		}
		for _, mv := range media.Variants(sh, it) {
			item.Variants = append(item.Variants, contract.MediaVariant(mv))
		}
		out.Items = append(out.Items, item)
	}
	return out
}

const maxPasswordBytes = 1024

// unlock checks a share's password and sets its unlock cookie.
func (s *Server) unlock(w http.ResponseWriter, r *http.Request, kind linkref.Kind, ref, password string) *apiError {
	if password == "" || len(password) > maxPasswordBytes {
		return newAPIError(http.StatusUnauthorized, "invalid_password", "wrong password")
	}
	target, err := s.deps.Links.Parse(kind, ref)
	if err != nil {
		return errNotFound()
	}
	ip := ratelimit.Key(ratelimit.ClientIP(r, s.deps.Cfg.TrustedProxies))
	sess, err := s.deps.Shares.Unlock(r.Context(), target, password, ip)
	var rl share.RateLimitedError
	switch {
	case errors.As(err, &rl):
		return &apiError{Status: http.StatusTooManyRequests, Code: "rate_limited",
			Message: "too many attempts, try again later", RetryAfter: ratelimit.RetryAfterSeconds(rl.RetryAfter)}
	case errors.Is(err, share.ErrInvalidPassword):
		return newAPIError(http.StatusUnauthorized, "invalid_password", "wrong password")
	case errors.Is(err, share.ErrNotLocked):
		return errBadRequest("not_locked", "this share has no password")
	case errors.Is(err, share.ErrUnavailable):
		return errUnavailable()
	case err != nil:
		return errNotFound()
	}
	value, err := s.deps.Sealer.Seal(sess)
	if err != nil {
		return errInternal("seal session", err)
	}
	setUnlockCookie(w, share.CookieName(sess.ShareID), value, max(1, int(sess.Exp.Sub(s.deps.Clock.Now()).Seconds())))
	return nil
}

func (s *Server) getShareAsset(c fuego.ContextNoBody) (contract.AssetDetail, error) {
	r := c.Request()
	id := r.PathValue("id")
	if !assetIDPattern.MatchString(id) {
		return contract.AssetDetail{}, errNotFound()
	}
	_, sh, aerr := s.shared(c.Response(), r)
	if aerr != nil {
		return contract.AssetDetail{}, aerr
	}
	it, ok := sh.Item(id)
	if !ok {
		return contract.AssetDetail{}, errNotFound()
	}
	var out contract.AssetDetail
	if s.deps.Cfg.ShowDates && it.TakenAt != "" {
		out.TakenAt = &it.TakenAt
	}
	if s.deps.Cfg.ShowDescriptions {
		d, err := s.deps.Shares.Detail(r.Context(), sh, id)
		if err != nil {
			return contract.AssetDetail{}, mediaError(err)
		}
		out.Description = d.Description
	}
	return out, nil
}

// setUnlockCookie sets an unlock cookie for maxAge seconds, or clears it
// when maxAge is negative.
func setUnlockCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

// mediaError maps media and share errors for JSON routes.
func mediaError(err error) *apiError {
	switch {
	case errors.Is(err, media.ErrBusy):
		return errBusy()
	case errors.Is(err, media.ErrUnavailable), errors.Is(err, share.ErrUnavailable):
		return errUnavailable()
	}
	return errNotFound()
}

func (s *Server) prepareMedia(c fuego.ContextNoBody) (contract.MediaStatus, error) {
	r := c.Request()
	id, variant := r.PathValue("id"), sanitize.Variant(r.PathValue("variant"))
	switch variant {
	case sanitize.VariantFull, sanitize.VariantVideo, sanitize.VariantDownload:
	default:
		return contract.MediaStatus{}, errNotFound()
	}
	if !assetIDPattern.MatchString(id) {
		return contract.MediaStatus{}, errNotFound()
	}
	v, sh, aerr := s.shared(c.Response(), r)
	if aerr != nil {
		return contract.MediaStatus{}, aerr
	}
	st, err := s.deps.Media.Prepare(r.Context(), sh, id, variant)
	if err != nil {
		return contract.MediaStatus{}, mediaError(err)
	}
	switch st.Phase {
	case media.PhaseReady:
		return contract.MediaReady(v.mediaURL(id, variant)), nil
	case media.PhaseFailed:
		return contract.MediaFailed(), nil
	}
	return contract.MediaPending(string(st.Phase), st.RetryAfter.Milliseconds()), nil
}

func (s *Server) zipStatus(v visit, st media.ZipStatus) contract.ZipStatus {
	switch st.Phase {
	case media.ZipReady:
		return contract.ZipReady(st.ID, v.base()+"/zip/"+st.ID+"/photos.zip", st.Included, st.Skipped, st.Size)
	case media.ZipPreparing:
		return contract.ZipPreparing(st.ID, st.Done, st.Total)
	}
	return contract.ZipFailed()
}

// zipIDBytes bounds a startZip body: one quoted id and a comma per item.
const zipIDBytes = 64

func (s *Server) startZip(c fuego.ContextNoBody) (contract.ZipStatus, error) {
	w, r := c.Response(), c.Request()
	v, sh, aerr := s.shared(w, r)
	if aerr != nil {
		return contract.ZipStatus{}, aerr
	}
	ids, aerr := readZipIDs(w, r, len(sh.Order))
	if aerr != nil {
		return contract.ZipStatus{}, aerr
	}
	st, err := s.deps.Media.StartZip(sh, ids)
	switch {
	case errors.Is(err, media.ErrBusy):
		return contract.ZipStatus{}, errBusy()
	case err != nil:
		return contract.ZipStatus{}, errNotFound()
	}
	return s.zipStatus(v, st), nil
}

// readZipIDs reads startZip's optional body: nil for the whole share. Bad
// bodies and malformed ids are not found.
func readZipIDs(w http.ResponseWriter, r *http.Request, items int) ([]string, *apiError) {
	body := http.MaxBytesReader(w, r.Body, int64(items)*zipIDBytes+1024)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	var req contract.StartZipRequest
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		return nil, errNotFound()
	}
	for _, id := range req.AssetIDs {
		if !assetIDPattern.MatchString(id) {
			return nil, errNotFound()
		}
	}
	return req.AssetIDs, nil
}

func (s *Server) getZip(c fuego.ContextNoBody) (contract.ZipStatus, error) {
	r := c.Request()
	if !zipIDPattern.MatchString(r.PathValue("zipId")) {
		return contract.ZipStatus{}, errNotFound()
	}
	v, sh, aerr := s.shared(c.Response(), r)
	if aerr != nil {
		return contract.ZipStatus{}, aerr
	}
	st, err := s.deps.Media.ZipStatusOf(sh, r.PathValue("zipId"))
	if err != nil {
		return contract.ZipStatus{}, errNotFound()
	}
	return s.zipStatus(v, st), nil
}
