// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package provision

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner executes an external command in a given working directory. It exists so
// ExecCompiler is testable without the attest binary installed, and so the argv
// is asserted rather than assumed. Mirrors steward's CommandMover seam.
//
// dir is part of the interface (not applied by the caller) because attest is
// directory-scoped, and the correct way to set it — exec.Cmd.Dir — lives with the
// command. os.Chdir would be wrong: it's process-global, so vending two accounts
// concurrently could point attest at the wrong account's .attest/sre.yaml.
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	// #nosec G204 -- name/args are built from the catalog + resolved account id,
	// never from raw shell input, and are passed as an argv (no shell).
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// ExecCompiler runs the `attest compile` pre-flight by shelling out to the attest
// binary. vendor depends on attest's *CLI*, deliberately not its Go packages: the
// suite's rule is that tools stay decoupled (only the evidence kernel is a shared
// import), so this is the same standard-interface handoff as ground → attest.
//
// **The real attest contract**, verified against attest's cmd/attest/main.go
// rather than assumed — it drove this design:
//
//   - `attest compile` takes **no account flag**. It operates on the SRE in its
//     working directory, reading active frameworks from `.attest/sre.yaml`. So
//     vendor runs it in a per-account directory instead of passing an account id.
//   - `compile --frameworks` is a **frameworks *directory* path**, NOT a list of
//     framework ids. Passing "nih-gds,cmmc-level-2" there would be read as a
//     directory name.
//   - Activating frameworks is a separate step: `attest frameworks add <id>...`,
//     which does hard-error on an unknown id.
//   - `attest init` creates `.attest/sre.yaml` and accepts `--region`.
//
// So the pre-flight is a three-step sequence — init, frameworks add, compile —
// run inside the account's own directory.
//
// The fail-open hazard this guards: with no active frameworks, `attest compile`
// prints "No active frameworks" and **exits 0**. Taken at face value that would
// let vendor report a passing compliance pre-flight that compiled nothing, so
// Compile treats that output as a failure.
type ExecCompiler struct {
	// Binary is the attest executable ("attest" resolved from PATH by default).
	Binary string
	// Region is passed to `attest init`.
	Region string
	// FrameworksDir is attest's frameworks *directory* (attest's own default is
	// "frameworks"); empty leaves attest's default in place.
	FrameworksDir string
	// WorkDirFor returns the directory to run attest in for a given account id.
	// Required, because attest is directory-scoped: running two accounts in one
	// directory would have the second overwrite the first's .attest/sre.yaml.
	WorkDirFor func(accountID string) string
	// SkipPrereqCheck passes --skip-prereq-check to `attest init`.
	SkipPrereqCheck bool

	runner Runner
	// mkdir is overridable in tests so Compile touches no real filesystem.
	mkdir func(string) error
}

// mkdirAll creates the working directory (overridable in tests).
func (c *ExecCompiler) mkdirAll(dir string) error {
	if c.mkdir != nil {
		return c.mkdir(dir)
	}
	return os.MkdirAll(dir, 0o750)
}

// WithMkdir overrides directory creation (tests).
func (c *ExecCompiler) WithMkdir(f func(string) error) *ExecCompiler { c.mkdir = f; return c }

// NewExecCompiler builds a Compiler that shells to the attest binary, running it
// in a per-account directory produced by workDirFor.
func NewExecCompiler(binary, region string, workDirFor func(string) string) *ExecCompiler {
	if binary == "" {
		binary = "attest"
	}
	return &ExecCompiler{Binary: binary, Region: region, WorkDirFor: workDirFor, runner: execRunner{}}
}

// WithRunner overrides the command runner (tests).
func (c *ExecCompiler) WithRunner(r Runner) *ExecCompiler { c.runner = r; return c }

// Compile runs the attest compile pre-flight for the account's frameworks:
// `attest init` → `attest frameworks add <ids…>` → `attest compile`, all in the
// account's own working directory.
//
// Fail-closed by construction. Any non-zero exit is an error, and the
// orchestrator refuses to emit an account meta when this fails — vendor never
// declares an account ready if its policy didn't compile. attest's combined
// output is included in the error, because that's where the actual reason lives
// (an unknown framework, a blocking framework conflict).
//
// An empty framework list is an error rather than a silent no-op: a catalog type
// with no frameworks would otherwise "pass" a pre-flight that never ran.
func (c *ExecCompiler) Compile(ctx context.Context, accountID string, frameworks []string) error {
	if accountID == "" {
		return fmt.Errorf("account id is required for the attest compile pre-flight")
	}
	if len(frameworks) == 0 {
		return fmt.Errorf("SRE type declares no frameworks — refusing to report a compile pre-flight that compiled nothing")
	}
	if c.WorkDirFor == nil {
		return fmt.Errorf("no working directory configured for the attest pre-flight " +
			"(attest is directory-scoped: it reads .attest/sre.yaml from its working directory)")
	}
	dir := c.WorkDirFor(accountID)
	if dir == "" {
		return fmt.Errorf("empty working directory for account %s", accountID)
	}
	// Create the directory here rather than in the caller: on the create path the
	// account id doesn't exist until CreateAccount succeeds, so only this point
	// knows the final path. `attest init` needs it to exist.
	if err := c.mkdirAll(dir); err != nil {
		return fmt.Errorf("create attest working directory %s: %w", dir, err)
	}

	bin := c.Binary
	if bin == "" {
		bin = "attest"
	}

	// 1. attest init — creates .attest/sre.yaml in the account's directory.
	initArgs := []string{"init"}
	if c.Region != "" {
		initArgs = append(initArgs, "--region", c.Region)
	}
	if c.SkipPrereqCheck {
		initArgs = append(initArgs, "--skip-prereq-check")
	}
	if _, err := c.run(ctx, dir, bin, initArgs); err != nil {
		return err
	}

	// 2. attest frameworks add <ids…> — activates the type's frameworks. Each id
	//    is a separate argv element; attest errors on an unknown one.
	addArgs := c.withFrameworksDir(append([]string{"frameworks", "add"}, frameworks...))
	if _, err := c.run(ctx, dir, bin, addArgs); err != nil {
		return err
	}

	// 3. attest compile — the pre-flight proper.
	out, err := c.run(ctx, dir, bin, c.withFrameworksDir([]string{"compile"}))
	if err != nil {
		return err
	}
	// Guard the fail-open case: compile exits 0 when nothing was activated.
	if strings.Contains(out, "No active frameworks") {
		return fmt.Errorf("attest compile reported no active frameworks for account %s despite activating %s — "+
			"treating this as a FAILED pre-flight rather than a pass, because compile exits 0 in this case and "+
			"vendor must not declare an account ready on a pre-flight that compiled nothing.\n%s",
			accountID, strings.Join(frameworks, ","), out)
	}
	return nil
}

// withFrameworksDir appends --frameworks <dir> when configured. Note this is
// attest's frameworks *directory* path, not a framework id list.
func (c *ExecCompiler) withFrameworksDir(args []string) []string {
	if c.FrameworksDir != "" {
		return append(args, "--frameworks", c.FrameworksDir)
	}
	return args
}

// run invokes the runner in dir and returns attest's combined output.
func (c *ExecCompiler) run(ctx context.Context, dir, bin string, args []string) (string, error) {
	r := c.runner
	if r == nil {
		r = execRunner{}
	}
	out, err := r.Run(ctx, dir, bin, args...)
	text := strings.TrimSpace(string(out))
	if err != nil {
		detail := text
		if detail == "" {
			detail = "(no output)"
		}
		return text, fmt.Errorf("%s %s (in %s): %w\n%s", bin, strings.Join(args, " "), dir, err, detail)
	}
	return text, nil
}
