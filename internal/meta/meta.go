// SPDX-FileCopyrightText: 2026 Playground Logic LLC
// SPDX-License-Identifier: Apache-2.0

// Package meta is vendor's manifest boundary: it reads ground's ground-meta.json
// (for org/region context) and writes the per-account <account-id>-meta.json that
// `attest init` consumes — the same standard-interface-plus-meta handoff as
// ground → attest, one level down (ground emits its meta once; vendor emits one
// per vended account).
//
// Design note — read ground-meta LENIENTLY. ground's GroundMeta struct
// (cmd/ground/main.go) is richer than vendor needs and evolves independently, so
// this reader takes only the fields vendor uses and tolerates unknown ones (no
// DisallowUnknownFields) — a new ground field must never break vendor. vendor
// deliberately does NOT assume ground-meta carries OU ids or SCP ARNs (it does
// not today): account placement comes from the operator's `--parent` flag, not
// from ground-meta. This keeps vendor honest about ground's actual contract
// rather than an aspirational one.
package meta

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// GroundMeta is the subset of ground's ground-meta.json vendor reads. It is
// intentionally a lenient superset-tolerant view: only the fields vendor uses,
// with unknown fields ignored so ground can add fields without breaking vendor.
type GroundMeta struct {
	GroundVersion       string `json:"ground_version"`
	Region              string `json:"region"`
	LogArchiveAccountID string `json:"log_archive_account_id,omitempty"`
	// IdentityCenterInstanceARN, when present, is carried through to the account
	// meta so attest can associate the vended account's access with the org SSO.
	IdentityCenterInstanceARN string `json:"identity_center_instance_arn,omitempty"`
}

// ReadGroundMeta parses a ground-meta.json. Lenient by design (unknown fields are
// ignored). A malformed file, or one missing the region vendor needs for context,
// is an error.
func ReadGroundMeta(r io.Reader) (*GroundMeta, error) {
	var m GroundMeta
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("parse ground-meta: %w", err)
	}
	if m.Region == "" {
		return nil, fmt.Errorf("ground-meta has no region — is this a valid ground export?")
	}
	return &m, nil
}

// ReadGroundMetaFile is ReadGroundMeta over a file path.
func ReadGroundMetaFile(path string) (*GroundMeta, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied ground-meta path
	if err != nil {
		return nil, fmt.Errorf("open ground-meta %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return ReadGroundMeta(f)
}

// AccountMeta is the per-account manifest vendor writes for `attest init` to
// consume. It records what vendor provisioned: the account, where it landed, the
// SRE type + its frameworks (so attest knows what to compile against), the tags
// applied, and the region/SSO context inherited from ground-meta.
type AccountMeta struct {
	SchemaVersion int      `json:"schema_version"`
	AccountID     string   `json:"account_id"`
	AccountName   string   `json:"account_name,omitempty"`
	Region        string   `json:"region"`
	ParentOU      string   `json:"parent_ou,omitempty"` // where vendor placed the account
	SREType       string   `json:"sre_type"`            // catalog key, e.g. "nih-genomics"
	Frameworks    []string `json:"frameworks"`          // from the catalog type; drives attest compile
	// Tags are the data-class/handling tags vendor applied at vend time.
	Tags map[string]string `json:"tags,omitempty"`
	// IdentityCenterInstanceARN is carried through from ground-meta when present.
	IdentityCenterInstanceARN string `json:"identity_center_instance_arn,omitempty"`
	// VendorVersion + VendedAt record the provenance of this manifest.
	VendorVersion string    `json:"vendor_version"`
	VendedAt      time.Time `json:"vended_at"`
}

// AccountMetaSchemaVersion is the account-meta schema version (attest's reader
// pins the version it understands; a breaking shape change bumps it).
const AccountMetaSchemaVersion = 1

// Write serialises the account meta to w as indented JSON.
func (a *AccountMeta) Write(w io.Writer) error {
	if a.AccountID == "" || a.Region == "" || a.SREType == "" {
		return fmt.Errorf("account meta requires account_id, region, and sre_type")
	}
	if a.SchemaVersion == 0 {
		a.SchemaVersion = AccountMetaSchemaVersion
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(a)
}

// WriteFile writes the account meta to a file path.
func (a *AccountMeta) WriteFile(path string) error {
	f, err := os.Create(path) //nolint:gosec // operator-supplied out path
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := a.Write(f); err != nil {
		return err
	}
	return f.Close()
}
