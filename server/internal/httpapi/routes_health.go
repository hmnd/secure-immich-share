package httpapi

import "net/http"

// Health is the server state /healthz reports.
type Health string

const (
	HealthReady       Health = "ready"
	HealthDegraded    Health = "degraded" // Immich unreachable
	HealthUnavailable Health = "unavailable"
)

// registerHealth serves /healthz: 200 while ready or degraded (an Immich
// outage must not restart this service), 503 otherwise.
func (s *Server) registerHealth(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		health := s.deps.Health()
		status := http.StatusOK
		if health == HealthUnavailable {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, status, map[string]Health{"state": health})
	})
}
