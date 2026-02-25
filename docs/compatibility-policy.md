# Compatibility Policy

This document defines how queue backend compatibility is communicated for releases.

## Policy

- The project documents backend/runtime compatibility expectations in release notes and supporting docs.
- Capability differences are documented separately (see `docs/backend-guarantees.md`).

## What "supported" means here

- The backend/version combination is exercised in CI integration tests for the release line.
- Behavior is still subject to the documented guarantee/capability matrix (not every backend supports every feature).

## CI evidence source

- CI workflows and integration suites in this repository are the source of compatibility evidence.
- Release notes should link to the relevant CI-backed docs for the tagged release.
