package harness

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

// Contract conformance: tests talk to the app through a reverse proxy that
// validates every /api/ response against the committed api/openapi.json.
// Violations fail the test at cleanup.

var (
	specOnce   sync.Once
	specRouter routers.Router
	specErr    error
)

func contractRouter() (routers.Router, error) {
	specOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		path := filepath.Join(filepath.Dir(file), "..", "..", "..", "api", "openapi.json")
		doc, err := openapi3.NewLoader().LoadFromFile(path)
		if err != nil {
			specErr = fmt.Errorf("harness: load %s: %w", path, err)
			return
		}
		closeObjects(doc)
		specRouter, specErr = legacy.NewRouter(doc)
	})
	return specRouter, specErr
}

// closeObjects sets additionalProperties: false on every object schema, so
// a response field outside the contract fails the test.
func closeObjects(doc *openapi3.T) {
	seen := map[*openapi3.Schema]bool{}
	var walk func(*openapi3.SchemaRef)
	walk = func(ref *openapi3.SchemaRef) {
		if ref == nil || ref.Value == nil || seen[ref.Value] {
			return
		}
		s := ref.Value
		seen[s] = true
		if s.Type != nil && s.Type.Is(openapi3.TypeObject) && s.AdditionalProperties.Schema == nil {
			s.AdditionalProperties.Has = new(false)
		}
		for _, p := range s.Properties {
			walk(p)
		}
		walk(s.Items)
		for _, group := range []openapi3.SchemaRefs{s.OneOf, s.AnyOf, s.AllOf} {
			for _, r := range group {
				walk(r)
			}
		}
	}
	for _, ref := range doc.Components.Schemas {
		walk(ref)
	}
}

type conformance struct {
	router routers.Router

	mu         sync.Mutex
	violations []string
}

func (c *conformance) record(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.violations = append(c.violations, fmt.Sprintf(format, args...))
}

func (c *conformance) Violations() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.violations...)
}

// check validates one response. A success on an /api/ route missing from
// the spec is a violation.
func (c *conformance) check(resp *http.Response) error {
	req := resp.Request
	if req == nil || !strings.HasPrefix(req.URL.Path, "/api/") {
		return nil
	}
	route, params, err := c.router.FindRoute(req)
	if err != nil {
		if resp.StatusCode < 300 {
			c.record("%s %s → %d is not in api/openapi.json", req.Method, req.URL.Path, resp.StatusCode)
		}
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request: req, PathParams: params, Route: route,
		},
		Status: resp.StatusCode,
		Header: resp.Header,
		Body:   io.NopCloser(bytes.NewReader(body)),
		Options: &openapi3filter.Options{
			IncludeResponseStatus: true,
			MultiError:            true,
			ExcludeResponseBody:   ct != "application/json",
		},
	}
	if verr := openapi3filter.ValidateResponse(context.Background(), input); verr != nil {
		c.record("%s %s → %d violates api/openapi.json: %v\nbody: %.300s",
			req.Method, req.URL.RequestURI(), resp.StatusCode, verr, body)
	}
	return nil
}

// startContractProxy serves on ln and forwards to the app at appAddr,
// checking each response.
func startContractProxy(ln net.Listener, appAddr string, c *conformance) *http.Server {
	target := &url.URL{Scheme: "http", Host: appAddr}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			// Rewrite drops X-Forwarded-For; tests set it to act as
			// visitors behind a trusted reverse proxy.
			pr.Out.Header["X-Forwarded-For"] = pr.In.Header["X-Forwarded-For"]
		},
		ModifyResponse: func(resp *http.Response) error {
			// resp.Request is the outbound request; validate against the
			// path the client asked for (identical apart from the host).
			return c.check(resp)
		},
	}
	srv := &http.Server{Handler: proxy}
	go func() { _ = srv.Serve(ln) }()
	return srv
}
