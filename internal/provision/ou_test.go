// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package provision

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeOULister serves a static OU tree: parent id → its children.
type fakeOULister struct {
	roots     []string
	tree      map[string][]OU
	rootsErr  error
	childErr  error
	callCount int
}

func (f *fakeOULister) Roots(context.Context) ([]string, error) {
	if f.rootsErr != nil {
		return nil, f.rootsErr
	}
	return f.roots, nil
}

func (f *fakeOULister) Children(_ context.Context, parentID string) ([]OU, error) {
	f.callCount++
	if f.childErr != nil {
		return nil, f.childErr
	}
	return f.tree[parentID], nil
}

// groundTree mirrors the OU layout ground actually deploys (internal/iac):
// Security / Infrastructure / Research / SensitiveResearch / DoD-CMMC under the
// root, with NIHGenomic, HIPAAResearch, and CUIResearch NESTED under
// SensitiveResearch.
func groundTree() *fakeOULister {
	return &fakeOULister{
		roots: []string{"r-root"},
		tree: map[string][]OU{
			"r-root": {
				{ID: "ou-sec", Name: "Security"},
				{ID: "ou-infra", Name: "Infrastructure"},
				{ID: "ou-research", Name: "Research"},
				{ID: "ou-sensitive", Name: "SensitiveResearch"},
				{ID: "ou-dod", Name: "DoD-CMMC"},
			},
			"ou-sensitive": {
				{ID: "ou-nih", Name: "NIHGenomic"},
				{ID: "ou-hipaa", Name: "HIPAAResearch"},
				{ID: "ou-cui", Name: "CUIResearch"},
			},
		},
	}
}

func TestResolveOU_TopLevelName(t *testing.T) {
	got, err := ResolveOU(context.Background(), groundTree(), "SensitiveResearch")
	if err != nil {
		t.Fatalf("ResolveOU: %v", err)
	}
	if got != "ou-sensitive" {
		t.Errorf("got %q, want ou-sensitive", got)
	}
}

// The load-bearing case: ground nests the data-scoped OUs one level down, so a
// top-level-only lookup would fail to find the very OUs sensitive accounts go in.
func TestResolveOU_NestedName(t *testing.T) {
	for name, want := range map[string]string{
		"NIHGenomic":    "ou-nih",
		"HIPAAResearch": "ou-hipaa",
		"CUIResearch":   "ou-cui",
	} {
		got, err := ResolveOU(context.Background(), groundTree(), name)
		if err != nil {
			t.Fatalf("ResolveOU(%s): %v", name, err)
		}
		if got != want {
			t.Errorf("ResolveOU(%s) = %q, want %q", name, got, want)
		}
	}
}

// An explicit id must short-circuit — no API calls at all, so an operator who
// knows the id doesn't need organizations:List* permissions.
func TestResolveOU_ExplicitIDSkipsLookup(t *testing.T) {
	for _, id := range []string{"ou-abc123", "r-root"} {
		f := groundTree()
		got, err := ResolveOU(context.Background(), f, id)
		if err != nil {
			t.Fatalf("ResolveOU(%s): %v", id, err)
		}
		if got != id {
			t.Errorf("got %q, want %q", got, id)
		}
		if f.callCount != 0 {
			t.Errorf("an explicit id must not call the API, got %d calls", f.callCount)
		}
	}
}

// A duplicate name under two parents must be an error, never a guess — picking
// one could place a sensitive account in the wrong compliance boundary.
func TestResolveOU_AmbiguousNameFailsClosed(t *testing.T) {
	f := &fakeOULister{
		roots: []string{"r-root"},
		tree: map[string][]OU{
			"r-root": {{ID: "ou-a", Name: "Research"}, {ID: "ou-b", Name: "SensitiveResearch"}},
			"ou-b":   {{ID: "ou-c", Name: "Research"}}, // same name, different parent
		},
	}
	_, err := ResolveOU(context.Background(), f, "Research")
	if err == nil {
		t.Fatal("expected an ambiguity error, got nil")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should explain the ambiguity, got: %v", err)
	}
	// Both candidates should be named so the operator can choose.
	if !strings.Contains(err.Error(), "ou-a") || !strings.Contains(err.Error(), "ou-c") {
		t.Errorf("error should list both candidate ids, got: %v", err)
	}
}

func TestResolveOU_UnknownName(t *testing.T) {
	_, err := ResolveOU(context.Background(), groundTree(), "NoSuchOU")
	if err == nil {
		t.Fatal("expected an error for an unknown OU name")
	}
	if !strings.Contains(err.Error(), "NoSuchOU") {
		t.Errorf("error should name the missing OU, got: %v", err)
	}
}

func TestResolveOU_EmptyName(t *testing.T) {
	if _, err := ResolveOU(context.Background(), groundTree(), ""); err == nil {
		t.Error("expected an error for an empty OU name")
	}
}

// API failures must propagate, not resolve to a wrong-but-plausible OU.
func TestResolveOU_APIErrorsFailClosed(t *testing.T) {
	t.Run("roots", func(t *testing.T) {
		f := groundTree()
		f.rootsErr = errors.New("AccessDenied")
		if _, err := ResolveOU(context.Background(), f, "SensitiveResearch"); err == nil {
			t.Error("a Roots failure must propagate")
		}
	})
	t.Run("children", func(t *testing.T) {
		f := groundTree()
		f.childErr = errors.New("AccessDenied")
		if _, err := ResolveOU(context.Background(), f, "SensitiveResearch"); err == nil {
			t.Error("a Children failure must propagate")
		}
	})
	t.Run("no roots", func(t *testing.T) {
		f := &fakeOULister{roots: nil, tree: map[string][]OU{}}
		if _, err := ResolveOU(context.Background(), f, "SensitiveResearch"); err == nil {
			t.Error("an org with no root must error")
		}
	})
}

// A cycle (or a repeated id) must not hang the walk.
func TestResolveOU_CycleTerminates(t *testing.T) {
	f := &fakeOULister{
		roots: []string{"r-root"},
		tree: map[string][]OU{
			"r-root": {{ID: "ou-a", Name: "A"}},
			"ou-a":   {{ID: "ou-a", Name: "A"}}, // self-reference
		},
	}
	got, err := ResolveOU(context.Background(), f, "A")
	if err != nil {
		t.Fatalf("ResolveOU: %v", err)
	}
	if got != "ou-a" {
		t.Errorf("got %q, want ou-a", got)
	}
}
