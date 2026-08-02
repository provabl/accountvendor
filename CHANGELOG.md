# Changelog

All notable changes to vendor will be documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`vendor provision` / `vendor adopt` — the live AWS Organizations vend, adopt-first.** Both
  commands run the same pipeline (resolve type → resolve OU → place → tag → `attest compile`
  pre-flight → write `<account-id>-meta.json`); the only difference is `Adopt` (reversible) vs.
  `Create` (irreversible). Everything locally checkable — catalog, type key, region, target OU —
  is validated **before the first AWS call**, because on the provision path the first AWS write is
  an irreversible `CreateAccount`. `--dry-run` resolves the type and OU and reports the plan
  without creating, adopting, tagging, or compiling.
  - **`CreateAccount` is asynchronous**, so `Create` polls `DescribeCreateAccountStatus` to
    terminal state — the account id doesn't exist until `SUCCEEDED`. A `FAILED` request surfaces
    AWS's `FailureReason` (`EMAIL_ALREADY_EXISTS`, `ACCOUNT_LIMIT_EXCEEDED`, …), which is the
    actionable part; a poll timeout warns the request may still be in flight rather than reading
    as "nothing happened".
  - **The created-but-unplaced case is called out explicitly.** If the account is created and
    placement then fails, the error says the account *exists*, says **not** to re-run create (that
    would vend a second account, and an account can only be closed — 90-day suspension — never
    deleted), and gives the recovery command: `vendor adopt <id> --parent <ou>`.
  - **`adopt` is idempotent and describes-first**: an account already in the target OU is a no-op
    success (so re-adopting to re-apply tags/pre-flight doesn't fail), and a bad account id fails
    on a *read* before any write.
- **OU name → id resolution (`internal/provision/ou.go`)** — required, not optional: ground creates
  its OUs **by name** and `ground-meta.json` carries **no OU ids**, and ground's tree is **nested**
  (`NIHGenomic` / `HIPAAResearch` / `CUIResearch` under `SensitiveResearch`). A breadth-first walk
  from the org root resolves a name; an explicit `ou-…` / `r-…` short-circuits with zero API calls;
  child listing follows `NextToken` (a silently truncated page would make an existing OU look
  missing and misplace an account); an **ambiguous** name is an error listing every candidate id,
  never a guess. Tests mirror ground's real OU layout.
- **`internal/provision/compiler.go` — the `attest compile` pre-flight, against attest's *actual*
  CLI.** Verified against attest's source rather than assumed, which changed the design: `attest
  compile` takes **no account flag** (it reads `.attest/sre.yaml` from its working directory), and
  `compile --frameworks` is a frameworks **directory** path, not a framework id list. So the
  pre-flight is a three-step sequence — `attest init` → `attest frameworks add <ids…>` → `attest
  compile` — run in a **per-account directory** (via `exec.Cmd.Dir`, never `os.Chdir`, which is
  process-global and would race across concurrent vends). Guards attest's fail-open case: `compile`
  prints "No active frameworks" and **exits 0**, so vendor treats that output as a **failed**
  pre-flight — it must not declare an account ready on a pre-flight that compiled nothing. A type
  with no frameworks is likewise refused rather than silently passing.
- **`vendor preflight`** — verifies the calling principal holds the Organizations actions a vend
  needs, via read-only `iam:SimulatePrincipalPolicy` against the caller (it evaluates, it never
  acts). Fail-closed: a credential, identity, simulator, or *empty-result* failure is an error, not
  a silent pass. It checks the full set including `CreateAccount` even for adopt-only use, since
  adopt exists to validate the pipeline before the irreversible create. Remediations name the
  **management account** — a correct policy in a member account denies every `organizations:*`
  action. Documented per-action and per-command in `docs/required-permissions.md`.
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
