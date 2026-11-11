package httpapi

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/a-h/templ"

	"github.com/hmnd/secure-immich-share/server/internal/linkref"
	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
	"github.com/hmnd/secure-immich-share/server/internal/pages"
	"github.com/hmnd/secure-immich-share/server/internal/share"
)

// registerPages serves the share pages. Links use Immich's own paths, so
// Immich's "Copy link" works once its external domain is PUBLIC_URL. A
// password form posts back to its page.
func (s *Server) registerPages(mux *http.ServeMux) {
	for _, p := range []struct {
		prefix string
		kind   linkref.Kind
	}{{"/share/", linkref.KindKey}, {"/s/", linkref.KindSlug}, {"/t/", linkref.KindToken}} {
		mux.HandleFunc("GET "+p.prefix+"{ref}", s.sharePage(p.prefix, p.kind))
		mux.HandleFunc("POST "+p.prefix+"{ref}", s.unlockPage(p.prefix, p.kind))
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		s.renderPage(w, r, http.StatusOK, pages.MessagePage(s.assets, pages.Head{Title: pages.DefaultTitle},
			pages.Message{Title: "Nothing to see here"}))
	})
}

func (s *Server) sharePage(prefix string, kind linkref.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ref := r.PathValue("ref")
		v, aerr := s.resolveRef(w, r, kind, ref)
		if aerr != nil {
			s.errorPage(w, r, aerr)
			return
		}
		head := pages.Head{Title: pages.DefaultTitle}
		if v.res.State == share.StateLocked {
			s.renderPage(w, r, http.StatusOK, pages.LockedPage(s.assets, head, pages.Locked{}))
			return
		}
		sh := v.res.Share
		s.deps.Media.Prewarm(sh)
		page := pages.Share{
			Path: prefix + url.PathEscape(ref), Kind: string(kind), Ref: ref, MediaBase: v.base(), View: s.view(v, sh),
		}
		if page.View.Title != nil && *page.View.Title != "" {
			head.Title = *page.View.Title
		}
		// Crawlers have no unlock cookies: only public shares get an image.
		if s.deps.Cfg.OGPreviews && v.res.State == share.StatePublic && sh.CoverID != nil {
			// Built from PUBLIC_URL, never from the Host header.
			head.OGImage = s.deps.Cfg.PublicURL + v.mediaURL(*sh.CoverID, sanitize.VariantPreview)
		}
		if id := r.URL.Query().Get("photo"); id != "" {
			page.Photo = s.openPhoto(r, sh, page, id)
		}
		s.renderPage(w, r, http.StatusOK, pages.SharePage(s.assets, head, page))
	}
}

// openPhoto is the ?photo= item, or nil when it isn't in the share.
func (s *Server) openPhoto(r *http.Request, sh *share.Share, page pages.Share, id string) *pages.Photo {
	for i, it := range page.View.Items {
		if it.ID != id {
			continue
		}
		p := &pages.Photo{Index: i}
		if s.deps.Cfg.ShowDescriptions {
			if d, err := s.deps.Shares.Detail(r.Context(), sh, id); err == nil {
				p.Description = d.Description
			}
		}
		return p
	}
	return nil
}

// unlockPage handles a password form: back to the page on success, the form
// again with the reason otherwise.
func (s *Server) unlockPage(prefix string, kind linkref.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		head := pages.Head{Title: pages.DefaultTitle}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		if err := r.ParseForm(); err != nil {
			s.renderPage(w, r, http.StatusBadRequest, pages.LockedPage(s.assets, head, pages.Locked{}))
			return
		}
		ref := r.PathValue("ref")
		aerr := s.unlock(w, r, kind, ref, r.PostForm.Get("password"))
		if aerr == nil || aerr.Code == "not_locked" {
			http.Redirect(w, r, prefix+url.PathEscape(ref), http.StatusSeeOther)
			return
		}
		var msg string
		switch aerr.Code {
		case "invalid_password":
			msg = "Wrong password"
		case "rate_limited":
			msg = "Too many attempts. Try again in " + strconv.Itoa(aerr.RetryAfter) + "s"
		case "upstream_unavailable":
			msg = "The photo server is unavailable. Try again shortly."
		default:
			s.errorPage(w, r, aerr)
			return
		}
		setRetryAfter(w, aerr)
		s.renderPage(w, r, aerr.Status, pages.LockedPage(s.assets, head, pages.Locked{Message: msg}))
	}
}

func (s *Server) errorPage(w http.ResponseWriter, r *http.Request, aerr *apiError) {
	if aerr.Status >= 500 && aerr.Cause != nil {
		s.deps.Logger.ErrorContext(r.Context(), "page error", "code", aerr.Code, "error", aerr.Cause.Error())
	}
	head := pages.Head{Title: pages.DefaultTitle}
	m := pages.Message{Title: "Something went wrong"}
	switch {
	case aerr.Status == http.StatusNotFound:
		m.Title = "This link doesn’t exist or has expired."
	case aerr.Code == "upstream_unavailable":
		m = pages.Message{Title: "Photos are temporarily unavailable", Retry: r.URL.RequestURI()}
	default:
		m.Retry = r.URL.RequestURI()
	}
	setRetryAfter(w, aerr)
	s.renderPage(w, r, aerr.Status, pages.MessagePage(s.assets, head, m))
}

func (s *Server) notFoundPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, http.StatusNotFound, pages.MessagePage(s.assets, pages.Head{Title: "Not found"},
		pages.Message{Title: "Page not found"}))
}

// renderPage renders c fully before writing, so a failed render is a 500.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		if !errors.Is(err, r.Context().Err()) {
			s.deps.Logger.ErrorContext(r.Context(), "render page", "error", err.Error())
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
