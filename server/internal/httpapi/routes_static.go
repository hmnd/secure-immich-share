package httpapi

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"time"
)

// registerStatic serves the web build's /assets/ and answers everything
// unrouted: JSON 404 under /api/ and /media/, the 404 page for other GETs
// and 405 for other methods.
func (s *Server) registerStatic(mux *http.ServeMux) {
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		writeAPIError(w, errNotFound())
	}
	mux.HandleFunc("/api/", notFound)
	mux.HandleFunc("/media/", notFound)
	mux.HandleFunc("GET /assets/{file...}", s.serveAsset)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		s.notFoundPage(w, r)
	})
}

// serveAsset serves a file of the build's assets/. Their names carry a
// content hash, so they are immutable and the name is the ETag.
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := path.Join("assets", r.PathValue("file"))
	if s.deps.Assets == nil || !fs.ValidPath(name) {
		s.notFoundPage(w, r)
		return
	}
	f, err := s.deps.Assets.Open(name)
	if err != nil {
		s.notFoundPage(w, r)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	rs, ok := f.(io.ReadSeeker)
	if err != nil || stat.IsDir() || !ok {
		s.notFoundPage(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+path.Base(name)+`"`)
	http.ServeContent(w, r, name, time.Time{}, rs)
}
