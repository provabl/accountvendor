// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provabl/accountvendor/internal/provision"
)

// runVend must reject every locally-detectable mistake BEFORE it touches AWS.
// That ordering is the point: on the provision path the first AWS write is an
// irreversible CreateAccount, so a typo'd type key or a missing catalog has to
// fail on a local read, not after an account exists.

func TestRunVend_MissingCatalogFailsBeforeAWS(t *testing.T) {
	o := &vendOptions{catalogPath: filepath.Join(t.TempDir(), "nope.json"), region: "us-east-1"}
	err := runVend(context.Background(), o, provision.Request{Type: "nih-genomics", Name: "n", Email: "e"})
	if err == nil {
		t.Fatal("expected an error for an absent catalog")
	}
	if !strings.Contains(err.Error(), "catalog") {
		t.Errorf("error should name the catalog, got: %v", err)
	}
}

func TestRunVend_UnknownTypeFailsBeforeAWS(t *testing.T) {
	o := &vendOptions{catalogPath: writeCatalog(t, sampleCatalog), region: "us-east-1"}
	err := runVend(context.Background(), o, provision.Request{Type: "not-a-type", Name: "n", Email: "e"})
	if err == nil {
		t.Fatal("expected an error for an unknown SRE type")
	}
	if !strings.Contains(err.Error(), "catalog list") {
		t.Errorf("error should point at 'vendor catalog list', got: %v", err)
	}
}

// Without --region and without --ground-meta there's no region to act in.
func TestRunVend_NoRegionFailsBeforeAWS(t *testing.T) {
	o := &vendOptions{catalogPath: writeCatalog(t, sampleCatalog)}
	err := runVend(context.Background(), o, provision.Request{Type: "nih-genomics", Name: "n", Email: "e"})
	if err == nil {
		t.Fatal("expected an error when no region is available")
	}
	if !strings.Contains(err.Error(), "--region") {
		t.Errorf("error should say how to supply a region, got: %v", err)
	}
}

// A type with no OU and no --parent has no target: refuse rather than guess a
// destination (misplacing an account under the wrong OU applies the wrong SCPs).
func TestRunVend_NoTargetOUFailsBeforeAWS(t *testing.T) {
	o := &vendOptions{catalogPath: writeCatalog(t, sampleCatalog), region: "us-east-1"}
	err := runVend(context.Background(), o, provision.Request{Type: "cui-l2", Name: "n", Email: "e"})
	if err == nil {
		t.Fatal("expected an error when neither the type nor --parent names an OU")
	}
	if !strings.Contains(err.Error(), "--parent") {
		t.Errorf("error should mention --parent, got: %v", err)
	}
}

func TestRunVend_GroundMetaMissingFileErrors(t *testing.T) {
	o := &vendOptions{
		catalogPath: writeCatalog(t, sampleCatalog),
		groundMeta:  filepath.Join(t.TempDir(), "nope.json"),
	}
	if err := runVend(context.Background(), o, provision.Request{Type: "nih-genomics"}); err == nil {
		t.Fatal("expected an error for an absent ground-meta file")
	}
}

// provision is the irreversible command; its help must say so, and must point at
// the reversible alternative. This is a contract with the operator, not decoration.
func TestProvisionCmd_WarnsIrreversible(t *testing.T) {
	long := provisionCmd().Long
	for _, want := range []string{"IRREVERSIBLE", "never\ndeleted", "vendor adopt", "--dry-run"} {
		if !strings.Contains(long, want) {
			t.Errorf("provision help must mention %q so an operator knows the stakes", want)
		}
	}
}

func TestAdoptCmd_DocumentsReversibleAndIdempotent(t *testing.T) {
	long := adoptCmd().Long
	for _, want := range []string{"Reversible", "Idempotent"} {
		if !strings.Contains(long, want) {
			t.Errorf("adopt help must mention %q", want)
		}
	}
}

// Both commands need --type; provision additionally needs --name/--email.
func TestVendCmds_RequiredFlags(t *testing.T) {
	p := provisionCmd()
	for _, f := range []string{"type", "name", "email"} {
		flag := p.Flags().Lookup(f)
		if flag == nil {
			t.Fatalf("provision missing --%s", f)
		}
		if flag.Annotations[cobraRequired] == nil {
			t.Errorf("provision --%s should be required", f)
		}
	}
	a := adoptCmd()
	if a.Flags().Lookup("type").Annotations[cobraRequired] == nil {
		t.Error("adopt --type should be required")
	}
	// adopt takes the account id positionally, not as a flag.
	if a.Args == nil {
		t.Error("adopt should require exactly one positional account id")
	}
}

// cobraRequired is cobra's internal annotation key for MarkFlagRequired.
const cobraRequired = "cobra_annotation_bash_completion_one_required_flag"

// skipCompiler must satisfy the Compiler seam so --skip-compile keeps the
// orchestrator's path uniform rather than nil-checking.
func TestSkipCompilerImplementsCompiler(t *testing.T) {
	var _ provision.Compiler = skipCompiler{}
}
