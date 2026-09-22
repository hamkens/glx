// Command glx is an interactive terminal UI for GitLab merge requests and
// GitHub pull requests.
//
// The backend is chosen from the host name (see internal/config) and can be
// forced with --provider; everything above internal/forge is provider-neutral.
// Several hosts can be connected at once, in which case their changes appear in
// one merged list — see internal/forge.Fleet.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hamkens/glx/internal/config"
	"github.com/hamkens/glx/internal/forge"
	"github.com/hamkens/glx/internal/github"
	"github.com/hamkens/glx/internal/gitlab"
	"github.com/hamkens/glx/internal/tui"
)

// hostList collects repeated --host flags, so `glx --host a --host b` connects
// to both.
type hostList []string

func (h *hostList) String() string { return strings.Join(*h, ",") }

func (h *hostList) Set(v string) error {
	// Accept a comma-separated list too, which is friendlier in a shell alias.
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*h = append(*h, part)
		}
	}
	return nil
}

func main() {
	var hosts hostList
	flag.Var(&hosts, "host", "host to connect to; repeat for several (default: $GLX_HOST, the hosts: map in ~/.config/glx/config.yml, else "+config.DefaultHost+")")
	provider := flag.String("provider", "", "backend to use: gitlab or github (default: inferred from the host)")
	check := flag.Bool("check", false, "verify connectivity and exit (no TUI)")
	flag.Parse()

	if err := run(hosts, *provider, *check); err != nil {
		fmt.Fprintln(os.Stderr, "glx:", err)
		os.Exit(1)
	}
}

func run(hosts []string, providerFlag string, checkOnly bool) error {
	provider, err := config.ParseProvider(providerFlag)
	if err != nil {
		return err
	}

	cfgs, cfgErrs, err := config.LoadAll(hosts, provider)
	if err != nil {
		return err
	}
	// A host that resolved no token is reported but not fatal, so the hosts that
	// did resolve still open.
	for _, e := range cfgErrs {
		fmt.Fprintln(os.Stderr, "glx: skipping host:", e)
	}

	members := make([]forge.Forge, 0, len(cfgs))
	for _, cfg := range cfgs {
		c, err := newClient(cfg)
		if err != nil {
			return err
		}
		members = append(members, c)
	}
	fleet := forge.NewFleet(members...)

	// Resolve the authenticated user up front: it validates connectivity and
	// lets the UI distinguish "approved by me" from "approved by others". For a
	// fleet this succeeds as long as one host answers; the rest are marked
	// degraded and surfaced in the status bar.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	user, version, err := fleet.CurrentUser(ctx)
	if err != nil {
		return fmt.Errorf("connectivity check failed: %w", err)
	}

	if checkOnly {
		return printCheck(fleet, user, version)
	}

	for _, host := range fleet.Degraded() {
		fmt.Fprintf(os.Stderr, "glx: %s is unreachable; continuing without it\n", host)
	}
	return tui.Run(fleet)
}

// printCheck reports connectivity per host, so a fleet says which member is
// healthy rather than collapsing to one line.
func printCheck(fleet *forge.Fleet, user, version string) error {
	if m, ok := fleet.Single(); ok {
		// Both backends return a self-describing version ("GitLab 18.7.1-ee",
		// "GitHub Enterprise 3.12"), so the provider needs no separate label.
		fmt.Printf("✓ connected to %s (%s) as %s\n", m.Host(), version, user)
		return nil
	}
	health := fleet.Health()
	for _, m := range fleet.Members() {
		if err, bad := health[m.Host()]; bad {
			fmt.Printf("✘ %s (%s): %v\n", m.Host(), m.Provider(), err)
			continue
		}
		fmt.Printf("✓ connected to %s (%s) as %s\n", m.Host(), m.Provider(), m.Username())
	}
	return nil
}

// newClient builds the backend for the resolved provider.
func newClient(cfg *config.Config) (forge.Forge, error) {
	switch cfg.Provider {
	case forge.ProviderGitHub:
		return github.New(cfg.Host, cfg.Token)
	case forge.ProviderGitLab:
		return gitlab.New(cfg.Host, cfg.Token)
	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
}
