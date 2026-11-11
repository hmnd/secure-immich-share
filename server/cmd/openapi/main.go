// Command openapi writes the generated API contract (api/openapi.json).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hmnd/secure-immich-share/server/internal/httpapi"
)

func main() {
	out := flag.String("o", "../api/openapi.json", "output path")
	flag.Parse()
	spec, err := httpapi.NewServer(httpapi.Deps{}).OpenAPISpec()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	//nolint:gosec // a committed source file
	if err := os.WriteFile(*out, spec, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
