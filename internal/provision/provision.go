// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

// Package provision orchestrates vending an account: resolve the SRE type from the
// catalog, place/adopt the account, run the `attest compile` pre-flight for the
// type's frameworks, and produce the per-account meta. It composes three seams —
// an Account provisioner (AWS Organizations), a Compiler (shells to attest), and
// the catalog + ground-meta — so the whole flow is testable without AWS or the
// attest binary. The live adapters land later, adopt-first.
//
// The load-bearing safety choice: **provision is adopt-capable from day one**.
// `organizations:CreateAccount` is irreversible (an account can only be closed,
// never deleted), so the orchestration is validated first via Adopt (retrofit an
// existing account — reversible) before Create is ever exercised live. Both go
// through the same post-placement path (baseline + tags + compile + meta), so
// adopt proves everything except the irreversible create call.
package provision

import (
	"context"
	"fmt"
	"time"

	"github.com/provabl/accountvendor/internal/meta"
	"github.com/provabl/schemas/catalog"
)

// Account is a provisioned (created or adopted) AWS account.
type Account struct {
	ID   string
	Name string
}

// Provisioner places an account under a parent OU. The live impl is AWS
// Organizations; a fake drives tests. Create is the irreversible path (validated
// last); Adopt retrofits an already-existing account (reversible, validated first).
type Provisioner interface {
	// Create vends a NEW account (Organizations CreateAccount + MoveAccount to
	// parentOU). Irreversible — an account can only be closed, never deleted.
	Create(ctx context.Context, req CreateRequest) (*Account, error)
	// Adopt takes an EXISTING account id and moves/places it under parentOU.
	Adopt(ctx context.Context, accountID, parentOU string) (*Account, error)
	// Tag applies tags to the account.
	Tag(ctx context.Context, accountID string, tags map[string]string) error
}

// CreateRequest describes a new account to vend.
type CreateRequest struct {
	Name     string // account name, e.g. "chen-genomics"
	Email    string // the account's root email (AWS requires a unique one)
	ParentOU string // target OU
}

// Compiler runs the attest compile pre-flight so policy is in place before the
// account is declared ready. The live impl shells to `attest compile`; a fake
// drives tests (no attest binary needed).
type Compiler interface {
	// Compile runs `attest compile --frameworks …` for the account. It returns an
	// error if compilation fails — vendor must not declare an account ready if its
	// policy couldn't be compiled.
	Compile(ctx context.Context, accountID string, frameworks []string) error
}

// Orchestrator composes the seams into the vend flow.
type Orchestrator struct {
	prov     Provisioner
	compiler Compiler
	ground   *meta.GroundMeta
	catalog  *catalog.Catalog
	version  string
	now      func() time.Time
}

// New builds an Orchestrator. ground provides region/SSO context; cat is the
// SRE-type catalog; version is stamped into the account meta.
func New(prov Provisioner, compiler Compiler, ground *meta.GroundMeta, cat *catalog.Catalog, version string) *Orchestrator {
	return &Orchestrator{prov: prov, compiler: compiler, ground: ground, catalog: cat, version: version, now: time.Now}
}

// WithClock overrides the timestamp source (tests).
func (o *Orchestrator) WithClock(now func() time.Time) *Orchestrator { o.now = now; return o }

// Request is one vend request.
type Request struct {
	Type     string // catalog SRE-type key (e.g. "nih-genomics")
	Name     string // account name
	Email    string // root email (Provision/Create only)
	ParentOU string // target OU; falls back to the type's catalog OU when empty
	// AdoptAccountID, when set, adopts that existing account instead of creating
	// a new one — the reversible path used to validate the pipeline.
	AdoptAccountID string
}

// Vend runs the flow: resolve the type, place (create or adopt) the account, apply
// the type's tags, run the attest-compile pre-flight, and build the account meta.
// Fail-closed ordering: a type that isn't in the catalog, a placement failure, or
// a compile failure each stop before an account meta is produced — vendor never
// emits a manifest for an account whose policy didn't compile.
func (o *Orchestrator) Vend(ctx context.Context, req Request) (*meta.AccountMeta, error) {
	t, ok := o.catalog.Get(req.Type)
	if !ok {
		return nil, fmt.Errorf("unknown SRE type %q (see 'vendor catalog list')", req.Type)
	}
	parentOU := req.ParentOU
	if parentOU == "" {
		parentOU = t.OU
	}
	if parentOU == "" {
		return nil, fmt.Errorf("no target OU: type %q has no default OU, and no --parent was given", t.Key)
	}

	// 1. Place the account — adopt (reversible) or create (irreversible).
	var acct *Account
	var err error
	if req.AdoptAccountID != "" {
		acct, err = o.prov.Adopt(ctx, req.AdoptAccountID, parentOU)
	} else {
		if req.Name == "" || req.Email == "" {
			return nil, fmt.Errorf("creating an account requires --name and --email")
		}
		acct, err = o.prov.Create(ctx, CreateRequest{Name: req.Name, Email: req.Email, ParentOU: parentOU})
	}
	if err != nil {
		return nil, fmt.Errorf("place account: %w", err)
	}

	// 2. Apply the type's tags.
	if len(t.Tags) > 0 {
		if err := o.prov.Tag(ctx, acct.ID, t.Tags); err != nil {
			return nil, fmt.Errorf("tag account %s: %w", acct.ID, err)
		}
	}

	// 3. attest compile pre-flight — policy in place before the account is ready.
	if err := o.compiler.Compile(ctx, acct.ID, t.Frameworks); err != nil {
		return nil, fmt.Errorf("attest compile pre-flight for %s: %w", acct.ID, err)
	}

	// 4. Build the account meta for `attest init`.
	return &meta.AccountMeta{
		AccountID:                 acct.ID,
		AccountName:               acct.Name,
		Region:                    o.ground.Region,
		ParentOU:                  parentOU,
		SREType:                   t.Key,
		Frameworks:                t.Frameworks,
		Tags:                      t.Tags,
		IdentityCenterInstanceARN: o.ground.IdentityCenterInstanceARN,
		VendorVersion:             o.version,
		VendedAt:                  o.now().UTC(),
	}, nil
}
