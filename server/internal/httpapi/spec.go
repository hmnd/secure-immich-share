package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-fuego/fuego"

	"github.com/hmnd/secure-immich-share/server/internal/httpapi/contract"
)

// newAPI builds the fuego server whose Mux carries every route. Its Run is
// never called: the app owns the listener, and the spec is file-only
// (OpenAPISpec), never served.
func (s *Server) newAPI() *fuego.Server {
	return fuego.NewServer(
		fuego.WithoutStartupMessages(),
		fuego.WithLoggingMiddleware(fuego.LoggingConfig{DisableRequest: true, DisableResponse: true}),
		fuego.WithSerializer(sendJSON),
		fuego.WithErrorSerializer(sendError),
		fuego.WithRouteOptions(
			fuego.OptionMiddleware(jsonContentType),
			fuego.OptionDefaultResponse("Error", fuego.Response{
				Type: contract.ErrorEnvelope{}, ContentTypes: []string{"application/json"},
			}),
			func(r *fuego.BaseRoute) {
				// Replaced by the default ErrorEnvelope response.
				r.Operation.Responses.Delete("400")
				r.Operation.Responses.Delete("500")
			},
		),
		fuego.WithEngineOptions(
			fuego.WithErrorHandler(s.handleError),
			fuego.WithoutFlowHeaders(),
			fuego.WithRequestContentType("application/json"),
			fuego.WithResponseContentType("application/json"),
			fuego.WithOpenAPIGeneratorSchemaCustomizer(customizeSchema),
			fuego.WithOpenAPIConfig(fuego.OpenAPIConfig{
				DisableSwaggerUI:     true,
				DisableLocalSave:     true,
				DisableDefaultServer: true,
				DisableMessages:      true,
				Info: &openapi3.Info{
					Title:       "secure-immich-share API",
					Description: "Generated from server/internal/httpapi.",
					Version:     "1",
				},
				MiddlewareConfig: fuego.MiddlewareConfig{
					DisableControllerSection: true,
					DisableMiddlewareSection: true,
				},
			}),
		),
	)
}

func opID(id string) fuego.RouteOption { return fuego.OptionOperationID(id) }

func tags(t ...string) fuego.RouteOption { return fuego.OptionTags(t...) }

// binary documents a 200 response with a binary body of the given media
// type (std handlers), plus 206 for Range requests.
func binary(mediaType string) fuego.RouteOption {
	return func(r *fuego.BaseRoute) {
		content := func() openapi3.Content {
			return openapi3.Content{mediaType: openapi3.NewMediaType().
				WithSchema(&openapi3.Schema{Type: &openapi3.Types{openapi3.TypeString}, Format: "binary"})}
		}
		resp := r.Operation.Responses.Value("200")
		if resp == nil {
			r.Operation.AddResponse(http.StatusOK, openapi3.NewResponse().WithDescription("OK"))
			resp = r.Operation.Responses.Value("200")
		}
		resp.Value.Content = content()
		partial := openapi3.NewResponse().WithDescription("Partial Content")
		partial.Content = content()
		r.Operation.AddResponse(http.StatusPartialContent, partial)
	}
}

// pathEnum documents a path parameter as a string enum.
func pathEnum(name string, values ...string) fuego.RouteOption {
	return func(r *fuego.BaseRoute) {
		enum := make([]any, len(values))
		for i, v := range values {
			enum[i] = v
		}
		fuego.OptionPath(name, "", fuego.ParamString())(r)
		for _, p := range r.Operation.Parameters {
			if p.Value != nil && p.Value.In == openapi3.ParameterInPath && p.Value.Name == name {
				p.Value.Schema = (&openapi3.Schema{Type: &openapi3.Types{openapi3.TypeString}, Enum: enum}).NewRef()
			}
		}
	}
}

// retryAfter documents the Retry-After header on an error status.
func retryAfter(status int, desc string) fuego.RouteOption {
	return func(r *fuego.BaseRoute) {
		resp := openapi3.NewResponse().WithDescription(desc)
		resp.Content = openapi3.NewContentWithJSONSchemaRef(&openapi3.SchemaRef{Ref: "#/components/schemas/ErrorEnvelope"})
		resp.Headers = openapi3.Headers{"Retry-After": &openapi3.HeaderRef{Value: &openapi3.Header{Parameter: openapi3.Parameter{
			Description: "Seconds to wait before retrying", Schema: openapi3.NewIntegerSchema().NewRef(),
		}}}}
		r.Operation.AddResponse(status, resp)
	}
}

// optionalBody documents an optional JSON request body of body's type, for
// handlers that read it themselves.
func optionalBody(body any) fuego.RouteOption {
	return func(r *fuego.BaseRoute) {
		fuego.OptionRequestBody(fuego.RequestBody{Type: body, ContentTypes: []string{"application/json"}})(r)
		r.Operation.RequestBody.Value.Required = false
	}
}

// OpenAPISpec renders the normalized, validated spec as indented JSON.
func (s *Server) OpenAPISpec() ([]byte, error) {
	doc := s.api.OutputOpenAPISpec()
	normalizeSpec(doc)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	loaded, err := openapi3.NewLoader().LoadFromData(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("openapi: reload spec: %w", err)
	}
	if err := loaded.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("openapi: invalid spec: %w", err)
	}
	return buf.Bytes(), nil
}

// normalizeSpec strips fuego defaults the contract does not use: the
// implicit Accept header param, generated descriptions, and components no
// operation references.
func normalizeSpec(doc *openapi3.T) {
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			op.Parameters = slices.DeleteFunc(op.Parameters, func(p *openapi3.ParameterRef) bool {
				return p.Value != nil && p.Value.In == openapi3.ParameterInHeader && p.Value.Name == "Accept"
			})
			if len(op.Parameters) == 0 {
				op.Parameters = nil
			}
			op.Description = ""
			op.Summary = ""
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				op.RequestBody.Value.Description = ""
			}
		}
	}
	for key, ref := range doc.Components.Schemas {
		if ref.Value != nil && ref.Value.Description == key+" schema" {
			ref.Value.Description = ""
		}
	}
	pruneComponents(doc, "ShareView")
	doc.Components.RequestBodies = nil
	doc.Components.Responses = nil
}

const schemaRefPrefix = "#/components/schemas/"

// pruneComponents drops component schemas unreachable from the paths and
// the roots.
func pruneComponents(doc *openapi3.T, roots ...string) {
	used := map[string]bool{}
	var walk func(*openapi3.SchemaRef)
	walk = func(ref *openapi3.SchemaRef) {
		if ref == nil {
			return
		}
		if name, ok := strings.CutPrefix(ref.Ref, schemaRefPrefix); ok {
			if used[name] {
				return
			}
			used[name] = true
			walk(doc.Components.Schemas[name])
			return
		}
		s := ref.Value
		if s == nil {
			return
		}
		for _, p := range s.Properties {
			walk(p)
		}
		walk(s.Items)
		walk(s.AdditionalProperties.Schema)
		for _, group := range []openapi3.SchemaRefs{s.OneOf, s.AnyOf, s.AllOf} {
			for _, r := range group {
				walk(r)
			}
		}
	}
	for _, name := range roots {
		walk(&openapi3.SchemaRef{Ref: schemaRefPrefix + name})
	}
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				for _, mt := range op.RequestBody.Value.Content {
					walk(mt.Schema)
				}
			}
			for _, resp := range op.Responses.Map() {
				if resp.Value == nil {
					continue
				}
				for _, mt := range resp.Value.Content {
					walk(mt.Schema)
				}
			}
			for _, p := range op.Parameters {
				if p.Value != nil {
					walk(p.Value.Schema)
				}
			}
		}
	}
	for name := range doc.Components.Schemas {
		if !used[name] {
			delete(doc.Components.Schemas, name)
		}
	}
}
