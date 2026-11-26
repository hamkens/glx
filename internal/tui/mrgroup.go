package tui

import (
	"fmt"
	"sort"
	"time"

	"github.com/hamkens/glx/internal/forge"
)

type statusGroup int

const (
	groupNeedsReview statusGroup = iota
	groupAwaitingAuthor
	groupApprovedByMe
	groupReviewOther
	groupReady
	groupNeedsFix
	groupWaiting
	groupDrafts
)

func (g statusGroup) label() string {
	switch g {
	case groupNeedsReview:
		return "Needs your review"
	case groupAwaitingAuthor:
		return "Waiting on author"
	case groupApprovedByMe:
		return "Approved by you"
	case groupReviewOther:
		return "Other"
	case groupReady:
		return "Ready to merge"
	case groupNeedsFix:
		return "Needs your fix"
	case groupWaiting:
		return "Waiting on others"
	default:
		return "Drafts"
	}
}

var (
	reviewerGroupOrder = []statusGroup{groupNeedsReview, groupAwaitingAuthor, groupApprovedByMe, groupReviewOther, groupDrafts}
	authorGroupOrder   = []statusGroup{groupReady, groupNeedsFix, groupWaiting, groupDrafts}
)

func statusGroupOrder(scope forge.Scope) []statusGroup {
	if scope == forge.ScopeReviewer {
		return reviewerGroupOrder
	}
	return authorGroupOrder
}

func classifyChange(scope forge.Scope, mr forge.Change) statusGroup {
	if mr.Draft {
		return groupDrafts
	}
	if scope == forge.ScopeReviewer {
		switch {
		case mr.ReviewState.Pending():
			return groupNeedsReview
		case mr.ReviewState.FeedbackGiven():
			return groupAwaitingAuthor
		case mr.ApprovedByMe:
			return groupApprovedByMe
		default:
			return groupReviewOther
		}
	}
	if readyToMerge(mr) {
		return groupReady
	}
	if needsAuthorFix(mr) {
		return groupNeedsFix
	}
	return groupWaiting
}

func needsAuthorFix(mr forge.Change) bool {
	if mr.Conflicts || mr.Pipeline == forge.StatusFailed {
		return true
	}
	switch mr.MergeState {
	case forge.MergeStateConflict, forge.MergeStateCIFailed, forge.MergeStateNeedsUpdate,
		forge.MergeStateChangesRequested, forge.MergeStateThreadsUnresolved:
		return true
	}
	return false
}

func (m *mrListModel) appendStatusGrouped(mrs []forge.Change) {
	scope := m.scope()
	groups := map[statusGroup][]forge.Change{}
	for _, mr := range mrs {
		g := classifyChange(scope, mr)
		groups[g] = append(groups[g], mr)
	}
	sortOldestFirst(groups[groupNeedsReview])

	first := true
	for _, g := range statusGroupOrder(scope) {
		members := groups[g]
		if len(members) == 0 {
			continue
		}
		if !first {
			m.rows = append(m.rows, mrRow{isSpacer: true})
		}
		first = false
		m.rows = append(m.rows, mrRow{isHeader: true, group: fmt.Sprintf("%s · %d", g.label(), len(members))})
		m.appendRepoGroups(members, true)
	}
}

func sortOldestFirst(mrs []forge.Change) {
	updated := func(mr forge.Change) time.Time {
		t, err := time.Parse(time.RFC3339, mr.UpdatedAt)
		if err != nil {
			return time.Time{}
		}
		return t
	}
	sort.SliceStable(mrs, func(i, j int) bool { return updated(mrs[i]).Before(updated(mrs[j])) })
}
