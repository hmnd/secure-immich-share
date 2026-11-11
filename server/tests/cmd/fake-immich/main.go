// Command fake-immich serves the test fake Immich with the standard
// fixtures (generated with ffmpeg and exiftool at startup), for e2e runs
// and local development without an Immich server.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	fake "github.com/hmnd/secure-immich-share/server/tests/fakes/immich"
)

type tb struct{}

func (tb) Helper() {}

func (tb) Fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fake-immich: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:2283", "listen address")
	flag.Parse()
	f := fake.NewUnstarted()
	f.Seed(tb{})
	log.Printf("fake-immich on http://%s: public album /share/%s (slug %s), password album /share/%s (password %q)",
		*addr, fake.PublicKey, fake.PublicSlug, fake.PasswordKey, fake.SharePassword)
	srv := &http.Server{Addr: *addr, Handler: f.Handler(), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
