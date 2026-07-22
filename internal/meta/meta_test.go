// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestReadGroundMeta_TakesNeededFields(t *testing.T) {
	m, err := ReadGroundMeta(strings.NewReader(`{
	  "ground_version": "0.3.0",
	  "region": "us-east-1",
	  "log_archive_account_id": "111122223333",
	  "identity_center_instance_arn": "arn:aws:sso:::instance/ssoins-abc"
	}`))
	if err != nil {
		t.Fatalf("ReadGroundMeta: %v", err)
	}
	if m.Region != "us-east-1" || m.LogArchiveAccountID != "111122223333" {
		t.Errorf("fields wrong: %+v", m)
	}
	if m.IdentityCenterInstanceARN == "" {
		t.Error("identity center ARN should be read")
	}
}

// ground's real GroundMeta is richer than vendor's view; unknown fields (external
// services, data endpoints, probe results, config flags) must be tolerated — a new
// ground field can't break vendor.
func TestReadGroundMeta_ToleratesUnknownFields(t *testing.T) {
	full := `{
	  "ground_version": "0.3.0",
	  "region": "us-west-2",
	  "cloudtrail_enabled": true,
	  "config_enabled": true,
	  "external_services": [{"name": "globus", "category": "data-transfer"}],
	  "data_endpoints": [{"name": "dbgap"}],
	  "probe_results": {"globus": null}
	}`
	m, err := ReadGroundMeta(strings.NewReader(full))
	if err != nil {
		t.Fatalf("must tolerate ground's richer meta: %v", err)
	}
	if m.Region != "us-west-2" {
		t.Errorf("region = %q", m.Region)
	}
}

func TestReadGroundMeta_RequiresRegion(t *testing.T) {
	if _, err := ReadGroundMeta(strings.NewReader(`{"ground_version":"0.3.0"}`)); err == nil {
		t.Error("ground-meta without a region must error")
	}
}

func TestReadGroundMeta_Malformed(t *testing.T) {
	if _, err := ReadGroundMeta(strings.NewReader(`not json`)); err == nil {
		t.Error("malformed ground-meta must error")
	}
}

func TestAccountMeta_WriteRoundTrip(t *testing.T) {
	a := &AccountMeta{
		AccountID:                 "123456789012",
		AccountName:               "chen-genomics",
		Region:                    "us-east-1",
		ParentOU:                  "ou-root-sensitive",
		SREType:                   "nih-genomics",
		Frameworks:                []string{"nih-gds", "nist-800-53-moderate"},
		Tags:                      map[string]string{"data-class": "GENOMIC"},
		IdentityCenterInstanceARN: "arn:aws:sso:::instance/ssoins-abc",
		VendorVersion:             "0.1.0",
		VendedAt:                  time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC),
	}
	var buf bytes.Buffer
	if err := a.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// schema version defaulted on write
	if a.SchemaVersion != AccountMetaSchemaVersion {
		t.Errorf("SchemaVersion not defaulted, got %d", a.SchemaVersion)
	}
	// round-trips back to the same content
	var got AccountMeta
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AccountID != "123456789012" || got.SREType != "nih-genomics" || len(got.Frameworks) != 2 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.SchemaVersion != AccountMetaSchemaVersion {
		t.Errorf("schema_version = %d, want %d", got.SchemaVersion, AccountMetaSchemaVersion)
	}
}

func TestAccountMeta_WriteRequiresCoreFields(t *testing.T) {
	cases := map[string]*AccountMeta{
		"no account": {Region: "us-east-1", SREType: "t"},
		"no region":  {AccountID: "1", SREType: "t"},
		"no type":    {AccountID: "1", Region: "us-east-1"},
	}
	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			if err := a.Write(&bytes.Buffer{}); err == nil {
				t.Errorf("%s: expected a validation error", name)
			}
		})
	}
}
