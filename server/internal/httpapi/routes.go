package httpapi

import "github.com/go-fuego/fuego"

// routes lists every route group. Keep this file to registration calls only;
// each domain's routes live in routes_<domain>.go.
func (s *Server) routes(api *fuego.Server) {
	s.registerHealth(api.Mux)
	s.registerShares(api)
	s.registerMedia(api)
	s.registerPages(api.Mux)
	s.registerStatic(api.Mux)
}
