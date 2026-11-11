package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/go-fuego/fuego"

	"github.com/hmnd/secure-immich-share/server/internal/media"
	"github.com/hmnd/secure-immich-share/server/internal/media/cache"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/share"
)

// Media routes serve sanitize.Verified files only: every handler gets its
// bytes from media.Service, never from Immich.
func (s *Server) registerMedia(api *fuego.Server) {
	kind := linkKindParam()
	routes := []struct {
		variant sanitize.Variant
		op      string
		mime    string
	}{
		{sanitize.VariantThumb, "getThumb", "image/*"},
		{sanitize.VariantPreview, "getPreview", "image/*"},
		{sanitize.VariantFull, "getFull", "image/*"},
		{sanitize.VariantVideo, "getVideo", "video/mp4"},
		{sanitize.VariantDownload, "getDownload", "*/*"},
	}
	version := fuego.OptionQuery("v", "The item's version from its thumbUrl or previewUrl; "+
		"a current one makes the response cacheable for long")
	for _, rt := range routes {
		opts := []fuego.RouteOption{opID(rt.op), tags("media"), kind, binary(rt.mime)}
		if rt.variant == sanitize.VariantThumb || rt.variant == sanitize.VariantPreview {
			opts = append(opts, version)
		}
		fuego.GetStd(api, "/media/{kind}/{ref}/{id}/"+string(rt.variant), s.serveMedia(rt.variant), opts...)
	}
	fuego.GetStd(api, "/media/{kind}/{ref}/zip/{zipId}/photos.zip", s.serveZip, opID("getZipFile"), tags("media"), kind,
		binary("application/zip"))
}

func (s *Server) mediaHeaders(w http.ResponseWriter, state share.State, h *cache.Handle, current bool) {
	hdr := w.Header()
	hdr.Set("Content-Type", h.V.ContentType())
	hdr.Set("ETag", `"`+h.ETag+`"`)
	switch {
	case state != share.StatePublic:
		hdr.Set("Cache-Control", "no-store")
	case current:
		hdr.Set("Cache-Control", "private, max-age=2592000, immutable")
	default:
		hdr.Set("Cache-Control", "private, max-age=300")
	}
}

// currentVersion reports whether a thumb or preview request carries the
// item's media.ListingVersion, so its URL changes with its bytes.
func currentVersion(r *http.Request, sh *share.Share, id string, variant sanitize.Variant) bool {
	if variant != sanitize.VariantThumb && variant != sanitize.VariantPreview {
		return false
	}
	it, ok := sh.Item(id)
	ver := media.ListingVersion(it)
	return ok && ver != "" && r.URL.Query().Get("v") == ver
}

// noWriteDeadline lifts the write deadline for long downloads.
func noWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
}

func (s *Server) serveMedia(variant sanitize.Variant) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !assetIDPattern.MatchString(id) {
			writeAPIError(w, errNotFound())
			return
		}
		v, sh, aerr := s.shared(w, r)
		if aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		h, err := s.deps.Media.Get(r.Context(), sh, id, variant)
		if err != nil {
			if errors.Is(err, media.ErrBusy) {
				writeAPIError(w, errBusy())
				return
			}
			// Every other failure looks the same.
			writeAPIError(w, errNotFound())
			return
		}
		defer h.Release()
		f, err := h.V.Open()
		if err != nil {
			writeAPIError(w, errNotFound())
			return
		}
		defer f.Close()
		s.mediaHeaders(w, v.res.State, h, currentVersion(r, sh, id, variant))
		if variant == sanitize.VariantDownload {
			n := slices.Index(sh.Order, id) + 1
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%03d.%s"`, n, h.V.Ext()))
		}
		if variant == sanitize.VariantVideo || variant == sanitize.VariantDownload {
			noWriteDeadline(w)
		}
		http.ServeContent(w, r, "", time.Time{}, f)
	}
}

func (s *Server) serveZip(w http.ResponseWriter, r *http.Request) {
	zipID := r.PathValue("zipId")
	if !zipIDPattern.MatchString(zipID) {
		writeAPIError(w, errNotFound())
		return
	}
	_, sh, aerr := s.shared(w, r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	rd, err := s.deps.Media.OpenZip(sh, zipID)
	if err != nil {
		writeAPIError(w, errNotFound())
		return
	}
	defer rd.Close()
	hdr := w.Header()
	hdr.Set("Content-Type", "application/zip")
	hdr.Set("Content-Disposition", `attachment; filename="photos.zip"`)
	hdr.Set("ETag", `"`+zipID+`"`)
	hdr.Set("Cache-Control", "no-store")
	noWriteDeadline(w)
	http.ServeContent(w, r, "", time.Time{}, rd)
}
