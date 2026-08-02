// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package provision

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// invocation is one recorded command run.
type invocation struct {
	dir  string
	name string
	args []string
}

// fakeRunner records every invocation and can fail / emit output for the call
// whose args contain a given first argument (init / frameworks / compile).
type fakeRunner struct {
	calls  []invocation
	outFor map[string]string // first arg → stdout
	errFor map[string]error  // first arg → error
}

func (f *fakeRunner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, invocation{dir: dir, name: name, args: args})
	key := ""
	if len(args) > 0 {
		key = args[0]
	}
	return []byte(f.outFor[key]), f.errFor[key]
}

func (f *fakeRunner) argvFor(sub string) []string {
	for _, c := range f.calls {
		if len(c.args) > 0 && c.args[0] == sub {
			return c.args
		}
	}
	return nil
}

func newCompiler(r *fakeRunner) *ExecCompiler {
	return NewExecCompiler("attest", "us-east-1", func(id string) string { return "/tmp/vend/" + id }).
		WithRunner(r)
}

// The pre-flight is a three-step sequence, because attest reads its frameworks
// from .attest/sre.yaml rather than from a flag.
func TestCompile_RunsInitFrameworksAddCompile(t *testing.T) {
	r := &fakeRunner{}
	err := newCompiler(r).Compile(context.Background(), "123456789012", []string{"nih-gds", "nist-800-53-moderate"})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(r.calls) != 3 {
		t.Fatalf("expected 3 attest calls (init, frameworks add, compile), got %d: %+v", len(r.calls), r.calls)
	}
	if got := r.calls[0].args[0]; got != "init" {
		t.Errorf("first call = %q, want init", got)
	}
	if got := strings.Join(r.calls[1].args, " "); !strings.HasPrefix(got, "frameworks add") {
		t.Errorf("second call = %q, want 'frameworks add …'", got)
	}
	if got := r.calls[2].args[0]; got != "compile" {
		t.Errorf("third call = %q, want compile", got)
	}
}

// Every call must run in the account's own directory — attest is directory-scoped,
// so a shared cwd would have one account overwrite another's .attest/sre.yaml.
func TestCompile_RunsInPerAccountDirectory(t *testing.T) {
	r := &fakeRunner{}
	if err := newCompiler(r).Compile(context.Background(), "123456789012", []string{"nih-gds"}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	for _, c := range r.calls {
		if c.dir != "/tmp/vend/123456789012" {
			t.Errorf("call %v ran in %q, want the per-account dir", c.args, c.dir)
		}
	}
}

// Framework ids go to `frameworks add` as separate argv elements — NOT to
// compile's --frameworks, which is a directory path.
func TestCompile_FrameworkIDsGoToFrameworksAddNotCompileFlag(t *testing.T) {
	r := &fakeRunner{}
	c := newCompiler(r)
	c.FrameworksDir = "/opt/attest/frameworks"
	if err := c.Compile(context.Background(), "1", []string{"nih-gds", "cmmc-level-2"}); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	add := r.argvFor("frameworks")
	if add == nil {
		t.Fatal("no 'frameworks add' call recorded")
	}
	// Each id is its own element, and never a comma-joined blob.
	joined := strings.Join(add, " ")
	if !strings.Contains(joined, "nih-gds cmmc-level-2") {
		t.Errorf("framework ids should be separate argv elements, got %q", joined)
	}
	if strings.Contains(joined, "nih-gds,cmmc-level-2") {
		t.Error("framework ids must not be comma-joined — attest takes them as separate args")
	}

	// --frameworks must carry the DIRECTORY, on both calls that accept it.
	for _, sub := range []string{"frameworks", "compile"} {
		argv := r.argvFor(sub)
		for i, a := range argv {
			if a == "--frameworks" {
				if i+1 >= len(argv) || argv[i+1] != "/opt/attest/frameworks" {
					t.Errorf("%s: --frameworks should be the directory path, got %v", sub, argv)
				}
			}
		}
	}
}

// The fail-open guard: attest compile exits 0 when nothing is active, so vendor
// must not read that as a pass.
func TestCompile_NoActiveFrameworksIsFailureNotPass(t *testing.T) {
	r := &fakeRunner{outFor: map[string]string{
		"compile": "No active frameworks. Run 'attest frameworks add <id>' first.",
	}}
	err := newCompiler(r).Compile(context.Background(), "123456789012", []string{"nih-gds"})
	if err == nil {
		t.Fatal("a zero-exit 'No active frameworks' compile must be treated as a FAILED pre-flight")
	}
	if !strings.Contains(err.Error(), "compiled nothing") {
		t.Errorf("error should explain the fail-open guard, got: %v", err)
	}
}

// A non-zero exit at any step must propagate, and must include attest's output —
// that's where the reason lives.
func TestCompile_PropagatesFailureWithOutput(t *testing.T) {
	for _, step := range []string{"init", "frameworks", "compile"} {
		t.Run(step, func(t *testing.T) {
			r := &fakeRunner{
				errFor: map[string]error{step: errors.New("exit status 1")},
				outFor: map[string]string{step: "framework \"bogus\" not found in frameworks"},
			}
			err := newCompiler(r).Compile(context.Background(), "1", []string{"bogus"})
			if err == nil {
				t.Fatalf("a %s failure must propagate", step)
			}
			if !strings.Contains(err.Error(), "not found") {
				t.Errorf("error should include attest's output, got: %v", err)
			}
		})
	}
}

// A failing step must stop the sequence — don't compile if activation failed.
func TestCompile_StopsAtFirstFailure(t *testing.T) {
	r := &fakeRunner{errFor: map[string]error{"init": errors.New("exit status 1")}}
	if err := newCompiler(r).Compile(context.Background(), "1", []string{"nih-gds"}); err == nil {
		t.Fatal("expected an init failure")
	}
	if len(r.calls) != 1 {
		t.Errorf("expected to stop after the failing init, got %d calls", len(r.calls))
	}
}

// A type with no frameworks must be an error, not a silently-passing pre-flight.
func TestCompile_EmptyFrameworksRefused(t *testing.T) {
	r := &fakeRunner{}
	err := newCompiler(r).Compile(context.Background(), "1", nil)
	if err == nil {
		t.Fatal("expected an error for an empty framework list")
	}
	if len(r.calls) != 0 {
		t.Error("must not invoke attest with nothing to compile")
	}
}

func TestCompile_RequiresAccountAndWorkDir(t *testing.T) {
	r := &fakeRunner{}
	if err := newCompiler(r).Compile(context.Background(), "", []string{"nih-gds"}); err == nil {
		t.Error("expected an error for an empty account id")
	}
	// No WorkDirFor configured at all.
	c := &ExecCompiler{Binary: "attest", runner: r}
	if err := c.Compile(context.Background(), "1", []string{"nih-gds"}); err == nil {
		t.Error("expected an error when no working directory is configured")
	}
	// WorkDirFor returning empty.
	c2 := NewExecCompiler("attest", "", func(string) string { return "" }).WithRunner(r)
	if err := c2.Compile(context.Background(), "1", []string{"nih-gds"}); err == nil {
		t.Error("expected an error for an empty working directory")
	}
}

// Region is passed to init (which accepts it), not to compile (which doesn't).
func TestCompile_RegionOnlyOnInit(t *testing.T) {
	r := &fakeRunner{}
	if err := newCompiler(r).Compile(context.Background(), "1", []string{"nih-gds"}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !strings.Contains(strings.Join(r.argvFor("init"), " "), "--region us-east-1") {
		t.Errorf("init should carry --region, got %v", r.argvFor("init"))
	}
	if strings.Contains(strings.Join(r.argvFor("compile"), " "), "--region") {
		t.Errorf("compile has no --region flag; must not be passed one: %v", r.argvFor("compile"))
	}
}

// ExecCompiler must satisfy the Compiler seam the orchestrator consumes.
func TestExecCompilerImplementsCompiler(t *testing.T) {
	var _ Compiler = (*ExecCompiler)(nil)
}
