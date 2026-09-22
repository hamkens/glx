package forge

// Vocabulary holds the provider's words for shared concepts, so views can
// render "merge request" or "pull request" (and "!42" or "#42") without
// branching on the provider themselves.
type Vocabulary struct {
	// Change is the singular noun ("merge request" / "pull request").
	Change string
	// Changes is its plural.
	Changes string
	// ChangeAbbrev is the short form used in tight spaces ("MR" / "PR").
	ChangeAbbrev string
	// IDPrefix is the sigil before a change's number ("!" / "#").
	IDPrefix string
	// Pipeline is the CI noun ("pipeline" / "workflow run").
	Pipeline string
	// UpdateBranch names the bring-up-to-date action ("rebase" / "update
	// branch"), used for both key hints and status messages.
	UpdateBranch string
	// UpdateBranchGerund is that action in progress ("rebasing" / "updating
	// branch"), for transient flash messages.
	UpdateBranchGerund string
	// UpdateBranchDone is its completion phrasing ("rebase started" /
	// "branch updated").
	UpdateBranchDone string
	// MergeQueue names the queued-merge concept ("merge train" / "merge queue").
	MergeQueue string
	// Approve / Unapprove name the approval actions. GitHub's withdrawal is a
	// review dismissal rather than a symmetric un-approve.
	Approve   string
	Unapprove string
	// Thread / Threads name comment threads ("thread" / "conversation").
	Thread  string
	Threads string
}

var gitlabVocabulary = Vocabulary{
	Change:             "merge request",
	Changes:            "merge requests",
	ChangeAbbrev:       "MR",
	IDPrefix:           "!",
	Pipeline:           "pipeline",
	UpdateBranch:       "rebase",
	UpdateBranchGerund: "rebasing",
	UpdateBranchDone:   "rebase started",
	MergeQueue:         "merge train",
	Approve:            "approve",
	Unapprove:          "unapprove",
	Thread:             "thread",
	Threads:            "threads",
}

var githubVocabulary = Vocabulary{
	Change:             "pull request",
	Changes:            "pull requests",
	ChangeAbbrev:       "PR",
	IDPrefix:           "#",
	Pipeline:           "workflow run",
	UpdateBranch:       "update branch",
	UpdateBranchGerund: "updating branch",
	UpdateBranchDone:   "branch update queued",
	MergeQueue:         "merge queue",
	Approve:            "approve",
	Unapprove:          "dismiss approval",
	Thread:             "conversation",
	Threads:            "conversations",
}

// mixedVocabulary is used when one list spans both products, where naming
// either vendor's concept would be wrong for half the rows. It deliberately
// says "change" and "CI" — the shared concepts — and leaves the vendor-specific
// spellings to per-row rendering. The sigil is empty because each row carries
// its own ("!" or "#").
var mixedVocabulary = Vocabulary{
	Change:             "change",
	Changes:            "changes",
	ChangeAbbrev:       "change",
	IDPrefix:           "",
	Pipeline:           "CI",
	UpdateBranch:       "update branch",
	UpdateBranchGerund: "updating branch",
	UpdateBranchDone:   "branch update started",
	MergeQueue:         "merge queue",
	Approve:            "approve",
	Unapprove:          "unapprove",
	Thread:             "thread",
	Threads:            "threads",
}

// Vocab returns the wording for a provider. Unknown providers fall back to
// GitLab's, which is glx's original vocabulary.
func Vocab(p Provider) Vocabulary {
	switch p {
	case ProviderGitHub:
		return githubVocabulary
	case ProviderMixed:
		return mixedVocabulary
	default:
		return gitlabVocabulary
	}
}
