# HANDOVER — Test the Docker Compose stack and the MCP server

Audience: a local coding agent picking this repo up to validate the Docker
Compose deployment and the MCP server end to end, ideally with real
Obsidian Sync credentials. Read `CLAUDE.md` first for goal, architecture,
methodology (TDD: reproduce → fix → re-test), and quality gates.

## 1. Current state

Branch: `claude/obsidian-mcp-server-vlcx2m` (repo has no `main` history yet).

| Area | State |
| --- | --- |
| Go code (`cmd/`, `internal/`) | Complete MVP, builds clean, `go vet` clean, gofmt clean |
| Tests | All passing; **96.7% total coverage** (CI gate is 95%) |
| Dockerfile | 3-stage build, ~142MB final image, verified building and running |
| docker-compose.yml + .env.example | Written, **never run — this is your primary job** |
| CI (`.github/workflows/ci.yml`) | Written, never executed (no push to `main` yet) |
| Real-credential sync | **Never tested.** Only fail-fast with fake credentials was verified |

What has already been verified in a sandbox (2026-07-03):

- `docker build` succeeds; image contains working `ob` 0.0.12, ripgrep 14.1.1,
  and the Go binary, running as non-root user `obsidian`.
- With fake credentials the container reaches Obsidian's real API, `ob`
  returns "Login failed…", and the container exits 1 with a `fatal:` log —
  fail-fast works.
- The full MCP handshake over HTTP was exercised with curl against the
  in-process server (section 5's commands are copy-paste verified).
- Unit/integration suite: real tempdir filesystems, real ripgrep, fake `ob`
  shell scripts on PATH, and a real MCP client session over the streamable
  HTTP transport including auth rejection.

What has **never** been exercised (your test targets):

1. `docker compose up` (volume persistence, healthcheck, env plumbing).
2. `ob login` / `ob sync-setup` / `ob sync --continuous` against a real
   account: credential persistence across restarts, sync-setup idempotency
   on an already-connected path, device-name behavior on re-registration.
3. A sync round trip: MCP write → appears on another device; device write →
   readable via MCP.
4. Behavior with large vaults (search latency, initial sync time).

## 2. Prerequisites

- Docker with Compose v2 (`docker compose version`).
- Go 1.25+ and ripgrep if you also want to run the test suite locally.
- An Obsidian account with a **Sync subscription**, **MFA disabled**, at
  least one remote vault, and its end-to-end encryption password. A
  throwaway/dedicated test vault is strongly recommended: the tests below
  create, edit, move, and delete notes.
- No corporate TLS-intercepting proxy, or you'll need to inject its CA into
  the build (the committed Dockerfile assumes a clean network; see §8).

## 3. Sanity pass before touching Docker

```sh
gofmt -l .            # expect empty
go vet ./...          # expect silence
go test ./... -coverprofile=coverage.out -covermode=atomic
go tool cover -func=coverage.out | tail -1   # expect >= 95%
```

If anything fails here, stop and fix (failing test first — see CLAUDE.md
methodology) before doing container work.

## 4. Compose bring-up

```sh
cp .env.example .env
# edit .env: OBSIDIAN_EMAIL, OBSIDIAN_PASSWORD, OBSIDIAN_VAULTS,
# MCP_AUTH_TOKEN (openssl rand -hex 32), optionally OBSIDIAN_DEVICE_NAME.
docker compose up --build
```

Expected boot sequence in logs (slog text format):

1. `logging in to Obsidian email=…`
2. Per vault: `connecting vault vault=… path=/home/obsidian/vaults/… device=…`
3. Per vault: `starting continuous sync vault=…`
4. `MCP server listening addr=[::]:8080 vaults=N`

Checks:

- `curl -s http://localhost:8080/healthz` → `ok` (no auth required).
- `docker compose ps` → healthcheck reports healthy after start_period.
- `docker compose exec obsidian-mcp ls /home/obsidian/vaults/<VaultName>` →
  vault content appears once initial sync completes (give large vaults
  time; watch the `ob` output lines in the logs).
- Container runs as non-root: `docker compose exec obsidian-mcp whoami` →
  `obsidian`.

Failure-mode checks (each should exit the container non-zero with a
`fatal:` line, and compose `restart: unless-stopped` will loop it — that's
expected):

- Wrong password → `fatal: ob login failed…`
- Nonexistent vault name in `OBSIDIAN_VAULTS` → `fatal: ob sync-setup failed for vault …`
- Missing `MCP_AUTH_TOKEN` → `fatal: MCP_AUTH_TOKEN must be set…`

## 5. MCP protocol smoke test (copy-paste verified)

The endpoint is streamable HTTP at `/`. Every request needs the bearer
token and BOTH accept types. Sessions: `initialize` returns an
`Mcp-Session-Id` response header that must be echoed on every subsequent
request.

```sh
TOKEN=<your MCP_AUTH_TOKEN>
URL=http://localhost:8080/

# 1) initialize — capture the session id from the response headers
SID=$(curl -s -D - -o /dev/null "$URL" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}' \
  | awk 'tolower($1)=="mcp-session-id:" {print $2}' | tr -d '\r')
echo "session: $SID"   # 26-char ID expected

# 2) initialized notification — expect HTTP 202
curl -s -o /dev/null -w '%{http_code}\n' "$URL" \
  -H "Authorization: Bearer $TOKEN" -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'

# 3) list tools — expect an SSE "event: message" carrying 9 tools
curl -s "$URL" \
  -H "Authorization: Bearer $TOKEN" -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'

# 4) call a tool
curl -s "$URL" \
  -H "Authorization: Bearer $TOKEN" -H "Mcp-Session-Id: $SID" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_vaults","arguments":{}}}'
```

Auth checks: repeat any call with no/wrong token → HTTP 401 with
`WWW-Authenticate: Bearer`. `/healthz` must work without a token.

A friendlier client: `npx @modelcontextprotocol/inspector`, transport
"Streamable HTTP", URL `http://localhost:8080/`, header
`Authorization: Bearer <token>`. Or add it to Claude Code:
`claude mcp add --transport http obsidian http://localhost:8080/ --header "Authorization: Bearer <token>"`.

## 6. Tool-by-tool functional matrix

Run through each tool via MCP against a real vault. Expected behavior is
authoritative in `internal/server/server.go` and `internal/vault/vault.go`;
summarized:

| Tool | Happy path to verify | Error paths to verify |
| --- | --- | --- |
| `list_vaults` | Returns all configured vault names sorted | — |
| `list_notes` | `{vault}` lists root, non-recursive; `recursive:true` walks; hidden dirs (`.obsidian`, `.trash`) never appear | unknown vault; missing `dir`; `dir:"../x"` rejected |
| `read_note` | Returns `content`, `total_characters`, `truncated`, `next_offset`; page a >10,240-char note with `offset` until `next_offset:-1` | missing note; `offset` < 0 or beyond end; path escape |
| `search_notes` | Regex hits grouped per file with line numbers; `context_lines`, `glob`, `case_sensitive`, `max_results` all honored; `.obsidian`/`.trash` excluded | empty query; invalid regex surfaces ripgrep's message |
| `create_note` | Creates with parent dirs | duplicate path fails with pointer to append/edit |
| `append_note` | Appends; creates if missing | path escape |
| `edit_note` | Unique exact `find` replaced; `replace_all:true` for multiple; returns `replacements` | `find` absent; `find` ambiguous without `replace_all` |
| `move_note` | Renames, creates dest parents | dest exists; source missing |
| `delete_note` | Default: note lands in `.trash/` (check `trashed_to`; collisions get ` (1)` suffix); `permanent:true` removes outright; deleting inside `.trash` is always permanent | missing note; path escape |

## 7. Sync round-trip and lifecycle tests (the real acceptance test)

1. **MCP → device**: `create_note` a file, wait for sync, confirm it
   appears in Obsidian on another device (or web). Then `edit_note` and
   `delete_note` it; confirm the edit propagates and the deletion shows up
   in the vault's `.trash` on the device.
2. **Device → MCP**: edit a note on a device; poll `read_note` until the
   change is visible locally.
3. **Restart persistence**: `docker compose restart`. Watch logs — login
   and sync-setup run again on every boot. Verify: no duplicate device
   registrations piling up in Obsidian's sync settings, no re-download of
   the full vault (the named volume persists `/home/obsidian`), and the
   server comes back healthy. If `ob sync-setup` fails on an
   already-connected path, that's a real bug in `internal/bootstrap` —
   reproduce it in a test with a fake `ob` script (see
   `internal/bootstrap/bootstrap_test.go` for the pattern), then fix.
4. **Sync process resilience**: `docker compose exec obsidian-mcp pkill -f "sync --continuous"`,
   then confirm a `continuous sync exited, restarting` log line and that
   sync resumes.
5. **Graceful shutdown**: `docker compose stop` should terminate cleanly
   (SIGTERM → context cancel → HTTP shutdown), not wait for the 10s kill.

## 8. Known gotchas

- **better-sqlite3**: `obsidian-headless` depends on it; no musl prebuilds
  exist, so the Dockerfile compiles it in the `headless` stage. If npm or
  the ABI drifts (apk `nodejs` must stay on the same Node major — currently
  22/ABI 127 — as the `node:22-alpine` build stage), the runtime will fail
  to load `better_sqlite3.node`. A mismatch shows as `ERR_DLOPEN_FAILED` or
  `NODE_MODULE_VERSION` errors from `ob`.
- **TLS-intercepting proxies** break `go mod download` and `npm install`
  inside `docker build` with x509 errors. Inject your proxy CA into each
  stage locally; do not commit that change.
- **Passwords on argv**: `ob` receives `--password` as an argument. Redacted
  in our logs, but visible in `/proc` inside the container. Accepted for
  the MVP; don't log raw command lines.
- **`ob` is beta** (0.0.x). If its flags change, `internal/bootstrap` is the
  only file that builds its invocations; its README-documented interface is
  `login --email --password [--mfa]`, `sync-setup --vault --path
  [--password] [--device-name]`, `sync [--path] [--continuous]`,
  `sync-list-remote`.
- **Compose PORT**: `PORT` in `.env` selects the *host* port only; the
  container is pinned to 8080 by the compose `environment:` override.
- Read paging counts **characters (runes), not bytes** — deliberate; don't
  "optimize" it to bytes.

## 9. Definition of done for this handover

- [ ] §3 sanity pass green locally.
- [ ] Compose brings up a healthy stack with real credentials; all §4 checks pass.
- [ ] All §4 failure modes exit non-zero with a clear `fatal:` message.
- [ ] §5 handshake works; 401s without the token; `/healthz` open.
- [ ] Every §6 row verified (happy + error paths).
- [ ] All five §7 lifecycle tests pass, or each failure is captured as a
      reproducing test + fix per the CLAUDE.md methodology.
- [ ] Any code change keeps gofmt/vet clean and total coverage ≥ 95%.
- [ ] Findings written up (what passed, what failed, fixes made) and pushed
      to `claude/obsidian-mcp-server-vlcx2m` — do not push elsewhere.

## 10. Fast file map

- `cmd/obsidian-mcp/main.go` — wiring; `run()` is the testable entrypoint.
- `internal/config/config.go` — env schema; add new vars here with tests.
- `internal/bootstrap/bootstrap.go` — everything `ob`.
- `internal/vault/vault.go` — file ops + path sandbox (`resolve`).
- `internal/search/search.go` — ripgrep args + `--json` parsing.
- `internal/server/server.go` — tool schemas/handlers, auth middleware.
- `Dockerfile`, `docker-compose.yml`, `.env.example`, `.github/workflows/ci.yml`.
