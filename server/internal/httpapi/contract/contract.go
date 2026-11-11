// Package contract holds the HTTP API DTOs; api/openapi.json is generated
// from them, so their Go names are the spec's component names. Immich types
// never appear here, so no Immich field can reach a visitor by accident.
//
// Rules (enforced by the schema customizer in internal/httpapi):
//   - fields never use omitempty: every field is required and pointer
//     fields are nullable;
//   - slices and maps are never null;
//   - `validate:"oneof=a b"` documents an enum;
//   - unions are oneOf + discriminator via SetSchema.
package contract

import (
	"slices"

	"github.com/getkin/kin-openapi/openapi3"
)

// ErrorEnvelope is every error response:
// {"error":{"code":"snake_case","message":"..."}}.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func types(t ...string) *openapi3.Types {
	out := openapi3.Types(t)
	return &out
}

func object(props map[string]*openapi3.SchemaRef, required ...string) *openapi3.Schema {
	slices.Sort(required)
	s := &openapi3.Schema{
		Type: types("object"), Properties: openapi3.Schemas{}, Required: required,
		AdditionalProperties: openapi3.AdditionalProperties{Has: new(false)},
	}
	for k, v := range props {
		s.Properties[k] = v
	}
	return s
}

func str() *openapi3.SchemaRef { return (&openapi3.Schema{Type: types("string")}).NewRef() }

func constStr(v string) *openapi3.SchemaRef {
	return (&openapi3.Schema{Type: types("string"), Enum: []any{v}}).NewRef()
}

func enumStr(vs ...string) *openapi3.SchemaRef {
	enum := make([]any, len(vs))
	for i, v := range vs {
		enum[i] = v
	}
	return (&openapi3.Schema{Type: types("string"), Enum: enum}).NewRef()
}

func integer() *openapi3.SchemaRef { return (&openapi3.Schema{Type: types("integer")}).NewRef() }

// union is a oneOf over inline object variants discriminated by prop.
func union(s *openapi3.Schema, prop string, variants ...*openapi3.Schema) {
	s.Type = nil
	s.Properties = nil
	s.OneOf = make(openapi3.SchemaRefs, len(variants))
	for i, v := range variants {
		s.OneOf[i] = v.NewRef()
	}
	s.Discriminator = &openapi3.Discriminator{PropertyName: prop}
}
