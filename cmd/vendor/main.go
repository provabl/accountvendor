// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

// Command vendor is the Provabl suite's AWS account vendor: it vends compliant
// AWS accounts on demand into a ground-deployed (or any) org — correct OU
// placement, baseline guardrails, data-class tags, and an `attest compile`
// pre-flight — then exports a per-account meta for `attest init`.
//
// vendor is infrastructure, a **sibling to ground, not part of it**: ground
// deploys the org foundation once; vendor supplies accounts on demand into it.
// It is lighter than Control Tower / Account Factory for Terraform — one binary,
// a shared SRE-type catalog, and a target parent. See the suite spec
// (business/vendor-product-spec.md, in the umbrella) and provabl epic #9.
//
// Boundary: vendor provisions and configures accounts; it makes **zero
// compliance claims** (attest does that, after a scan). It reads ground's
// ground-meta.json, reuses ground's CloudFormation baseline stacks, and shells
// out to `attest compile` — the same standard-interface + meta handoff as
// ground → attest.
//
// The high-consequence, irreversible core (`organizations:CreateAccount` — an
// account can only be closed, not deleted) is built adopt-first: the whole
// pipeline is validated against an existing account (`vendor adopt`, reversible)
// before live account creation is ever exercised.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vendor",
		Short: "AWS account vendor for the Provabl suite",
		Long: `vendor vends compliant AWS accounts on demand into a ground-deployed (or any)
org: correct OU placement, baseline guardrails, data-class tags, and an
'attest compile' pre-flight, then exports a per-account meta for 'attest init'.

Sibling to ground (ground deploys the org once; vendor vends accounts into it);
lighter than Control Tower / Account Factory. vendor makes zero compliance
claims — attest does that, after a scan.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(catalogCmd())
	// Further subcommands land in their own PRs: meta, provision, adopt, preflight, log.
	return cmd
}
