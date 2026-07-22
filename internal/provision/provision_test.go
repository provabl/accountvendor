// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package provision

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/provabl/accountvendor/internal/meta"
	"github.com/provabl/schemas/catalog"
)

// fakeProv records what it did and can fail any step.
type fakeProv struct {
	created                     string // account id returned by Create
	createErr, adoptErr, tagErr error
	didCreate, didAdopt         bool
	taggedID                    string
	taggedTags                  map[string]string
}

func (f *fakeProv) Create(_ context.Context, req CreateRequest) (*Account, error) {
	f.didCreate = true
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &Account{ID: f.created, Name: req.Name}, nil
}
func (f *fakeProv) Adopt(_ context.Context, id, _ string) (*Account, error) {
	f.didAdopt = true
	if f.adoptErr != nil {
		return nil, f.adoptErr
	}
	return &Account{ID: id, Name: "adopted"}, nil
}
func (f *fakeProv) Tag(_ context.Context, id string, tags map[string]string) error {
	f.taggedID, f.taggedTags = id, tags
	return f.tagErr
}

// fakeCompiler records the compile call and can fail.
type fakeCompiler struct {
	err        error
	gotAccount string
	gotFw      []string
}

func (f *fakeCompiler) Compile(_ context.Context, accountID string, fw []string) error {
	f.gotAccount, f.gotFw = accountID, fw
	return f.err
}

func testCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load(strings.NewReader(`{
	  "schema_version": 1,
	  "types": [
	    {"key":"nih-genomics","name":"NIH genomics","frameworks":["nih-gds","nist-800-53-moderate"],"ou":"SensitiveResearch","tags":{"data-class":"GENOMIC"}},
	    {"key":"no-ou","name":"No default OU","frameworks":["cmmc-level-2"]}
	  ]}`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newOrch(t *testing.T, p Provisioner, c Compiler) *Orchestrator {
	t.Helper()
	g := &meta.GroundMeta{Region: "us-east-1", IdentityCenterInstanceARN: "arn:sso:ins-1"}
	return New(p, c, g, testCatalog(t), "0.1.0").
		WithClock(func() time.Time { return time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC) })
}

func TestVend_CreatePath(t *testing.T) {
	p := &fakeProv{created: "123456789012"}
	comp := &fakeCompiler{}
	o := newOrch(t, p, comp)

	m, err := o.Vend(context.Background(), Request{Type: "nih-genomics", Name: "chen", Email: "chen@x.org"})
	if err != nil {
		t.Fatalf("Vend: %v", err)
	}
	if !p.didCreate || p.didAdopt {
		t.Errorf("expected Create, not Adopt (create=%v adopt=%v)", p.didCreate, p.didAdopt)
	}
	// tags applied, compile run against the type's frameworks
	if p.taggedTags["data-class"] != "GENOMIC" {
		t.Errorf("tags not applied: %v", p.taggedTags)
	}
	if comp.gotAccount != "123456789012" || len(comp.gotFw) != 2 {
		t.Errorf("compile args wrong: acct=%s fw=%v", comp.gotAccount, comp.gotFw)
	}
	// account meta carries type/frameworks/region/OU/SSO
	if m.AccountID != "123456789012" || m.SREType != "nih-genomics" || m.Region != "us-east-1" {
		t.Errorf("meta wrong: %+v", m)
	}
	if m.ParentOU != "SensitiveResearch" {
		t.Errorf("ParentOU should default to the type's OU, got %q", m.ParentOU)
	}
	if m.IdentityCenterInstanceARN == "" {
		t.Error("SSO ARN should carry through from ground-meta")
	}
}

func TestVend_AdoptPath(t *testing.T) {
	p := &fakeProv{}
	o := newOrch(t, p, &fakeCompiler{})
	m, err := o.Vend(context.Background(), Request{Type: "nih-genomics", AdoptAccountID: "999988887777"})
	if err != nil {
		t.Fatalf("Vend adopt: %v", err)
	}
	if !p.didAdopt || p.didCreate {
		t.Error("expected Adopt, not Create")
	}
	if m.AccountID != "999988887777" {
		t.Errorf("adopted account id = %s", m.AccountID)
	}
}

func TestVend_UnknownTypeFailsClosed(t *testing.T) {
	p := &fakeProv{created: "1"}
	o := newOrch(t, p, &fakeCompiler{})
	if _, err := o.Vend(context.Background(), Request{Type: "nope", Name: "x", Email: "e"}); err == nil {
		t.Error("unknown type must error")
	}
	if p.didCreate {
		t.Error("must not create an account for an unknown type")
	}
}

func TestVend_NoOUAndNoParentErrors(t *testing.T) {
	p := &fakeProv{created: "1"}
	o := newOrch(t, p, &fakeCompiler{})
	// "no-ou" type has no default OU and we pass no --parent
	if _, err := o.Vend(context.Background(), Request{Type: "no-ou", Name: "x", Email: "e"}); err == nil {
		t.Error("no OU + no --parent must error")
	}
	if p.didCreate {
		t.Error("must not create without a target OU")
	}
}

func TestVend_ParentOverridesTypeOU(t *testing.T) {
	p := &fakeProv{created: "1"}
	o := newOrch(t, p, &fakeCompiler{})
	m, err := o.Vend(context.Background(), Request{Type: "nih-genomics", Name: "x", Email: "e", ParentOU: "ou-custom"})
	if err != nil {
		t.Fatal(err)
	}
	if m.ParentOU != "ou-custom" {
		t.Errorf("explicit --parent should win, got %q", m.ParentOU)
	}
}

func TestVend_CreateWithoutNameEmailErrors(t *testing.T) {
	p := &fakeProv{created: "1"}
	o := newOrch(t, p, &fakeCompiler{})
	if _, err := o.Vend(context.Background(), Request{Type: "nih-genomics"}); err == nil {
		t.Error("create path needs --name and --email")
	}
	if p.didCreate {
		t.Error("must not attempt create without name/email")
	}
}

// A compile failure must stop before an account meta is produced.
func TestVend_CompileFailureNoMeta(t *testing.T) {
	p := &fakeProv{created: "1"}
	comp := &fakeCompiler{err: errors.New("framework not found")}
	o := newOrch(t, p, comp)
	m, err := o.Vend(context.Background(), Request{Type: "nih-genomics", Name: "x", Email: "e"})
	if err == nil {
		t.Error("compile failure must propagate")
	}
	if m != nil {
		t.Error("no account meta when compile fails — the account isn't ready")
	}
}

// A placement failure never reaches tag/compile.
func TestVend_PlacementFailureStops(t *testing.T) {
	p := &fakeProv{createErr: errors.New("email in use")}
	comp := &fakeCompiler{}
	o := newOrch(t, p, comp)
	if _, err := o.Vend(context.Background(), Request{Type: "nih-genomics", Name: "x", Email: "e"}); err == nil {
		t.Error("placement failure must propagate")
	}
	if comp.gotAccount != "" {
		t.Error("must not compile when placement failed")
	}
}
