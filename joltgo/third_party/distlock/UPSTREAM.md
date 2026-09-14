# distlock vendored source

- Upstream: https://github.com/beijian128/distlock
- Pinned commit: `70ac9aed453f20f8bf849678ef9cd17fbb21c5bc`
- Vendored on: 2026-09-14
- License: MIT (see `LICENSE`)

Only one local patch is applied: `go.mod` declares the canonical module path
`github.com/beijian128/distlock` instead of the upstream short path `distlock`.
This lets the root module use a normal local `replace` and keeps
`go mod verify` working. All Go source files match the pinned commit.
