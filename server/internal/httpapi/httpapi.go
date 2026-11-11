// Package httpapi owns the HTTP surface: the JSON API, media routes, share
// pages (internal/pages), the web build's assets and middleware. API routes
// are typed fuego handlers over the DTOs in ./contract, which generate
// api/openapi.json (cmd/openapi).
package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/go-fuego/fuego"

	"github.com/hmnd/secure-immich-share/server/internal/clock"
	"github.com/hmnd/secure-immich-share/server/internal/config"
	"github.com/hmnd/secure-immich-share/server/internal/linkref"
	"github.com/hmnd/secure-immich-share/server/internal/media"
	"github.com/hmnd/secure-immich-share/server/internal/pages"
	"github.com/hmnd/secure-immich-share/server/internal/ratelimit"
	"github.com/hmnd/secure-immich-share/server/internal/share"
)

type Deps struct {
	Cfg    config.Config
	Clock  clock.Clock
	Logger *slog.Logger
	// Assets is the embedded web build (webassets.FS()); nil serves pages
	// without styles or islands.
	Assets fs.FS
	Health func() Health

	Links  *linkref.Parser
	Shares *share.Service
	Media  *media.Service
	Sealer *share.Sealer
}

// General per-IP request limits.
const (
	apiRate    = 60
	apiBurst   = 120
	mediaRate  = 200
	mediaBurst = 400
)

type Server struct {
	deps       Deps
	assets     pages.Assets
	api        *fuego.Server
	handler    http.Handler
	apiLimit   *ratelimit.Buckets
	mediaLimit *ratelimit.Buckets
}

// NewServer builds the server and registers all routes. Registration
// never touches deps, so cmd/openapi can build the spec from a zero Deps.
func NewServer(deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Clock == nil {
		deps.Clock = clock.Real{}
	}
	if deps.Health == nil {
		deps.Health = func() Health { return HealthReady }
	}
	s := &Server{
		deps:       deps,
		assets:     loadAssets(deps),
		apiLimit:   ratelimit.NewBuckets(deps.Clock, apiRate, apiBurst),
		mediaLimit: ratelimit.NewBuckets(deps.Clock, mediaRate, mediaBurst),
	}
	s.api = s.newAPI()
	s.routes(s.api)
	s.handler = s.withMiddleware(s.api.Mux)
	return s
}

func loadAssets(deps Deps) pages.Assets {
	if deps.Cfg.ViteDev {
		return pages.DevAssets()
	}
	a, err := pages.LoadAssets(deps.Assets)
	if err != nil {
		deps.Logger.Warn("pages without web build", "error", err.Error())
	}
	return a
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}
