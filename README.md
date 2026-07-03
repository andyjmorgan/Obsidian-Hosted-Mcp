# Obsidian Hosted MCP

An internet-hostable MCP server for your Obsidian vaults. One container logs
into [Obsidian Sync](https://obsidian.md/sync) with the official
[headless client](https://obsidian.md/help/headless), bootstraps your vaults
to `~/vaults`, keeps them continuously synced, and exposes MCP tools over
streamable HTTP to read, search, create, edit, move, and delete notes. Every
change propagates back to your phone and desktop through Obsidian Sync.

Requirements: an Obsidian account with a Sync subscription. The account must
not have MFA enabled (use a dedicated sync account if yours does).

## Quick start

```sh
docker run -d \
  -e OBSIDIAN_EMAIL=you@example.com \
  -e OBSIDIAN_PASSWORD=your-account-password \
  -e OBSIDIAN_VAULTS="Work:vault-password,Personal" \
  -e OBSIDIAN_VAULT_PASSWORD=shared-fallback-password \
  -e MCP_AUTH_TOKEN=$(openssl rand -hex 32) \
  -p 8080:8080 \
  ghcr.io/andyjmorgan/obsidian-hosted-mcp:latest
```

Then point any MCP client at `http://your-host:8080/` with an
`Authorization: Bearer <MCP_AUTH_TOKEN>` header. `GET /healthz` is
unauthenticated for load balancers.

## Configuration

| Variable | Required | Description |
| --- | --- | --- |
| `OBSIDIAN_EMAIL` | yes | Obsidian account email. |
| `OBSIDIAN_PASSWORD` | yes | Obsidian account password. |
| `OBSIDIAN_VAULTS` | yes | Comma-separated remote vault names, each optionally `Name:encryption-password`. |
| `OBSIDIAN_VAULT_PASSWORD` | no | End-to-end encryption password applied to vaults that don't carry their own. |
| `MCP_AUTH_TOKEN` | yes | Static bearer token clients must present. Real MCP auth is out of scope for the MVP. |
| `OBSIDIAN_DEVICE_NAME` | no | Device name shown in sync version history. Defaults to `ObsidianMCP-` plus 8 random hex characters; set it explicitly so restarts reuse one device identity. |
| `VAULTS_DIR` | no | Where vaults are synced locally. Defaults to `~/vaults`. |
| `PORT` | no | HTTP listen port, default `8080`. |

Startup is fail-fast: if login or any vault's `sync-setup` fails, the
container exits non-zero so your orchestrator surfaces the misconfiguration.
After setup, one `ob sync --continuous` process per vault keeps the local
copy current; crashed sync processes are restarted with exponential backoff.

## MCP tools

| Tool | Description |
| --- | --- |
| `list_vaults` | Names of the vaults served. |
| `list_notes` | List files and folders in a vault, optionally recursive. Hidden folders (`.obsidian`, `.trash`) are excluded. |
| `read_note` | Read a note, paged at 10,240 characters per call with `offset`/`next_offset` for longer notes. |
| `search_notes` | ripgrep-backed regex search: glob filters, context lines, case sensitivity, result caps. |
| `create_note` | Create a new note; fails if it already exists. |
| `append_note` | Append to a note, creating it if needed. |
| `edit_note` | Exact find/replace; the snippet must be unique unless `replace_all` is set. |
| `move_note` | Move or rename a note. |
| `delete_note` | Move a note to the vault's `.trash` (Obsidian's own convention, recoverable everywhere); `permanent: true` removes it outright. |

All paths are vault-relative and sandboxed: absolute paths and `..` escapes
are rejected.

## Development

```sh
go test ./...                       # requires ripgrep on PATH for integration tests
go test ./... -coverprofile=coverage.out && go tool cover -func=coverage.out
docker build -t obsidian-hosted-mcp .
```

CI enforces `gofmt`, `go vet`, and a 95% total coverage gate, then publishes
a multi-arch (amd64/arm64) image to GHCR: `:latest` from `main` and semver
tags from `v*` releases.

## Security notes

- Run behind TLS (a reverse proxy or your platform's ingress); the token
  travels in a header.
- The bearer token is a shared secret, not user auth. OAuth-based MCP auth
  is planned but out of scope for the MVP.
- Credentials are passed to the `ob` CLI as arguments inside the container
  and are redacted from this server's logs.
