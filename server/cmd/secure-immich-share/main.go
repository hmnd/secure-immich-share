// Command secure-immich-share serves Immich shared links to the public with
// every photo and video stripped of identifying metadata.
//
//	secure-immich-share serve
//	secure-immich-share mint <immich share URL | key | slug>
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hmnd/secure-immich-share/server/internal/app"
	"github.com/hmnd/secure-immich-share/server/internal/config"
	"github.com/hmnd/secure-immich-share/server/internal/immich"
	"github.com/hmnd/secure-immich-share/server/internal/linkref"
)

const usage = "usage: secure-immich-share serve | mint <immich share URL | key | slug>"

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "secure-immich-share: config: %v\n", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "serve":
		err = serve(ctx, cfg)
	case "mint":
		if len(args) != 1 {
			err = fmt.Errorf("%s", usage)
			break
		}
		err = mint(ctx, cfg, args[0])
	default:
		err = fmt.Errorf("%s", usage)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "secure-immich-share: %v\n", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, cfg config.Config) error {
	a, err := app.New(ctx, cfg, app.Options{})
	if err != nil {
		return err
	}
	return a.Run(ctx)
}

// mint prints an encrypted /t/ link for an Immich share with the active LINK_KEYS key.
func mint(ctx context.Context, cfg config.Config, input string) error {
	if !cfg.TokenLinks() {
		return errors.New("mint: LINK_KEYS is not set")
	}
	target, err := linkref.ParseShareInput(input)
	if err != nil {
		return fmt.Errorf("mint: %w", err)
	}
	ring, err := linkref.NewKeyring(cfg.LinkKeys)
	if err != nil {
		return fmt.Errorf("mint: %w", err)
	}
	link, err := immich.New(cfg.ImmichURL, nil).GetMine(ctx, immich.Auth{Target: target})
	switch {
	case errors.Is(err, immich.ErrPasswordRequired):
		fmt.Fprintln(os.Stderr, "note: the share has a password; visitors will need it")
	case errors.Is(err, immich.ErrGone):
		return errors.New("mint: Immich has no such share (or it expired)")
	case err != nil:
		return fmt.Errorf("mint: %w", err)
	default:
		if !link.AllowDownload || !link.ShowMetadata {
			fmt.Fprintln(os.Stderr, "warning: turn on \"Allow download\" and \"Show metadata\" for this share in Immich;"+
				" without them only Immich's previews can be served")
		}
		if link.Password != nil && *link.Password != "" {
			fmt.Fprintln(os.Stderr, "note: the share has a password; visitors will need it")
		}
	}
	token, err := ring.Mint(target)
	if err != nil {
		return fmt.Errorf("mint: %w", err)
	}
	fmt.Println(cfg.PublicURL + "/t/" + token)
	return nil
}
