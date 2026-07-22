# Changelog

All notable changes to vendor will be documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`internal/provision` — the vend orchestration (seams, no AWS yet)**: `Orchestrator.Vend` resolves
  the SRE type from the catalog, places the account (via a `Provisioner` seam — **`Adopt`** an existing
  account *or* `Create` a new one), applies the type's tags, runs the `attest compile` pre-flight (via
  a `Compiler` seam), and produces the account meta. Fail-closed ordering: an unknown type, a missing
  target OU, a placement failure, or a compile failure each stop *before* a manifest is emitted —
  vendor never declares an account ready if its policy didn't compile. Built **adopt-first**: `Adopt`
  (reversible) and `Create` (irreversible) share the same post-placement path, so the whole pipeline
  is validated via adopt before live `CreateAccount` is exercised. Fully fake-tested (create / adopt /
  unknown-type / no-OU / parent-override / missing-name-email / compile-fail-no-meta /
  placement-fail-stops).
- **`internal/meta` — the manifest boundary**: reads ground's `ground-meta.json` **leniently** (only
  the fields vendor needs — region, SSO ARN — tolerating ground's richer, evolving struct so a new
  ground field can't break vendor) and writes the per-account `<account-id>-meta.json` that
  `attest init` consumes (schema-versioned; account id, region, OU, SRE type + frameworks, tags, SSO,
  provenance). Honest to ground's *actual* contract: vendor does **not** assume ground-meta carries OU
  ids (it doesn't) — placement comes from `--parent`. Fully unit-tested (round-trips + unknown-field
  tolerance + validation).

- **Initial repo scaffold** — `vendor`, the Provabl suite's AWS account vendor (infrastructure layer,
  **sibling to ground** — ground deploys the org once, vendor vends accounts into it on demand). Go
  1.26.5, Apache-2.0 / Playground Logic LLC, cobra CLI root, Makefile, CI (Check + Lint) + weekly
  Security Scan. vendor makes **zero compliance claims** (attest does, after a scan). See
  `business/vendor-product-spec.md` and provabl epic #9. This session builds the AWS-free foundation;
  the live account operations land adopt-first (validate the pipeline against an existing account
  before ever calling the irreversible `organizations:CreateAccount`).
- **`vendor catalog list` / `show`** — inspect the SRE-type catalog: each type (e.g. `nih-genomics`,
  `cui-l2`) maps to its compliance frameworks, target OU, required tags, and baseline stacks. The
  catalog schema is imported from [`github.com/provabl/schemas`](https://github.com/provabl/schemas)
  (`catalog` package, v0.1.0) — the **same schema attest uses** (attest#98), one source of truth, not
  two. `--catalog <path>` reads + validates a catalog file via the shared loader. Fully tested
  (list / show-found / show-missing / invalid-file / missing-file).
