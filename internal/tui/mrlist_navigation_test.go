package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestListPageKeysMoveHalfPageInReviewsAndAuthored(t *testing.T) {
	for _, scopeIdx := range []int{0, 1} {
		m := mrListModel{scopeIdx: scopeIdx, height: 12, rows: selectableRows(20)}

		m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})
		if m.cur != 5 {
			t.Errorf("scope %d: page down cursor = %d, want 5", scopeIdx, m.cur)
		}

		m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyPgUp})
		if m.cur != 0 {
			t.Errorf("scope %d: page up cursor = %d, want 0", scopeIdx, m.cur)
		}
	}
}

func TestListPageKeysDoNotMoveAssigned(t *testing.T) {
	m := mrListModel{scopeIdx: 2, height: 12, rows: selectableRows(20)}

	m, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.cur != 0 {
		t.Errorf("assigned page down cursor = %d, want 0", m.cur)
	}
}

func selectableRows(n int) []mrRow {
	rows := make([]mrRow, n)
	for i := range rows {
		rows[i] = mrRow{}
	}
	return rows
}
