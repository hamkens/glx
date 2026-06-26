// Command glx is an interactive terminal UI for GitLab.
//
// Phase 0: resolve config/auth and verify connectivity to the instance.
// Subsequent phases add the merge-request TUI on top of this foundation.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/hamkens/glx/internal/config"
	"github.com/hamkens/glx/internal/gitlab"
	"github.com/hamkens/glx/internal/tui"
)

func main() {
	host := flag.String("host", "", "GitLab host (default: configured or gitlab.infr.zglbl.net)")
	check := flag.Bool("check", false, "verify connectivity and exit (no TUI)")
	flag.Parse()

	if err := run(*host, *check); err != nil {
		fmt.Fprintln(os.Stderr, "glx:", err)
		os.Exit(1)
	}
}

func run(host string, checkOnly bool) error {
	cfg, err := config.Load(host)
	if err != nil {
		return err
	}

	client, err := gitlab.New(cfg.Host, cfg.Token)
	if err != nil {
		return err
	}

	// Resolve the authenticated user up front: it validates connectivity and
	// lets the UI distinguish "approved by me" from "approved by others".
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	user, version, err := client.CurrentUser(ctx)
	if err != nil {
		return fmt.Errorf("connectivity check failed: %w", err)
	}

	if checkOnly {
		fmt.Printf("✓ connected to %s (GitLab %s) as %s\n", cfg.Host, version, user)
		return nil
	}

	return tui.Run(client)
}
