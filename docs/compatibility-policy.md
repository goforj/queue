# Compatibility Policy (Pre-GA Baseline)

This document defines the intended compatibility policy for `queue` as it approaches `v1`.

Before GA, this is a baseline policy draft. Before tagging `v1.0.0`, confirm and publish the final version in release notes.

## Scope

This policy applies to:

- Public Go APIs in package `queue`
- Public behavior claims documented in:
  - `README.md`
  - `docs/backend-guarantees.md`
  - `docs/metrics-contract.md`
- Supported backend driver surface and capability documentation

This policy does **not** guarantee stability for:

- Internal packages / plumbing (for example internal orchestration implementation details)
- Test-only helpers not documented as public API contracts
- Experimental features explicitly marked as such

## Versioning Policy

`queue` follows Semantic Versioning (SemVer) after `v1.0.0`.

### Before `v1.0.0`

- Breaking API changes are allowed
- The project should still prefer migration notes for notable changes
- Public API churn should be minimized as the project moves toward GA

### After `v1.0.0`

- `MAJOR` version:
  - breaking API changes
  - incompatible behavior changes in documented guarantees
- `MINOR` version:
  - backwards-compatible new features
  - additive API surface
- `PATCH` version:
  - backwards-compatible bug fixes and docs/test improvements

## Public API Compatibility (Post-GA Target)

The following are treated as compatibility-sensitive after `v1.0.0`:

- exported names and signatures in package `queue`
- documented semantics of `queue.Queue` methods
- documented backend capability claims in `docs/backend-guarantees.md`

Examples of breaking changes:

- removing/renaming exported `queue` symbols
- changing method signatures or return semantics
- changing documented capability guarantees without a major release (unless previously marked incorrect and fixed with clear release notes)

## Deprecation Policy (Post-GA Target)

For public API replacements/removals:

- Mark deprecated APIs in code comments (`Deprecated:`)
- Document replacement path in release notes / docs
- Maintain deprecated APIs for at least:
  - `1 minor release`, or
  - `90 days`

Use the longer of the two when practical.

Emergency exceptions:

- security or data-loss issues may require shorter deprecation windows
- such exceptions must be called out clearly in release notes

## Behavior and Guarantee Compatibility

Documented guarantees are part of the compatibility surface.

That includes:

- delivery semantics (at-least-once)
- duplicate expectations
- restart/recovery behavior claims
- backend capability matrix entries in `docs/backend-guarantees.md`

Policy:

- If a guarantee changes, update docs and integration tests in the same PR
- If a behavior claim was incorrect, fix the behavior or docs and call out the correction in release notes

## Observability Contract Compatibility

The observability contract is defined by `docs/metrics-contract.md` and `docs/ops-alerts.md`.

After GA, treat these as compatibility-sensitive:

- event kind names used in public docs (`queue.EventKind`, `queue.WorkflowEventKind`)
- required field names/labels documented in `docs/metrics-contract.md`

Recommended policy after GA:

- avoid removing/renaming event kinds in minor/patch releases
- when adding fields/labels, prefer additive changes
- deprecate renamed metrics/labels where practical before removal

## Backend Support Policy (Baseline)

Document supported backends and tested coverage in release notes and docs.

When adding a backend:

- add it to the shared integration scenario suite (or document capability gates)
- document guarantees/caveats in `docs/backend-guarantees.md`

When changing/removing backend support:

- announce in release notes
- provide migration guidance when feasible

## Upgrade/Migration Testing Expectations

Before GA and for release candidates:

- run unit tests and full integration suite
- run backend-specific integration scenarios for changed backends
- run upgrade/migration tests for DB-backed behavior/schema changes (when applicable)

See:

- `docs/ga-readiness.md`

## GA Completion Criteria for This Document

Before `v1.0.0`, finalize:

- deprecation window duration(s)
- list of experimental vs stable surfaces (if any)
- supported backend/version policy and maintenance expectations
