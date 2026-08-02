// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/provabl/accountvendor/internal/preflight"
)

func preflightCmd() *cobra.Command {
	var region string
	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Verify the calling principal holds the IAM permissions a vend needs",
		Long: `Check that the calling AWS principal can perform vendor's AWS Organizations
actions (OU resolution, account placement, tagging, and account creation), via
read-only iam:SimulatePrincipalPolicy against the caller — it evaluates, it does
not act. A denied action prints a remediation and the command exits non-zero.

Run this BEFORE 'vendor provision'. organizations:CreateAccount is irreversible
and asynchronous: if a later step is denied, the account already exists and can
only be closed (90-day suspension), never deleted. Checking first is how you
avoid a half-vended account.

Requires credentials for the organization's MANAGEMENT account — an otherwise
correct policy in a member account will deny every organizations:* action here.
See docs/required-permissions.md.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPreflight(preflight.CheckCallerPermissions(cmd.Context(), region))
		},
	}
	cmd.Flags().StringVar(&region, "region", "us-east-1", "AWS region")
	return cmd
}

// runPreflight renders preflight results and returns a non-nil error if any failed.
func runPreflight(results []preflight.Result) error {
	failures := 0
	for _, r := range results {
		if r.Status {
			fmt.Printf("  ✓ %s\n", r.Name)
			continue
		}
		failures++
		fmt.Printf("  ✗ %s: %s\n", r.Name, r.Detail)
		if r.Remediation != "" {
			fmt.Printf("      Remediation: %s\n", r.Remediation)
		}
	}
	fmt.Println()
	if failures > 0 {
		return fmt.Errorf("preflight failed: %d required permission(s) missing", failures)
	}
	fmt.Println("✓ All required permissions present")
	return nil
}
