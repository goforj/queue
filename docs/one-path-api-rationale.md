# One-Path API Rationale

The public API is intentionally queue-first:

- app code constructs `*queue.Queue`
- app code uses `queue.New(...)` or driver-module `New(...)` / `NewWithConfig(...)`

Low-level runtime seams still exist internally for driver construction and tests, but they are not part of the public application-facing API because:

- they add a second mental model for normal users
- they make docs/examples harder to keep canonical
- they freeze internal construction seams that need to evolve

This keeps the default path simple without removing internal flexibility.
