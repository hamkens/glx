// Package config resolves the host, provider and access token for glx.
//
// The provider is normally inferred from the host name (github.com and GitHub
// Enterprise hosts → GitHub, everything else → GitLab) and can be forced with
// the --provider flag for hosts whose names don't reveal which product they
// run.
//
// Token resolution order (first hit wins), per provider:
//
//	GitLab: GLX_TOKEN / GITLAB_TOKEN → ~/.config/glx/config.yml
//	        → ~/.config/glab-cli/config.yml (reuses a glab login)
//	GitHub: GLX_TOKEN / GH_TOKEN / GITHUB_TOKEN → ~/.config/glx/config.yml
//	        → ~/.config/gh/hosts.yml → `gh auth token` (reuses a gh login,
//	        including the keyring gh stores tokens in by default)
//
// This lets glx work out-of-the-box for anyone who already uses glab or gh,
// while still allowing a standalone setup.
package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hamkens/glx/internal/forge"
)

// DefaultHost is the GitLab instance used when nothing else selects a host.
const DefaultHost = "gitlab.infr.zglbl.net"

// DefaultGitHubHost is used when GitHub is selected without naming a host.
const DefaultGitHubHost = "github.com"

// Config holds everything needed to talk to one host.
type Config struct {
	Host     string         `yaml:"host"`
	Token    string         `yaml:"token"`
	Provider forge.Provider `yaml:"provider"`
}

// glxFile is glx's own config file. The top-level host/token keys describe a
// single host (the original format); the hosts map lets one file hold
// credentials for several, keyed by host name.
type glxFile struct {
	Host  string               `yaml:"host"`
	Token string               `yaml:"token"`
	Hosts map[string]hostEntry `yaml:"hosts"`
}

type hostEntry struct {
	Token string `yaml:"token"`
	// Provider forces the backend for this host, for GitHub Enterprise or
	// GitLab installs whose names don't reveal the product.
	Provider string `yaml:"provider"`
}

// glabConfig mirrors the subset of glab's config file we read.
type glabConfig struct {
	Host  string `yaml:"host"`
	Hosts map[string]struct {
		Token   string `yaml:"token"`
		APIHost string `yaml:"api_host"`
	} `yaml:"hosts"`
}

// ghHosts mirrors the subset of gh's hosts.yml we read. gh writes the token at
// the host level and, since v2.40, again under each logged-in user.
type ghHosts map[string]struct {
	OAuthToken string `yaml:"oauth_token"`
	User       string `yaml:"user"`
	Users      map[string]struct {
		OAuthToken string `yaml:"oauth_token"`
	} `yaml:"users"`
}

// DetectProvider infers the provider from a host name. GitHub is recognized by
// github.com itself, by GitHub Enterprise Cloud's ".ghe.com" naming, and by the
// common "github.<company>.com" self-hosted convention. Everything else is
// assumed to be GitLab, which preserves glx's behaviour for self-hosted GitLab
// instances with arbitrary names; use --provider to correct a wrong guess.
func DetectProvider(host string) forge.Provider {
	h := strings.ToLower(normalizeHost(host))
	switch {
	case h == "github.com", h == "api.github.com",
		strings.HasSuffix(h, ".github.com"),
		strings.HasPrefix(h, "github."),
		strings.Contains(h, ".ghe."),
		strings.HasSuffix(h, ".ghe.com"):
		return forge.ProviderGitHub
	default:
		return forge.ProviderGitLab
	}
}

// ParseProvider validates a --provider flag value. An empty string means
// "auto-detect from the host".
func ParseProvider(s string) (forge.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", nil
	case "gitlab":
		return forge.ProviderGitLab, nil
	case "github":
		return forge.ProviderGitHub, nil
	default:
		return "", fmt.Errorf("unknown provider %q (want gitlab or github)", s)
	}
}

// Load resolves a usable Config or returns an error describing what's missing.
//
// host may be empty, in which case it comes from GLX_HOST and then the
// provider's default. provider may be empty to auto-detect from the host; a
// non-empty value wins over detection, which is how --provider forces a backend
// for a host whose name doesn't reveal it.
func Load(host string, provider forge.Provider) (*Config, error) {
	host = normalizeHost(host)
	if host == "" {
		host = normalizeHost(os.Getenv("GLX_HOST"))
	}
	if host == "" {
		if provider == forge.ProviderGitHub {
			host = normalizeHost(os.Getenv("GH_HOST"))
			if host == "" {
				host = DefaultGitHubHost
			}
		} else {
			host = DefaultHost
		}
	}
	if provider == "" {
		provider = DetectProvider(host)
	}

	// 1. Environment. GLX_TOKEN applies to whichever provider is active; the
	//    vendor variables only apply to their own.
	env := []string{os.Getenv("GLX_TOKEN")}
	if provider == forge.ProviderGitHub {
		env = append(env, os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN"))
	} else {
		env = append(env, os.Getenv("GITLAB_TOKEN"))
	}
	if tok := firstNonEmpty(env...); tok != "" {
		return &Config{Host: host, Token: tok, Provider: provider}, nil
	}

	// 2. glx's own config.
	if tok := loadGlxToken(host); tok != "" {
		return &Config{Host: host, Token: tok, Provider: provider}, nil
	}

	// 3. The vendor CLI's config, for this host.
	if provider == forge.ProviderGitHub {
		if tok := loadGhToken(host); tok != "" {
			return &Config{Host: host, Token: tok, Provider: provider}, nil
		}
		// 4. gh stores tokens in the system keyring by default, in which case
		//    hosts.yml holds no token at all — ask gh itself for it.
		if tok := ghCLIToken(host); tok != "" {
			return &Config{Host: host, Token: tok, Provider: provider}, nil
		}
		return nil, fmt.Errorf("no token found for %s: set GH_TOKEN, add it to ~/.config/glx/config.yml, or log in with `gh auth login`", host)
	}
	if tok := loadGlabToken(host); tok != "" {
		return &Config{Host: host, Token: tok, Provider: provider}, nil
	}
	return nil, fmt.Errorf("no token found for %s: set GITLAB_TOKEN, add it to ~/.config/glx/config.yml, or log in with `glab auth login`", host)
}

// LoadAll resolves one Config per host glx should connect to.
//
// Explicit hosts (repeated --host flags, or GLX_HOST) win outright. Otherwise
// every host under `hosts:` in glx's own config file is used, which is how one
// glx session shows GitLab and GitHub side by side. With neither, it falls back
// to the single-host behaviour of Load.
//
// A host whose token can't be resolved is reported in errs rather than failing
// the whole call, so one stale entry doesn't stop the others from loading; only
// an empty result is an error.
func LoadAll(hosts []string, provider forge.Provider) (cfgs []*Config, errs []error, err error) {
	if len(hosts) == 0 {
		if env := normalizeHost(os.Getenv("GLX_HOST")); env != "" {
			hosts = []string{env}
		}
	}
	if len(hosts) == 0 && provider == "" {
		hosts = configuredHosts()
	}
	if len(hosts) == 0 {
		cfg, err := Load("", provider)
		if err != nil {
			return nil, nil, err
		}
		return []*Config{cfg}, nil, nil
	}

	seen := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		h = normalizeHost(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		// An explicit --provider applies to every host named on that command
		// line; per-host config entries can still pin their own.
		p := provider
		if p == "" {
			p = configuredProvider(h)
		}
		cfg, cfgErr := Load(h, p)
		if cfgErr != nil {
			errs = append(errs, cfgErr)
			continue
		}
		cfgs = append(cfgs, cfg)
	}
	if len(cfgs) == 0 {
		if len(errs) == 1 {
			return nil, errs, errs[0]
		}
		return nil, errs, fmt.Errorf("no host could be configured: %s", joinErrors(errs))
	}
	return cfgs, errs, nil
}

// configuredHosts lists the hosts under `hosts:` in glx's config, plus the
// top-level host when it names one, in sorted order so the fleet's member order
// is stable between runs.
//
// Listing a host is enough to select it; its entry needs no token, because the
// token may well come from an existing glab or gh login further down the
// resolution order. Load reports the ones that resolve to nothing.
func configuredHosts() []string {
	f, ok := readGlxFile()
	if !ok {
		return nil
	}
	seen := make(map[string]bool, len(f.Hosts)+1)
	var out []string
	for h := range f.Hosts {
		h = normalizeHost(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Strings(out)
	// The single-host form still counts as a configured host, so upgrading to a
	// hosts: map is additive rather than a migration.
	if h := normalizeHost(f.Host); h != "" && f.Token != "" && !seen[h] {
		out = append(out, h)
	}
	return out
}

// configuredProvider returns the provider pinned for a host in glx's config, or
// "" to auto-detect.
func configuredProvider(host string) forge.Provider {
	f, ok := readGlxFile()
	if !ok {
		return ""
	}
	e, ok := f.Hosts[host]
	if !ok {
		return ""
	}
	p, err := ParseProvider(e.Provider)
	if err != nil {
		return ""
	}
	return p
}

func readGlxFile() (glxFile, bool) {
	b, err := os.ReadFile(filepath.Join(userConfigDir(), "glx", "config.yml"))
	if err != nil {
		return glxFile{}, false
	}
	var f glxFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return glxFile{}, false
	}
	return f, true
}

func joinErrors(errs []error) string {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	return strings.Join(msgs, "; ")
}

// loadGlxToken reads glx's own config file, preferring an entry under `hosts:`
// for this exact host over the single-host top-level keys.
func loadGlxToken(host string) string {
	f, ok := readGlxFile()
	if !ok {
		return ""
	}
	if e, ok := f.Hosts[host]; ok && e.Token != "" {
		return e.Token
	}
	// The top-level token only applies when the file names this host — or names
	// no host at all, which means "whichever host glx was asked for".
	if f.Token != "" && (f.Host == "" || normalizeHost(f.Host) == host) {
		return f.Token
	}
	return ""
}

func loadGlabToken(host string) string {
	b, err := os.ReadFile(filepath.Join(userConfigDir(), "glab-cli", "config.yml"))
	if err != nil {
		return ""
	}
	var gc glabConfig
	if err := yaml.Unmarshal(b, &gc); err != nil {
		return ""
	}
	return gc.Hosts[host].Token
}

// loadGhToken reads a token out of gh's hosts.yml for the given host.
func loadGhToken(host string) string {
	b, err := os.ReadFile(filepath.Join(userConfigDir(), "gh", "hosts.yml"))
	if err != nil {
		return ""
	}
	var hosts ghHosts
	if err := yaml.Unmarshal(b, &hosts); err != nil {
		return ""
	}
	h, ok := hosts[host]
	if !ok {
		return ""
	}
	if h.OAuthToken != "" {
		return h.OAuthToken
	}
	// Newer gh versions store the token only under the active user.
	if u, ok := h.Users[h.User]; ok && u.OAuthToken != "" {
		return u.OAuthToken
	}
	return ""
}

// ghCLIToken is the keyring lookup, indirected through a variable so tests can
// stub out the subprocess and stay hermetic on machines with a real gh login.
var ghCLIToken = ghKeyringToken

// ghKeyringToken asks the gh CLI for its stored token. gh keeps credentials in
// the OS keyring unless GH_CONFIG_DIR-based insecure storage was chosen, so for
// most `gh auth login` users this is the only place the token exists. Absent or
// unauthenticated gh yields "", which callers treat as "no token here".
func ghKeyringToken(host string) string {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, gh, "auth", "token", "--hostname", host).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// normalizeHost strips a scheme, trailing slash and surrounding space so
// "https://github.com/" and "github.com" resolve identically.
func normalizeHost(host string) string {
	h := strings.TrimSpace(host)
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	return strings.TrimSuffix(h, "/")
}

// userConfigDir returns $XDG_CONFIG_HOME or ~/.config.
func userConfigDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
