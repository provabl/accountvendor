# vendor — required AWS permissions

`vendor preflight` verifies the calling AWS principal holds these actions, using
read-only `iam:SimulatePrincipalPolicy` against the caller ARN (from
`sts:GetCallerIdentity`). It **evaluates, it never acts** — running preflight changes
nothing. A denied action prints a remediation and the command exits non-zero.

**Run it before `vendor provision`.** `organizations:CreateAccount` is irreversible
*and* asynchronous: if a later step in the vend (placement, tagging) is denied, the
account already exists, and an AWS account can only be **closed** (90-day suspension),
never deleted — its root email is never reusable. A half-vended account is not cleanly
recoverable, so the cheap move is to check permissions first.

**All `organizations:*` actions require the organization's MANAGEMENT account.** An
otherwise-correct policy attached in a member account will deny every one of them.
This is the most common cause of a preflight failure.

| Action | Needed by |
|--------|-----------|
| `sts:GetCallerIdentity` | preflight itself (resolves the caller ARN to simulate) |
| `iam:SimulatePrincipalPolicy` | preflight itself (the permission self-check) |
| `organizations:ListRoots` | OU resolution — the root to start the name→id walk from |
| `organizations:ListOrganizationalUnitsForParent` | OU resolution — walks the (nested) OU tree to resolve a name to an `ou-…` id |
| `organizations:ListParents` | `provision` + `adopt` — the account's current parent, needed as `MoveAccount`'s source |
| `organizations:DescribeAccount` | `adopt` — confirms the account exists *before* any write |
| `organizations:MoveAccount` | `provision` + `adopt` — places the account under the SRE type's OU |
| `organizations:TagResource` | `provision` + `adopt` — applies the SRE type's data-class tags |
| `organizations:CreateAccount` | `provision` only — **irreversible** |
| `organizations:DescribeCreateAccountStatus` | `provision` only — `CreateAccount` is async; the account id only exists at `SUCCEEDED`, so polling is mandatory |

**Why OU resolution needs list permissions.** ground creates its OUs **by name** and
`ground-meta.json` carries **no OU ids**, so vendor resolves `SensitiveResearch` →
`ou-…` itself by walking the tree. ground's tree is also **nested** (`NIHGenomic`,
`HIPAAResearch`, `CUIResearch` sit under `SensitiveResearch`), which is why a
single-level list isn't enough. Passing an explicit `--parent ou-…` short-circuits the
walk entirely — no list calls at all — but preflight still checks the actions, because
the catalog's default OU is a name.

**Scoping by command.** To scope a principal narrowly:

| Command | Actions beyond preflight's own two |
|---------|-----------------------------------|
| `vendor catalog list` / `show` | **none — entirely local** |
| `vendor provision --dry-run` | `ListRoots`, `ListOrganizationalUnitsForParent` (read-only) |
| `vendor adopt` | + `DescribeAccount`, `ListParents`, `MoveAccount`, `TagResource` |
| `vendor provision` | + `CreateAccount`, `DescribeCreateAccountStatus` |

`vendor preflight` checks the **full** set, including `CreateAccount`, even though
`adopt` never calls it. That's deliberate: adopt exists to validate the pipeline
*before* the irreversible create, so an operator wants to know their principal is
ready for both. Simulation is read-only, so checking a not-yet-used action costs
nothing. If you are only ever adopting, a denial on `CreateAccount` /
`DescribeCreateAccountStatus` is expected and safe to ignore.

The `attest compile` pre-flight runs as a **subprocess** (`attest`, in a per-account
directory) and uses **attest's** permissions, not vendor's — see attest's own
`docs/required-permissions.md`. Pass `--skip-compile` to omit it (not recommended: the
account is then unproven).

## Boundary

vendor **vends and configures accounts; it makes zero compliance claims** — attest does
that, after `attest scan`. vendor writes no `attest:*` IAM tags; the tags it applies are
the catalog's account-level data-class tags on the account resource itself.
