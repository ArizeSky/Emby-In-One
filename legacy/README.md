# Legacy Node.js implementation

This is the V1.2.1 Node.js/Express implementation of Emby-In-One. It is **not** part of the
shipped product and is not maintained — the Go implementation under `cmd/` and
`internal/` replaced it.

It stays in the tree for one reason: `src/utils/id-rewriter.js` and `src/id-manager.js` are
the reference the Go port's ID virtualisation was written against, and they are still the
place to look when a response-rewriting difference between the two needs explaining. The
older Node tests under `tests/` are similarly historical (most of them no longer pass).

Nothing here participates in a build, a Docker image, or an install. The root
`package.json` keeps only `build:panel` (the Tailwind step for the Go admin panel) and has no
dependencies, so it no longer carries a lock file — run `npm install` inside this directory if
you actually intend to run the old implementation.
