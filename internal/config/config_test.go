package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hamkens/glx/internal/forge"
)

func TestDetectProvider(t *testing.T) {
	cases := []struct {
		host string
		want forge.Provider
	}{
		{"github.com", forge.ProviderGitHub},
		{"https://github.com/", forge.ProviderGitHub},
		{"api.github.com", forge.ProviderGitHub},
		{"github.acme.com", forge.ProviderGitHub},
		{"acme.ghe.com", forge.ProviderGitHub},
		{"git.ghe.acme.net", forge.ProviderGitHub},
		{"gitlab.com", forge.ProviderGitLab},
		{"gitlab.infr.zglbl.net", forge.ProviderGitLab},
		// Self-hosted GitLab with an opaque name must not be misread as GitHub;
		// --provider is the escape hatch for that.
		{"scm.internal.example", forge.ProviderGitLab},
		{"", forge.ProviderGitLab},
	}
	for _, c := range cases {
		if got := DetectProvider(c.host); got != c.want {
			t.Errorf("DetectProvider(%q) = %q, want %q", c.host, got, c.want)
		}
	}
}

func TestParseProvider(t *testing.T) {
	for in, want := range map[string]forge.Provider{
		"":        "",
		"gitlab":  forge.ProviderGitLab,
		"GitHub":  forge.ProviderGitHub,
		" github": forge.ProviderGitHub,
	} {
		got, err := ParseProvider(in)
		if err != nil {
			t.Fatalf("ParseProvider(%q) errored: %v", in, err)
		}
		if got != want {
			t.Errorf("ParseProvider(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ParseProvider("bitbucket"); err == nil {
		t.Error("ParseProvider(bitbucket) should error")
	}
}

// isolateEnv points config lookups at a scratch config dir, clears every
// token/host variable, and stubs the gh-CLI fallback, so a test only sees what
// it sets itself even on a machine with a real gh login.
func isolateEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	for _, k := range []string{"GLX_TOKEN", "GLX_HOST", "GITLAB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN", "GH_HOST"} {
		t.Setenv(k, "")
	}
	stubGhCLI(t, "")
	return dir
}

// stubGhCLI replaces the `gh auth token` lookup for the duration of a test.
func stubGhCLI(t *testing.T, token string) {
	t.Helper()
	prev := ghCLIToken
	ghCLIToken = func(string) string { return token }
	t.Cleanup(func() { ghCLIToken = prev })
}

func writeConfig(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDefaultsToGitLabHost(t *testing.T) {
	isolateEnv(t)
	t.Setenv("GITLAB_TOKEN", "glpat-x")

	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != DefaultHost || cfg.Provider != forge.ProviderGitLab || cfg.Token != "glpat-x" {
		t.Fatalf("got %+v", cfg)
	}
}

// A GitHub host must not pick up GITLAB_TOKEN, and vice versa: the vendor
// variables are provider-scoped so having both set doesn't cross the wires.
func TestLoadKeepsVendorTokensProviderScoped(t *testing.T) {
	isolateEnv(t)
	t.Setenv("GITLAB_TOKEN", "glpat-x")
	t.Setenv("GH_TOKEN", "ghp-y")

	gh, err := Load("github.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if gh.Provider != forge.ProviderGitHub || gh.Token != "ghp-y" {
		t.Errorf("github: got %+v", gh)
	}

	gl, err := Load("gitlab.example.net", "")
	if err != nil {
		t.Fatal(err)
	}
	if gl.Provider != forge.ProviderGitLab || gl.Token != "glpat-x" {
		t.Errorf("gitlab: got %+v", gl)
	}
}

// --provider overrides detection, which is the whole point of the flag: a
// GitHub Enterprise host named nothing like GitHub still resolves to GitHub.
func TestLoadProviderOverrideBeatsDetection(t *testing.T) {
	isolateEnv(t)
	t.Setenv("GH_TOKEN", "ghp-y")

	cfg, err := Load("scm.internal.example", forge.ProviderGitHub)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != forge.ProviderGitHub || cfg.Token != "ghp-y" {
		t.Fatalf("got %+v", cfg)
	}
}

func TestLoadGitHubDefaultHost(t *testing.T) {
	isolateEnv(t)
	t.Setenv("GITHUB_TOKEN", "ghp-z")

	cfg, err := Load("", forge.ProviderGitHub)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != DefaultGitHubHost {
		t.Fatalf("host = %q, want %q", cfg.Host, DefaultGitHubHost)
	}
}

func TestLoadReadsGhHostsFile(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "gh/hosts.yml", `github.com:
    user: someone
    oauth_token: gho_fromfile
    git_protocol: ssh
`)

	cfg, err := Load("github.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "gho_fromfile" {
		t.Fatalf("token = %q", cfg.Token)
	}
}

// gh ≥ 2.40 keeps the token only under the active user, so the host-level
// oauth_token can be absent entirely.
func TestLoadReadsGhHostsUsersSection(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "gh/hosts.yml", `ghe.acme.com:
    user: someone
    users:
        someone:
            oauth_token: gho_user
`)

	cfg, err := Load("ghe.acme.com", forge.ProviderGitHub)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "gho_user" {
		t.Fatalf("token = %q", cfg.Token)
	}
}

// hosts.yml is often tokenless because gh keeps credentials in the OS keyring;
// the `gh auth token` fallback is what makes "reuses your gh login" true there.
func TestLoadFallsBackToGhCLIToken(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "gh/hosts.yml", `github.com:
    user: someone
    users:
        someone:
`)
	stubGhCLI(t, "gho_fromkeyring")

	cfg, err := Load("github.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "gho_fromkeyring" {
		t.Fatalf("token = %q", cfg.Token)
	}
}

// A token written in hosts.yml wins over the subprocess, so the common case
// costs no exec.
func TestLoadPrefersGhHostsFileOverCLI(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "gh/hosts.yml", `github.com:
    oauth_token: gho_fromfile
`)
	stubGhCLI(t, "gho_fromkeyring")

	cfg, err := Load("github.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "gho_fromfile" {
		t.Fatalf("token = %q, want the hosts.yml value", cfg.Token)
	}
}

func TestLoadReadsGlabConfig(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "glab-cli/config.yml", `host: gitlab.example.net
hosts:
    gitlab.example.net:
        token: glpat-fromglab
`)

	cfg, err := Load("gitlab.example.net", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "glpat-fromglab" {
		t.Fatalf("token = %q", cfg.Token)
	}
}

func TestLoadGlxHostsMapWinsOverTopLevel(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "glx/config.yml", `host: gitlab.example.net
token: glpat-toplevel
hosts:
    github.com:
        token: ghp-perhost
`)

	gh, err := Load("github.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if gh.Token != "ghp-perhost" {
		t.Errorf("github token = %q, want the per-host entry", gh.Token)
	}

	gl, err := Load("gitlab.example.net", "")
	if err != nil {
		t.Fatal(err)
	}
	if gl.Token != "glpat-toplevel" {
		t.Errorf("gitlab token = %q, want the top-level entry", gl.Token)
	}
}

// The top-level token in glx's config names one host; it must not leak to a
// different host that happens to be asked for.
func TestLoadGlxTopLevelTokenIsHostScoped(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "glx/config.yml", `host: gitlab.example.net
token: glpat-toplevel
`)

	if _, err := Load("other.example.net", ""); err == nil {
		t.Fatal("expected an error for a host the config doesn't cover")
	}
}

func TestLoadErrorMentionsTheRightCLI(t *testing.T) {
	isolateEnv(t)

	_, err := Load("github.com", "")
	if err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("github error = %v, want it to mention gh auth login", err)
	}
	_, err = Load("gitlab.example.net", "")
	if err == nil || !strings.Contains(err.Error(), "glab auth login") {
		t.Errorf("gitlab error = %v, want it to mention glab auth login", err)
	}
}

// The hosts: map is what makes one glx session show both products, so an
// unflagged launch must connect to every host it lists.
func TestLoadAllUsesEveryConfiguredHost(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "glx/config.yml", `hosts:
    github.com:
        token: ghp-a
    gitlab.example.net:
        token: glpat-b
`)

	cfgs, errs, err := LoadAll(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none", errs)
	}
	if len(cfgs) != 2 {
		t.Fatalf("got %d configs, want 2", len(cfgs))
	}
	// Sorted host order keeps the fleet's member order stable between runs.
	if cfgs[0].Host != "github.com" || cfgs[0].Provider != forge.ProviderGitHub {
		t.Errorf("cfgs[0] = %+v", cfgs[0])
	}
	if cfgs[1].Host != "gitlab.example.net" || cfgs[1].Provider != forge.ProviderGitLab {
		t.Errorf("cfgs[1] = %+v", cfgs[1])
	}
}

// A per-host provider key is the escape hatch for a GHE install whose name
// reveals nothing.
func TestLoadAllHonoursPerHostProvider(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "glx/config.yml", `hosts:
    scm.internal.example:
        token: ghp-a
        provider: github
`)

	cfgs, _, err := LoadAll(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 || cfgs[0].Provider != forge.ProviderGitHub {
		t.Fatalf("got %+v, want the pinned github provider", cfgs)
	}
}

// Explicit --host flags override the config file entirely, so a single-host run
// stays possible without editing config.
func TestLoadAllExplicitHostsWin(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "glx/config.yml", `hosts:
    github.com:
        token: ghp-a
    gitlab.example.net:
        token: glpat-b
`)

	cfgs, _, err := LoadAll([]string{"gitlab.example.net"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 || cfgs[0].Host != "gitlab.example.net" {
		t.Fatalf("got %+v, want only the flagged host", cfgs)
	}
}

// One unusable entry must not stop the others from loading; it is reported so
// the user knows why a host is missing.
func TestLoadAllReportsBadHostButKeepsGoing(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "glx/config.yml", `hosts:
    github.com:
        token: ghp-a
`)

	cfgs, errs, err := LoadAll([]string{"github.com", "tokenless.example.net"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 || cfgs[0].Host != "github.com" {
		t.Fatalf("cfgs = %+v, want just the usable host", cfgs)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "tokenless.example.net") {
		t.Errorf("errs = %v, want one naming the skipped host", errs)
	}
}

// Nothing configured at all must keep the original single-host behaviour.
func TestLoadAllFallsBackToSingleHost(t *testing.T) {
	isolateEnv(t)
	t.Setenv("GITLAB_TOKEN", "glpat-x")

	cfgs, _, err := LoadAll(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 || cfgs[0].Host != DefaultHost {
		t.Fatalf("got %+v, want the default single host", cfgs)
	}
}

// Duplicate --host flags shouldn't produce two connections to one host.
func TestLoadAllDeduplicatesHosts(t *testing.T) {
	isolateEnv(t)
	t.Setenv("GH_TOKEN", "ghp-a")

	cfgs, _, err := LoadAll([]string{"github.com", "https://github.com/"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("got %d configs, want 1 after dedup", len(cfgs))
	}
}

// GLX_HOST names one host, so it must not be widened into the whole config map.
func TestLoadAllEnvHostWinsOverConfigMap(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "glx/config.yml", `hosts:
    github.com:
        token: ghp-a
    gitlab.example.net:
        token: glpat-b
`)
	t.Setenv("GLX_HOST", "github.com")

	cfgs, _, err := LoadAll(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 || cfgs[0].Host != "github.com" {
		t.Fatalf("got %+v, want only GLX_HOST", cfgs)
	}
}

// Listing a host with no token of its own must still select it: the token can
// come from an existing glab or gh login, which is the whole reason those
// fallbacks exist.
func TestLoadAllSelectsTokenlessConfiguredHosts(t *testing.T) {
	dir := isolateEnv(t)
	writeConfig(t, dir, "glx/config.yml", `hosts:
    github.com:
    gitlab.example.net:
`)
	writeConfig(t, dir, "gh/hosts.yml", `github.com:
    oauth_token: gho_fromgh
`)
	writeConfig(t, dir, "glab-cli/config.yml", `hosts:
    gitlab.example.net:
        token: glpat-fromglab
`)

	cfgs, errs, err := LoadAll(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none", errs)
	}
	if len(cfgs) != 2 {
		t.Fatalf("got %d configs, want 2", len(cfgs))
	}
	if cfgs[0].Token != "gho_fromgh" {
		t.Errorf("github token = %q, want the gh login", cfgs[0].Token)
	}
	if cfgs[1].Token != "glpat-fromglab" {
		t.Errorf("gitlab token = %q, want the glab login", cfgs[1].Token)
	}
}
