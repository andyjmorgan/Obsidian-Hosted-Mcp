<p align="center">
  <img src="https://obsidian.md/images/obsidian-logo-gradient.svg" alt="Obsidian logo" width="120">
</p>

<h1 align="center">Obsidian Hosted MCP</h1>

<p align="center">
  Give Claude, ChatGPT, and any other MCP client full access to your
  <a href="https://obsidian.md">Obsidian</a> vaults — from anywhere.
</p>

<p align="center">
  <a href="https://github.com/andyjmorgan/Obsidian-Hosted-Mcp/actions/workflows/ci.yml"><img src="https://github.com/andyjmorgan/Obsidian-Hosted-Mcp/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/andyjmorgan/Obsidian-Hosted-Mcp/pkgs/container/obsidian-hosted-mcp"><img src="https://img.shields.io/badge/ghcr.io-obsidian--hosted--mcp-blue?logo=docker" alt="Container image"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="MIT license"></a>
</p>

## Why this exists

Your Obsidian vault is where your notes, projects, and ideas live — but AI
assistants can't see it. Plugins that embed an HTTP server inside the
desktop app only work while that machine is awake, on your network, with
Obsidian running.

This project takes a different approach: a **single, internet-hostable
container** that is itself a first-class Obsidian Sync device. It logs into
[Obsidian Sync](https://obsidian.md/sync) with the official
[headless client](https://help.obsidian.md/install/headless) (no
reverse-engineered protocols), keeps a live copy of your vaults, and serves
them to AI assistants over the
[Model Context Protocol](https://modelcontextprotocol.io) (streamable HTTP).

That means:

- **Claude** (claude.ai, Claude Code, Claude Desktop) and **ChatGPT**
  (connectors / deep research) can read, search, create, edit, and organize
  your notes.
- Every write syncs back to your phone, tablet, and desktop within seconds —
  and every note you jot down on your phone becomes visible to your
  assistant.
- It runs 24/7 wherever you host containers: a NAS, a VPS, Kubernetes, or a
  Raspberry Pi (images are amd64 + arm64).

Requirements: an Obsidian account with a **Sync subscription**. The account
must not have MFA enabled (use a dedicated sync account if yours does).

## Quick start

```sh
docker run -d \
  -e OBSIDIAN_EMAIL=you@example.com \
  -e OBSIDIAN_PASSWORD=your-account-password \
  -e OBSIDIAN_VAULTS="Work:vault-password,Personal" \
  -e MCP_AUTH_TOKEN=$(openssl rand -hex 32) \
  -p 8080:8080 \
  ghcr.io/andyjmorgan/obsidian-hosted-mcp:latest
```

Or with Docker Compose (persists the synced vaults and the sync client's
credential store across restarts):

```sh
cp .env.example .env   # fill in credentials, vaults, and a generated token
docker compose up -d
```

Watch the logs: the container logs in, connects each vault, starts
continuous sync, and then serves MCP on port 8080. `GET /healthz` is
unauthenticated for load balancers and health checks.

## Connect your AI assistant

The endpoint is the server root (`/`) over streamable HTTP.

**Claude Code**

```sh
claude mcp add --transport http obsidian https://your-host/ \
  --header "Authorization: Bearer <MCP_AUTH_TOKEN>"
```

**claude.ai / Claude Desktop** — add a custom connector with URL
`https://your-host/`. With the static token, paste it as a bearer header
where supported; with OAuth configured (below), the connector discovers the
flow automatically and sends your users through your identity provider.

**ChatGPT** — add an MCP connector (Settings → Connectors) pointing at
`https://your-host/`. OAuth-configured servers let ChatGPT run the
authorization flow itself.

**Anything else** — `npx @modelcontextprotocol/inspector`, transport
"Streamable HTTP", URL `https://your-host/`, header
`Authorization: Bearer <token>`.

## Deploying it for yourself

1. **Pick a host** that can run a container and be reached over HTTPS. TLS
   is non-negotiable — tokens travel in a header. A reverse proxy
   (Caddy/Traefik/nginx), a platform ingress, or a
   [Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
   all work; the container itself speaks plain HTTP on 8080.
2. **Use a dedicated vault (or account) first.** The server can create,
   edit, move, and delete notes. Deletes are soft by default (they land in
   the vault's `.trash` and stay recoverable everywhere), but start with a
   test vault until you trust your setup.
3. **Persist `/home/obsidian`** (a named volume or PVC). It holds the sync
   client's credential store and the local vault copies; losing it is
   recoverable but costs a full re-sync on next boot.
4. **Run one replica.** Two sync processes on the same vault copy fight
   over the sync client's lock. On Kubernetes use `strategy: Recreate`.
5. **Pick auth**: a static API key (`MCP_AUTH_TOKEN`), OAuth via your
   identity provider (below), or both side by side.

Startup is fail-fast: if login, any vault's `sync-setup`, or OAuth
discovery fails, the container exits non-zero so your orchestrator surfaces
the misconfiguration instead of serving a half-configured vault set.

## Configuration

| Variable | Required | Description |
| --- | --- | --- |
| `OBSIDIAN_EMAIL` | yes | Obsidian account email. |
| `OBSIDIAN_PASSWORD` | yes | Obsidian account password. |
| `OBSIDIAN_VAULTS` | yes | Comma-separated remote vault names, each optionally `Name:encryption-password`. |
| `OBSIDIAN_VAULT_PASSWORD` | no | End-to-end encryption password applied to vaults that don't carry their own. |
| `MCP_AUTH_TOKEN` | yes* | Static bearer token (API key) clients may present. Optional when `OAUTH_ISSUER` is set; at least one of the two is required. |
| `OAUTH_ISSUER` | no | OpenID Connect issuer URL. Setting it delegates auth to that provider — see below. |
| `OAUTH_AUDIENCE` | with `OAUTH_ISSUER` | Audience tokens must carry in `aud` (or `azp`, the authorized-party fallback used by e.g. Keycloak client tokens). |
| `MCP_PUBLIC_URL` | with `OAUTH_ISSUER` | This server's canonical public URL, used as the protected-resource identifier. |
| `OAUTH_INTERNAL_ISSUER` | no | Alternative base URL for fetching discovery/JWKS (e.g. cluster-internal). The discovery document must still report `OAUTH_ISSUER` as its issuer. |
| `OAUTH_SCOPES` | no | Comma-separated scopes advertised as `scopes_supported`. Default `openid,profile,email`. |
| `OBSIDIAN_DEVICE_NAME` | no | Device name shown in sync version history. Defaults to `ObsidianMCP-` plus 8 random hex characters; set it explicitly so restarts reuse one device identity. |
| `VAULTS_DIR` | no | Where vaults are synced locally. Defaults to `~/vaults`. |
| `PORT` | no | HTTP listen port, default `8080`. |

## OAuth: delegate auth to your identity provider

Setting `OAUTH_ISSUER` turns the server into an OAuth 2.0 protected
resource. It is provider-agnostic — anything OIDC-compliant works
(Keycloak, Auth0, Entra ID, Okta, ...):

- Bearer JWTs from the issuer are validated (signature via JWKS, issuer,
  lifetime, and audience — with the `azp` fallback Keycloak uses for
  client tokens).
- RFC 9728 protected-resource metadata is served at
  `/.well-known/oauth-protected-resource`, and 401 responses carry a
  `resource_metadata` challenge — so MCP clients like claude.ai and ChatGPT
  discover your authorization server and run the flow (including dynamic
  client registration, if your IdP allows it) without any manual token
  handling.
- The static API key keeps working alongside, which is handy for scripts
  and smoke tests.

Minimal example:

```sh
OAUTH_ISSUER=https://auth.example.com/realms/myrealm
OAUTH_AUDIENCE=obsidian-mcp
MCP_PUBLIC_URL=https://obsidian.example.com
```

Your IdP must issue tokens whose `aud` (or `azp`) contains
`OAUTH_AUDIENCE` — in Keycloak that's a client scope with an audience
mapper, made a realm default so dynamically-registered MCP clients pick it
up automatically.

## MCP tools

| Tool | Description |
| --- | --- |
| `list_vaults` | Names of the vaults served. |
| `list_notes` | List files and folders in a vault, optionally recursive. Hidden folders (`.obsidian`, `.trash`) are excluded; pass `dir: ".trash"` to browse deleted notes. |
| `read_note` | Read a note, paged at 10,240 characters per call with `offset`/`next_offset` for longer notes. |
| `search_notes` | ripgrep-backed regex search: glob filters, context lines, case sensitivity, result caps. |
| `create_note` | Create a new note; fails if it already exists. |
| `append_note` | Append to a note, creating it if needed. |
| `edit_note` | Exact find/replace; the snippet must be unique unless `replace_all` is set. |
| `move_note` | Move or rename a note. |
| `delete_note` | Move a note to the vault's `.trash` (Obsidian's own convention, recoverable everywhere); `permanent: true` removes it outright. |
| `restore_note` | Undelete: move a note out of `.trash`, back to its original name or an explicit destination. |

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
- The static bearer token is a shared secret, not user auth. For user
  identity, set `OAUTH_ISSUER` to delegate authentication to any OpenID
  Connect provider; both modes can run side by side.
- Credentials are passed to the `ob` CLI as arguments inside the container
  and are redacted from this server's logs.

---

*Obsidian is a trademark of Dynalist Inc. This project is not affiliated
with or endorsed by Obsidian; it simply drives the official headless sync
client.*
