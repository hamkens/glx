package forge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type paginatedForge struct {
	fakeForge
	pages     map[string]*ChangePage
	requested []string
}

func (f *paginatedForge) Changes(_ context.Context, _ Scope, cursor string, _ int) (*ChangePage, error) {
	f.requested = append(f.requested, cursor)
	if f.err != nil {
		return nil, f.err
	}
	page, ok := f.pages[cursor]
	if !ok {
		return nil, errors.New("unexpected cursor: " + cursor)
	}
	copyPage := *page
	copyPage.Changes = append([]Change(nil), page.Changes...)
	return &copyPage, nil
}

func TestFleetPaginationWalksEachHostAndSkipsExhaustedHosts(t *testing.T) {
	a := &paginatedForge{fakeForge: fakeForge{host: "a"}, pages: map[string]*ChangePage{
		"":       {Changes: []Change{{ID: "1", UpdatedAt: "2026-11-05T10:00:00Z"}}, HasNextPage: true, EndCursor: "a-next"},
		"a-next": {Changes: []Change{{ID: "2"}}, HasNextPage: true, EndCursor: "a-last"},
		"a-last": {Changes: []Change{{ID: "3"}}},
	}}
	b := &paginatedForge{fakeForge: fakeForge{host: "b"}, pages: map[string]*ChangePage{
		"":       {Changes: []Change{{ID: "4", UpdatedAt: "2026-11-05T11:00:00Z"}}, HasNextPage: true, EndCursor: "b-next"},
		"b-next": {Changes: []Change{{ID: "5"}}},
	}}
	f := NewFleet(a, b)
	cursor := ""
	seen := make(map[string]bool)
	for step := 0; step < 3; step++ {
		page, err := f.Changes(context.Background(), ScopeReviewer, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, change := range page.Changes {
			if change.Host == "" || seen[change.ID] {
				t.Fatalf("missing provenance or duplicate: %+v", change)
			}
			seen[change.ID] = true
		}
		if step == 0 && page.Changes[0].ID != "4" {
			t.Fatal("page should be sorted by update time")
		}
		if step < 2 && (!page.HasNextPage || page.EndCursor == "") {
			t.Fatal("later host pages are unreachable")
		}
		if step == 2 && (page.HasNextPage || page.EndCursor != "") {
			t.Fatal("pagination should stop when hosts are exhausted")
		}
		cursor = page.EndCursor
	}
	if len(seen) != 5 || strings.Join(a.requested, ",") != ",a-next,a-last" || strings.Join(b.requested, ",") != ",b-next" {
		t.Fatalf("seen %v; requested a=%v b=%v", seen, a.requested, b.requested)
	}
}

func TestFleetPaginationPreservesFailedHostWarning(t *testing.T) {
	a := &paginatedForge{fakeForge: fakeForge{host: "a"}, pages: map[string]*ChangePage{
		"":     {Changes: []Change{{ID: "1"}}, HasNextPage: true, EndCursor: "next"},
		"next": {Changes: []Change{{ID: "2"}}},
	}}
	b := &paginatedForge{fakeForge: fakeForge{host: "b", err: errors.New("offline")}}
	f := NewFleet(a, b)
	page, err := f.Changes(context.Background(), ScopeReviewer, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	page, err = f.Changes(context.Background(), ScopeReviewer, page.EndCursor, 1)
	if err != nil || len(page.Changes) != 1 {
		t.Fatalf("healthy pagination failed: %+v %v", page, err)
	}
	if bad := f.Degraded(); len(bad) != 1 || bad[0] != "b" {
		t.Fatalf("lost degraded host: %v", bad)
	}
	if len(b.requested) != 1 {
		t.Fatal("failed host should be retried on refresh, not every page")
	}
}

func TestFleetPaginationRejectsInvalidAndStuckCursors(t *testing.T) {
	a := &paginatedForge{fakeForge: fakeForge{host: "a"}, pages: map[string]*ChangePage{
		"": {HasNextPage: true},
	}}
	b := &paginatedForge{fakeForge: fakeForge{host: "b", err: errors.New("offline")}}
	f := NewFleet(a, b)
	if _, err := f.Changes(context.Background(), ScopeReviewer, "bad cursor", 1); err == nil {
		t.Fatal("invalid fleet cursor accepted")
	}
	if _, err := f.Changes(context.Background(), ScopeReviewer, "", 1); err == nil {
		t.Fatal("non-advancing host cursor should fail")
	}
}
