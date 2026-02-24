# Compatibility Policy

This document defines how queue backend compatibility is communicated for releases.

## Policy

- The project documents the backend/runtime versions exercised by CI integration suites.
- Release notes should link the committed `docs/compatibility-matrix.md` from the tagged release.
- Capability differences are documented separately (see `docs/backend-guarantees.md`).

## What "supported" means here

- The backend/version combination is exercised in CI integration tests for the release line.
- Behavior is still subject to the documented guarantee/capability matrix (not every backend supports every feature).

## CI-tested matrix source

- Generated file: `docs/compatibility-matrix.md`
- Generator: `scripts/gen-compatibility-matrix.sh`

The generated matrix is kept in sync by CI checks so version drift is visible in pull requests.
