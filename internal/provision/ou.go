// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package provision

import (
	"context"
	"fmt"
	"strings"
)

// OULister is the read-only slice of AWS Organizations needed to turn an OU
// *name* into an OU *id*. Kept separate from Provisioner so the resolution logic
// (the part with real branching) is testable without any AWS client.
type OULister interface {
	// Roots returns the organization root ids (usually exactly one).
	Roots(ctx context.Context) ([]string, error)
	// Children returns the (id, name) pairs of the OUs directly under parentID.
	Children(ctx context.Context, parentID string) ([]OU, error)
}

// OU is one organizational unit.
type OU struct {
	ID   string
	Name string
}

// ouIDPrefixes are the forms AWS accepts as a placement target directly: an OU
// id ("ou-…") or a root id ("r-…").
func isOUID(s string) bool {
	return strings.HasPrefix(s, "ou-") || strings.HasPrefix(s, "r-")
}

// ResolveOU turns a catalog OU *name* into an OU *id*.
//
// This exists because of a real contract detail, not for generality: ground
// creates its OUs via CDK with plain names ("SensitiveResearch", "NIHGenomic"),
// and `ground-meta.json` carries **no OU ids at all** — so a name is the only
// handle vendor is given. AWS placement APIs require an id.
//
// ground's OUs are also **nested** (NIHGenomic / HIPAAResearch / CUIResearch sit
// under SensitiveResearch), so this walks the tree breadth-first from the root
// rather than checking only the top level.
//
// Passing a literal "ou-…"/"r-…" short-circuits: an operator who knows the id
// shouldn't need a lookup (and shouldn't need list permissions).
//
// Ambiguity is an error, never a guess: AWS permits the same OU name under two
// different parents, and silently picking one could place a sensitive account in
// the wrong compliance boundary. The caller must disambiguate with an explicit id.
func ResolveOU(ctx context.Context, l OULister, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", fmt.Errorf("no OU name or id given")
	}
	if isOUID(nameOrID) {
		return nameOrID, nil
	}

	roots, err := l.Roots(ctx)
	if err != nil {
		return "", fmt.Errorf("list organization roots: %w", err)
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("organization has no root — is this the management account?")
	}

	// Breadth-first walk, collecting every match so ambiguity can be reported
	// rather than silently resolved. `seen` bounds the walk; `matched` dedupes the
	// results, so encountering the same OU id twice (a repeated edge) reports one
	// match rather than a bogus "ambiguous (ou-a, ou-a)".
	var matches []string
	matched := make(map[string]bool)
	queue := append([]string(nil), roots...)
	seen := make(map[string]bool, len(roots))
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		if seen[parent] {
			continue
		}
		seen[parent] = true

		children, err := l.Children(ctx, parent)
		if err != nil {
			return "", fmt.Errorf("list OUs under %s: %w", parent, err)
		}
		for _, c := range children {
			if c.Name == nameOrID && !matched[c.ID] {
				matched[c.ID] = true
				matches = append(matches, c.ID)
			}
			queue = append(queue, c.ID)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no OU named %q in the organization — check the name, or pass the ou-… id directly", nameOrID)
	default:
		return "", fmt.Errorf("OU name %q is ambiguous (%s): AWS allows duplicate names under different parents, "+
			"so vendor will not guess which compliance boundary you meant — pass the ou-… id explicitly",
			nameOrID, strings.Join(matches, ", "))
	}
}
