package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/pages"
	"github.com/hmnd/secure-immich-share/server/internal/ratelimit"
)

// statusWriter records the status code for logging.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) withMiddleware(mux *http.ServeMux) http.Handler {
	h := s.withWriteDeadline(recordPattern(mux))
	h = s.withCSRF(h)
	h = s.withRateLimit(h)
	return s.withLogging(s.withRecover(s.withHeaders(h)))
}

// Write deadlines: JSON and pages are quick; media gets longer, and video,
// download and zip handlers lift the deadline while streaming from disk.
const (
	writeTimeout      = 30 * time.Second
	mediaWriteTimeout = 5 * time.Minute
)

func (s *Server) withWriteDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline := writeTimeout
		if strings.HasPrefix(r.URL.Path, "/media/") {
			deadline = mediaWriteTimeout
		}
		// Deadlines set here outlive the request on keep-alive connections
		// (the server only resets them when WriteTimeout is set), so clear
		// it afterwards.
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Now().Add(deadline))
		defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
		next.ServeHTTP(w, r)
	})
}

// withRateLimit applies the per-IP request limits to the API, media and
// the password forms.
func (s *Server) withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var limiter *ratelimit.Buckets
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"), r.Method == http.MethodPost:
			limiter = s.apiLimit
		case strings.HasPrefix(r.URL.Path, "/media/"):
			limiter = s.mediaLimit
		}
		if limiter != nil {
			ip := ratelimit.Key(ratelimit.ClientIP(r, s.deps.Cfg.TrustedProxies))
			if ok, wait := limiter.Allow(ip); !ok {
				writeAPIError(w, &apiError{Status: http.StatusTooManyRequests, Code: "rate_limited",
					Message: "too many requests", RetryAfter: ratelimit.RetryAfterSeconds(wait)})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// withCSRF refuses cross-origin mutations: API calls and password forms.
// Pages send Referrer-Policy: no-referrer, so browsers post forms with
// Origin: null and Sec-Fetch-Site decides. PUBLIC_URL's origin is trusted
// for proxies that rewrite Host, such as the Vite dev server.
func (s *Server) withCSRF(next http.Handler) http.Handler {
	c := http.NewCrossOriginProtection()
	if u, err := url.Parse(s.deps.Cfg.PublicURL); err == nil && u.Host != "" {
		if err := c.AddTrustedOrigin(u.Scheme + "://" + u.Host); err != nil {
			s.deps.Logger.Error("PUBLIC_URL is not a valid origin", "error", err.Error())
		}
	}
	c.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusForbidden, "csrf", "cross-origin request refused")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		s.renderPage(w, r, http.StatusForbidden, pages.MessagePage(s.assets, pages.Head{Title: pages.DefaultTitle},
			pages.Message{Title: "Something went wrong", Detail: "Open the link again and retry."}))
	}))
	return c.Handler(next)
}

// routeKey holds the *string the mux's matched pattern is written into, so
// the logging middleware can read it.
type routeKey struct{}

func recordPattern(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := r.Context().Value(routeKey{}).(*string); ok {
			defer func() { *p = r.Pattern }()
		}
		mux.ServeHTTP(w, r)
	})
}

// logRoute is what request logs show: the matched route pattern, never the
// raw path (share keys, slugs and tokens live in URLs).
func logRoute(pattern string) string {
	if pattern != "" {
		return pattern
	}
	return "(unmatched)"
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.deps.Clock.Now()
		sw := &statusWriter{ResponseWriter: w}
		pattern := new(string)
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, pattern))
		next.ServeHTTP(sw, r)
		s.deps.Logger.InfoContext(r.Context(), "http request",
			"method", r.Method, "route", logRoute(*pattern),
			"status", sw.status, "bytes", sw.bytes, "duration", s.deps.Clock.Now().Sub(start).String())
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw, _ := w.(*statusWriter)
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			pattern, _ := r.Context().Value(routeKey{}).(*string)
			route := ""
			if pattern != nil {
				route = *pattern
			}
			s.deps.Logger.ErrorContext(r.Context(), "http panic",
				"method", r.Method, "route", logRoute(route), "panic", rec, "stack", string(debug.Stack()))
			if sw == nil || sw.status == 0 {
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

const csp = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
	"media-src 'self'; connect-src 'self'; font-src 'self'; manifest-src 'self'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

// withHeaders sets security headers on every response.
func (s *Server) withHeaders(next http.Handler) http.Handler {
	hsts := strings.HasPrefix(s.deps.Cfg.PublicURL, "https://")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", permissionsPolicy)
		h.Set("X-Robots-Tag", "noindex, nofollow")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

const permissionsPolicy = "accelerometer=(), autoplay=(), camera=(), display-capture=(), encrypted-media=(), " +
	"fullscreen=(self), geolocation=(), gyroscope=(), hid=(), idle-detection=(), magnetometer=(), microphone=(), " +
	"midi=(), payment=(), picture-in-picture=(self), publickey-credentials-get=(), screen-wake-lock=(self), serial=(), " +
	"usb=(), web-share=(self), xr-spatial-tracking=()"
