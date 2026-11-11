package httpapi

import (
	"reflect"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3gen"
)

var setSchemaType = reflect.TypeFor[openapi3gen.SetSchemar]()

// customizeSchema runs after fuego's own customizer for every generated
// schema node and applies the contract rules (see package contract):
//   - types implementing SetSchema replace their generated schema;
//   - int64 loses its format, so clients use plain numbers;
//   - pointer fields are nullable, slices and maps are not.
func customizeSchema(_ string, t reflect.Type, _ reflect.StructTag, schema *openapi3.Schema) error {
	if hasSchemaOverride(t) {
		*schema = openapi3.Schema{}
		reflect.New(t).Interface().(openapi3gen.SetSchemar).SetSchema(schema)
		// Struct schemas without properties would not be exported as
		// components; an empty map is omitted from the JSON.
		if t.Kind() == reflect.Struct && schema.Properties == nil {
			schema.Properties = openapi3.Schemas{}
		}
		return nil
	}
	if schema.Format == "int64" {
		schema.Format = ""
	}
	schema.Nullable = false
	if t.Kind() == reflect.Struct {
		applyNullability(t, schema)
	}
	return nil
}

func hasSchemaOverride(t reflect.Type) bool {
	return reflect.PointerTo(t).Implements(setSchemaType)
}

// applyNullability derives property nullability from the Go field types.
func applyNullability(t reflect.Type, schema *openapi3.Schema) {
	for _, f := range reflect.VisibleFields(t) {
		if f.Anonymous || !f.IsExported() {
			continue
		}
		name := jsonName(f)
		prop := schema.Properties[name]
		if name == "-" || prop == nil {
			continue
		}
		ft := f.Type
		if k := ft.Kind(); (k == reflect.Slice || k == reflect.Map) && prop.Value != nil && !isComponentRef(prop) {
			if hasSchemaOverride(ft) {
				// fuego marks slices nullable after the override ran.
				var own openapi3.Schema
				reflect.New(ft).Interface().(openapi3gen.SetSchemar).SetSchema(&own)
				prop.Value.Type = own.Type
			} else {
				prop.Value.Type = withoutNull(prop.Value.Type)
			}
		}
		if ft.Kind() == reflect.Pointer {
			schema.Properties[name] = nullable(prop)
		}
	}
}

// isComponentRef: during generation, inline schemas also carry their Go type
// name in Ref; only component refs keep it.
func isComponentRef(ref *openapi3.SchemaRef) bool {
	return strings.HasPrefix(ref.Ref, schemaRefPrefix)
}

func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		return f.Name
	}
	return name
}

func withoutNull(ts *openapi3.Types) *openapi3.Types {
	if ts == nil || !ts.Includes(openapi3.TypeNull) {
		return ts
	}
	out := openapi3.Types(slices.DeleteFunc(slices.Clone(ts.Slice()), func(s string) bool { return s == openapi3.TypeNull }))
	return &out
}

// nullable makes a property accept null: a type union for inline schemas,
// anyOf [$ref, null] for component references.
func nullable(prop *openapi3.SchemaRef) *openapi3.SchemaRef {
	if isComponentRef(prop) {
		return &openapi3.SchemaRef{Value: &openapi3.Schema{AnyOf: openapi3.SchemaRefs{
			{Ref: prop.Ref, Value: prop.Value},
			{Value: &openapi3.Schema{Type: &openapi3.Types{openapi3.TypeNull}}},
		}}}
	}
	s := prop.Value
	if s == nil || s.Type == nil || s.Type.Includes(openapi3.TypeNull) {
		return prop
	}
	ts := append(slices.Clone(s.Type.Slice()), openapi3.TypeNull)
	s.Type = (*openapi3.Types)(&ts)
	if len(s.Enum) > 0 && !slices.Contains(s.Enum, any(nil)) {
		s.Enum = append(s.Enum, nil)
	}
	return prop
}
