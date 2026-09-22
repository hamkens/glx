package tui

import "github.com/hamkens/glx/internal/forge"

// The views below the list always work on one repo, which belongs to exactly one
// host — so they resolve that host's wording and capabilities rather than the
// fleet-wide neutral ones. These helpers keep the "is this a fleet?" question in
// a single place instead of in every constructor.

// vocabForRepo returns the vocabulary of whichever host serves repo, falling
// back to the client's own when it isn't a fleet or the repo is unknown.
func vocabForRepo(client forge.Forge, repo string) forge.Vocabulary {
	if m, ok := memberForRepo(client, repo); ok {
		return forge.Vocab(m.Provider())
	}
	return forge.Vocab(client.Provider())
}

// capsForRepo returns the capabilities of whichever host serves repo.
func capsForRepo(client forge.Forge, repo string) forge.Capabilities {
	if m, ok := memberForRepo(client, repo); ok {
		return m.Capabilities()
	}
	return client.Capabilities()
}

// memberForRepo resolves the fleet member owning repo. A non-fleet client, or a
// repo no host has served yet, reports false so callers use their own defaults.
func memberForRepo(client forge.Forge, repo string) (forge.Forge, bool) {
	f, ok := client.(*forge.Fleet)
	if !ok {
		return nil, false
	}
	m, err := f.ForRepo(repo)
	if err != nil {
		return nil, false
	}
	return m, true
}
