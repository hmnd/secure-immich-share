package httpapi

import (
	"bytes"
	"os"
	"testing"
)

// TestOpenAPISpecUpToDate fails when api/openapi.json is stale; run
// `just gen-api` after changing routes or contract types.
func TestOpenAPISpecUpToDate(t *testing.T) {
	got, err := NewServer(Deps{}).OpenAPISpec()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../../api/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("api/openapi.json is out of date: run `just gen-api`")
	}
}
