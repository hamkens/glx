// Package config resolves the GitLab host and access token for glx.
//
// Resolution order (first hit wins):
//  1. GITLAB_TOKEN / GLX_TOKEN environment variables
//  2. glx's own config file (~/.config/glx/config.yml)
//  3. glab's config file (~/.config/glab-cli/config.yml) for the active host
//
// This lets glx work out-of-the-box for anyone who already uses glab, while
// still allowing a standalone setup.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DefaultHost is pinned to the internal instance; override via config or flag.
const DefaultHost = "gitlab.infr.zglbl.net"

// Config holds everything needed to talk to a GitLab instance.
type Config struct {
	Host  string `yaml:"host"`
	Token string `yaml:"token"`
}

// glabConfig mirrors the subset of glab's config file we read.
type glabConfig struct {
	Host  string `yaml:"host"`
	Hosts map[string]struct {
		Token   string `yaml:"token"`
		APIHost string `yaml:"api_host"`
	} `yaml:"hosts"`
}

// Load resolves a usable Config or returns an error describing what's missing.
// If host is empty, DefaultHost is used.
func Load(host string) (*Config, error) {
	if host == "" {
		host = DefaultHost
	}

	// 1. Environment.
	if tok := firstNonEmpty(os.Getenv("GLX_TOKEN"), os.Getenv("GITLAB_TOKEN")); tok != "" {
		return &Config{Host: host, Token: tok}, nil
	}

	// 2. glx's own config.
	if c, err := loadGlxFile(host); err == nil && c.Token != "" {
		return c, nil
	}

	// 3. glab's config, for the requested host.
	if tok, err := loadGlabToken(host); err == nil && tok != "" {
		return &Config{Host: host, Token: tok}, nil
	}

	return nil, fmt.Errorf("no token found for %s: set GITLAB_TOKEN, create ~/.config/glx/config.yml, or log in with glab", host)
}

func loadGlxFile(host string) (*Config, error) {
	path := filepath.Join(userConfigDir(), "glx", "config.yml")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Host == "" {
		c.Host = host
	}
	return &c, nil
}

func loadGlabToken(host string) (string, error) {
	path := filepath.Join(userConfigDir(), "glab-cli", "config.yml")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var gc glabConfig
	if err := yaml.Unmarshal(b, &gc); err != nil {
		return "", err
	}
	if h, ok := gc.Hosts[host]; ok {
		return h.Token, nil
	}
	return "", fmt.Errorf("host %s not present in glab config", host)
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
