// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleCatalog = `{
  "schema_version": 1,
  "types": [
    {"key":"nih-genomics","name":"NIH genomics SRE","frameworks":["nih-gds"],"ou":"SensitiveResearch","tags":{"data-class":"GENOMIC"}},
    {"key":"cui-l2","name":"CUI L2 SRE","frameworks":["cmmc-level-2"]}
  ]
}`

func writeCatalog(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCatalogList(t *testing.T) {
	path := writeCatalog(t, sampleCatalog)
	cmd := catalogCmd()
	cmd.SetArgs([]string{"list", "--catalog", path})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("catalog list: %v", err)
	}
}

func TestCatalogShow_Found(t *testing.T) {
	path := writeCatalog(t, sampleCatalog)
	cmd := catalogCmd()
	cmd.SetArgs([]string{"show", "nih-genomics", "--catalog", path})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("catalog show: %v", err)
	}
}

func TestCatalogShow_Missing(t *testing.T) {
	path := writeCatalog(t, sampleCatalog)
	cmd := catalogCmd()
	cmd.SetArgs([]string{"show", "nope", "--catalog", path})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Error("expected an error for an unknown type key")
	}
}

// A malformed catalog surfaces as an error (validation lives in the shared schema).
func TestCatalog_InvalidFileErrors(t *testing.T) {
	path := writeCatalog(t, `{"schema_version": 1, "types": [{"key":"bad","name":"","frameworks":[]}]}`)
	cmd := catalogCmd()
	cmd.SetArgs([]string{"list", "--catalog", path})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Error("expected a validation error for a malformed catalog")
	}
}

func TestCatalog_MissingFileErrors(t *testing.T) {
	cmd := catalogCmd()
	cmd.SetArgs([]string{"list", "--catalog", filepath.Join(t.TempDir(), "nope.json")})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil {
		t.Error("expected an error when the catalog file is absent")
	}
}
