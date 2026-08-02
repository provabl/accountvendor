// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/spf13/cobra"

	"github.com/provabl/accountvendor/internal/meta"
	"github.com/provabl/accountvendor/internal/provision"
)

// vendOptions are the flags shared by `provision` and `adopt` — the two commands
// are the same pipeline with one difference (create vs. adopt placement), so they
// share flags and the run path deliberately.
type vendOptions struct {
	catalogPath string
	groundMeta  string
	sreType     string
	parent      string
	region      string
	outDir      string
	attestBin   string
	frameworks  string
	skipPrereq  bool
	skipCompile bool
	dryRun      bool
}

func (o *vendOptions) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&o.catalogPath, "catalog", "catalog.json", "path to the SRE-type catalog file")
	f.StringVar(&o.groundMeta, "ground-meta", "", "path to ground-meta.json (region/SSO context from ground)")
	f.StringVar(&o.sreType, "type", "", "SRE type key from the catalog (required; see 'vendor catalog list')")
	f.StringVar(&o.parent, "parent", "", "target OU name or ou-… id (defaults to the SRE type's OU)")
	f.StringVar(&o.region, "region", "", "AWS region (defaults to ground-meta's region)")
	f.StringVar(&o.outDir, "out-dir", ".", "directory to write <account-id>-meta.json into")
	f.StringVar(&o.attestBin, "attest-bin", "attest", "attest executable for the compile pre-flight")
	f.StringVar(&o.frameworks, "frameworks-dir", "", "attest frameworks DIRECTORY (not a framework list); empty uses attest's default")
	f.BoolVar(&o.skipPrereq, "skip-prereq-check", false, "pass --skip-prereq-check to attest init")
	f.BoolVar(&o.skipCompile, "skip-compile", false, "skip the attest compile pre-flight (NOT recommended: the account is then unproven)")
	f.BoolVar(&o.dryRun, "dry-run", false, "resolve type + OU and report what would happen, without creating, adopting, tagging, or compiling")
	_ = cmd.MarkFlagRequired("type")
}

func provisionCmd() *cobra.Command {
	o := &vendOptions{}
	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Vend a NEW AWS account for an SRE type (irreversible)",
		Long: `Creates a new AWS account, places it in the SRE type's OU, applies the type's
tags, runs the 'attest compile' pre-flight, and writes <account-id>-meta.json for
'attest init'.

IRREVERSIBLE: an AWS account can only be closed (90-day suspension), never
deleted, and its root email can never be reused. Validate your catalog, OU, and
permissions with 'vendor adopt' (reversible) or --dry-run first.

Requires credentials for the organization's MANAGEMENT account.`,
		Example: `  # Dry run first — resolve the type and OU, touch nothing
  vendor provision --type nih-genomics --name chen-genomics --email aws-chen@uni.edu --dry-run

  # Vend for real
  vendor provision --type nih-genomics --name chen-genomics --email aws-chen@uni.edu \
    --ground-meta ground-meta.json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name, _ := cmd.Flags().GetString("name")
			email, _ := cmd.Flags().GetString("email")
			return runVend(cmd.Context(), o, provision.Request{
				Type: o.sreType, Name: name, Email: email, ParentOU: o.parent,
			})
		},
	}
	o.bind(cmd)
	cmd.Flags().String("name", "", "account name (required)")
	cmd.Flags().String("email", "", "unique root email for the new account (required)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("email")
	return cmd
}

func adoptCmd() *cobra.Command {
	o := &vendOptions{}
	cmd := &cobra.Command{
		Use:   "adopt <account-id>",
		Short: "Retrofit an EXISTING account to an SRE type (reversible)",
		Long: `Places an existing AWS account in the SRE type's OU, applies the type's tags,
runs the 'attest compile' pre-flight, and writes <account-id>-meta.json.

Reversible — nothing is created, so this is the recommended way to validate the
whole vend pipeline (catalog, OU resolution, tagging, compile, manifest) before
running the irreversible 'vendor provision'. Also the way to bring a
hand-created account under vendor's management.

Idempotent: re-adopting an account already in the target OU re-applies tags and
the pre-flight without error.

Requires credentials for the organization's MANAGEMENT account.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVend(cmd.Context(), o, provision.Request{
				Type: o.sreType, AdoptAccountID: args[0], ParentOU: o.parent,
			})
		},
	}
	o.bind(cmd)
	return cmd
}

// runVend is the shared pipeline for provision and adopt.
func runVend(ctx context.Context, o *vendOptions, req provision.Request) error {
	cat, err := loadCatalog(o.catalogPath)
	if err != nil {
		return err
	}

	// ground-meta is optional; without it, --region carries the context.
	g := &meta.GroundMeta{Region: o.region}
	if o.groundMeta != "" {
		g, err = meta.ReadGroundMetaFile(o.groundMeta)
		if err != nil {
			return err
		}
		if o.region != "" {
			g.Region = o.region // explicit flag wins
		}
	}
	if g.Region == "" {
		return fmt.Errorf("no region: pass --region or --ground-meta")
	}

	t, ok := cat.Get(req.Type)
	if !ok {
		return fmt.Errorf("unknown SRE type %q (see 'vendor catalog list')", req.Type)
	}

	// Decide the target OU *name* before touching AWS. Everything above this point
	// is local validation, deliberately: on the provision path the first AWS write
	// is an irreversible CreateAccount, so a bad catalog, an unknown type, or a
	// missing target must fail on a local read.
	target := req.ParentOU
	if target == "" {
		target = t.OU
	}
	if target == "" {
		return fmt.Errorf("no target OU: type %q has no default OU, and no --parent was given", t.Key)
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(g.Region))
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	prov := provision.NewAWSProvisioner(cfg)

	// Resolve the OU name → id. ground names its OUs (and nests some), and
	// ground-meta carries no OU ids, so this lookup is required, not optional.
	ouID, err := provision.ResolveOU(ctx, prov, target)
	if err != nil {
		if provision.IsNotInOrgErr(err) {
			return fmt.Errorf("%w\n  vendor must run against the organization's MANAGEMENT account", err)
		}
		return err
	}
	req.ParentOU = ouID

	action := fmt.Sprintf("CREATE new account %q (%s)", req.Name, req.Email)
	if req.AdoptAccountID != "" {
		action = fmt.Sprintf("ADOPT existing account %s", req.AdoptAccountID)
	}
	fmt.Printf("SRE type:   %s (%s)\n", t.Key, t.Name)
	fmt.Printf("frameworks: %v\n", t.Frameworks)
	fmt.Printf("target OU:  %s → %s\n", target, ouID)
	fmt.Printf("action:     %s\n", action)

	if o.dryRun {
		fmt.Println("\n--dry-run: nothing was created, adopted, tagged, or compiled.")
		return nil
	}

	// The compile pre-flight runs attest in a per-account directory, because
	// attest is directory-scoped (it reads .attest/sre.yaml from its cwd).
	var compiler provision.Compiler = skipCompiler{}
	if !o.skipCompile {
		ec := provision.NewExecCompiler(o.attestBin, g.Region, func(id string) string {
			return filepath.Join(o.outDir, id)
		})
		ec.FrameworksDir = o.frameworks
		ec.SkipPrereqCheck = o.skipPrereq
		compiler = ec
	} else {
		fmt.Println("\nWARNING: --skip-compile — the account's policy will NOT be compiled, so vendor")
		fmt.Println("         cannot claim its baseline is in place. Run 'attest compile' yourself.")
	}

	am, err := provision.New(prov, compiler, g, cat, version).Vend(ctx, req)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(o.outDir, 0o750); err != nil {
		return fmt.Errorf("create output directory %s: %w", o.outDir, err)
	}
	outPath := filepath.Join(o.outDir, am.AccountID+"-meta.json")
	if err := am.WriteFile(outPath); err != nil {
		return err
	}
	fmt.Printf("\n✓ account %s ready — wrote %s\n", am.AccountID, outPath)
	fmt.Printf("  next: attest init --ground-meta %s   (in %s)\n", outPath, filepath.Join(o.outDir, am.AccountID))
	return nil
}

// skipCompiler satisfies Compiler when --skip-compile is set. It is explicit
// rather than a nil check so the orchestrator's fail-closed path stays uniform.
type skipCompiler struct{}

func (skipCompiler) Compile(context.Context, string, []string) error { return nil }
