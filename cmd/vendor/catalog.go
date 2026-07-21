// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/provabl/schemas/catalog"
)

func catalogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "List and inspect the SRE-type catalog vendor can vend",
		Long: `Inspect the SRE-type catalog: each type (e.g. nih-genomics, cui-l2) maps to the
compliance frameworks it must satisfy, its target OU, the tags every account of
that type must carry, and the baseline stacks to apply. The catalog schema is
shared with attest (attest#98) via github.com/provabl/schemas — one schema, not
two.`,
	}
	cmd.AddCommand(catalogListCmd(), catalogShowCmd())
	return cmd
}

// loadCatalog reads + validates the catalog file at path via the shared schema.
func loadCatalog(path string) (*catalog.Catalog, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied catalog path
	if err != nil {
		return nil, fmt.Errorf("open catalog %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return catalog.Load(f)
}

func catalogListCmd() *cobra.Command {
	var catalogPath string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the SRE types in the catalog",
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := loadCatalog(catalogPath)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "KEY\tNAME\tFRAMEWORKS\tOU")
			for _, key := range c.Keys() {
				t, _ := c.Get(key)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.Key, t.Name, join(t.Frameworks), dash(t.OU))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&catalogPath, "catalog", "catalog.json", "path to the SRE-type catalog file")
	return cmd
}

func catalogShowCmd() *cobra.Command {
	var catalogPath string
	cmd := &cobra.Command{
		Use:   "show <type-key>",
		Short: "Show one SRE type's frameworks, OU, tags, and baseline stacks",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := loadCatalog(catalogPath)
			if err != nil {
				return err
			}
			t, ok := c.Get(args[0])
			if !ok {
				return fmt.Errorf("no SRE type %q in the catalog (see 'vendor catalog list')", args[0])
			}
			fmt.Printf("%s — %s\n", t.Key, t.Name)
			if t.Description != "" {
				fmt.Printf("  %s\n", t.Description)
			}
			fmt.Printf("  frameworks:      %s\n", join(t.Frameworks))
			fmt.Printf("  ou:              %s\n", dash(t.OU))
			fmt.Printf("  baseline stacks: %s\n", dash(join(t.BaselineStacks)))
			if len(t.Tags) > 0 {
				fmt.Println("  tags:")
				for k, v := range t.Tags {
					fmt.Printf("    %s=%s\n", k, v)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&catalogPath, "catalog", "catalog.json", "path to the SRE-type catalog file")
	return cmd
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
