# CLAUDE.md

Guidance for AI agents working in this repository.

## Goal

An internet-hostable MCP server for Obsidian vaults. One Docker container:

1. Logs into Obsidian Sync using the **official** headless client
   (`obsidian-headless` npm package, the `ob` CLI) — never a
   reverse-engineered sync protocol.
2. Bootstraps the configured vaults to `~/vaults/<name>` and keeps them
   continuously synced.
3. Serves MCP tools over streamable HTTP to read, search, create, edit,
   move, and delete notes. Writes propagate back to the user's devices
   through Obsidian Sync.

Deliberate MVP boundaries (do not "fix" these without being asked):

- Text/markdown only; no charts, canvases, or attachments handling.
- Auth is a single static bearer token (`MCP_AUTH_TOKEN`); OAuth/MCP auth
  is future work.
- The Obsidian account must not have MFA.
- Deletes are soft by default: notes move to the vault's `.trash`
  (Obsidian's own convention) so they sync and stay recoverable.
- `read_note` returns at most 10,240 characters per call
  (`vault.ReadPageSize`) with `offset`/`next_offset` paging — chosen
  deliberately for LLM context-window hygiene.

## Architecture

```
cmd/obsidian-mcp/      thin main: config -> bootstrap -> serve (testable run())
internal/config/       env parsing/validation (Getenv + rand injected for tests)
internal/bootstrap/    drives the ob CLI: login, sync-setup per vault, and a
                       supervisor that restarts `ob sync --continuous` with
                       exponential backoff (reset after stable runs)
internal/vault/        sandboxed filesystem ops rooted at one vault; ALL path
                       handling goes through resolve(), which rejects absolute
                       paths and `..` escapes
internal/search/       ripgrep runner; parses `rg --json` events; hidden dirs
                       (.obsidian, .trash) excluded because rg skips hidden
                       files by default; --no-ignore so stray ignore files
                       can't hide notes
internal/server/       MCP tool registration (official modelcontextprotocol/
                       go-sdk, typed handlers), HTTP handler with bearer auth
                       middleware and unauthenticated /healthz
```

Key invariants:

- **Fail-fast boot**: any login or `sync-setup` failure exits non-zero;
  never start serving a partially configured vault set.
- **Path sandboxing lives in `internal/vault`**, not in tool handlers.
  New file operations must use `resolve()`.
- **Secrets never reach logs**: `bootstrap.redact()` masks `--password`
  values; keep that property when adding ob invocations.
- **External processes are injected for tests**: `ob` via a fake script on
  PATH, ripgrep via `search.RunFunc`. Follow the same pattern for new
  process dependencies; do not mock `os/exec` any other way.
- Multiple vaults are served by one process; vault names come from
  `OBSIDIAN_VAULTS` (`Name` or `Name:e2e-password`, comma-separated) with
  `OBSIDIAN_VAULT_PASSWORD` as the shared fallback.

The Docker image is three stages: Go builder → `node:22-alpine` stage that
npm-installs `obsidian-headless` (better-sqlite3 has no musl prebuilds, so
a node-gyp toolchain is installed there and build intermediates stripped) →
bare `alpine` runtime with apk `nodejs` (same Node 22 ABI), `ripgrep`,
`tini`, running as a non-root user. Keep the runtime stage minimal; the
image-size target is "as small as possible" (~142MB today).

## Methodology

TDD is crucial here — treat it as a regular checkpoint, file by file and
feature by feature, not something to backfill at the end.

- **Bugs**: reproduce with a failing test first, then fix, then confirm the
  test passes. No bug fix lands without the test that would have caught it.
- **New features / builds**: build the piece, test it, and only then
  proceed to the next piece. After each file or feature, stop and run the
  full check (`gofmt`, `go vet`, `go test ./...`) before moving on — do not
  batch several features and test at the end.

## Quality gates

CI (`.github/workflows/ci.yml`) enforces, in order:

1. `gofmt -l .` must be empty.
2. `go vet ./...` must pass.
3. `go test ./... -coverprofile=coverage.out -covermode=atomic` must pass.
4. **Total coverage must be ≥ 95%** (currently ~96.7%). If you add code,
   add tests in the same change; prefer refactoring untestable error
   branches away (injection, restructuring) over excluding them.
5. On push to `main` or `v*` tags: multi-arch (amd64/arm64) image publish
   to `ghcr.io/andyjmorgan/obsidian-hosted-mcp`.

Conventions:

- Strict idiomatic Go; no panics in library code; errors wrapped with
  `%w` and enough context to act on.
- Tests use real filesystems (`t.TempDir()`), real ripgrep when present
  (skip otherwise), fake `ob` shell scripts on PATH, and a real MCP client
  session over HTTP for end-to-end coverage — keep new tests in that style
  rather than introducing mocking frameworks.
- Run before committing:
  `gofmt -l . && go vet ./... && go test ./... -coverprofile=coverage.out && go tool cover -func=coverage.out | tail -1`

## Reference material (not committed)

`.reference/` is gitignored and may contain clones of
`kepano/obsidian-skills` and `MarkusPfundstein/mcp-obsidian` for
inspiration. Never commit it. The closest prior art is
`alexjbarnes/vault-sync`; this project intentionally differs by using the
official headless client and serving multiple vaults per container.
