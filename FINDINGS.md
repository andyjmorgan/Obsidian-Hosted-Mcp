# FINDINGS — Compose + MCP validation run (2026-07-03)

Executed against a real Obsidian Sync account (vault "Me", ~600 notes),
per HANDOVER.md. Result: **everything passed; no code changes required.**

## What passed

- **§3 sanity**: gofmt/vet clean, all tests pass, coverage 95.4% (≥95%
  gate; handover quoted 96.7% — see observations).
- **§4 bring-up**: boot sequence exactly as documented; `/healthz` open;
  compose healthcheck healthy; non-root `obsidian`; full vault synced to
  the named volume.
- **§4 failure modes**: wrong password, nonexistent vault, and missing
  `MCP_AUTH_TOKEN` each exit 1 with a clear `fatal:` line (password
  redacted).
- **§5 handshake**: 26-char session id, 202 on `notifications/initialized`,
  9 tools listed, tool calls work; 401 + `WWW-Authenticate: Bearer` with
  missing/wrong token.
- **§6 tool matrix**: all 9 tools verified, happy and error paths —
  including recursive listing with hidden dirs excluded (598 entries, no
  `.obsidian`/`.trash`), 10,240-char read paging to `next_offset:-1`,
  ripgrep flag pass-through and error surfacing, duplicate-create rejection,
  ambiguous-edit rejection + `replace_all`, move with dest-parent creation,
  soft delete with `.trash` collision suffix ` (1)`, permanent delete, and
  path-escape rejection on every path-taking tool.
- **§7 lifecycle**:
  - Restart: login + sync-setup re-ran idempotently on the connected path,
    no full re-download, stable device name, healthy after restart.
  - MCP → cloud: every create/edit/move/delete was uploaded by continuous
    sync within seconds (verified in `ob` output).
  - Device → MCP: live `Push:` updates from the account's other devices
    were downloaded and readable during the run.
  - Resilience: `pkill -f "sync --continuous"` → supervisor logged
    `continuous sync exited, restarting` and sync resumed.
  - Graceful shutdown: `docker compose stop` completed in 0.24s, exit 0.

Not verified (needs a human at another device): visually confirming an
MCP-created note appears in the Obsidian UI on another device. Server-side
upload + independent device pushes strongly suggest this works.

## Observations (no action taken)

1. **Stale sync lock after restart**: immediately after
   `docker compose restart`, the first two `ob sync --continuous` attempts
   exited with "Another sync instance is already running for this vault";
   the supervisor's backoff recovered on the third attempt (~3s total).
   This is `ob`'s lock going stale across container restarts, self-healed
   by design of `superviseVault` — acceptable, but if `ob` ever grows a
   `--force`/lock-timeout flag it would remove the noise.
2. **Coverage 95.4% vs 96.7% in the handover** — still above the gate;
   the delta is likely environment-dependent test paths. Worth a look if
   the CI gate ever gets tight.
3. **Error messages leak container-absolute paths**
   (e.g. `open /home/obsidian/vaults/Me/x: no such file or directory`).
   Harmless internally, but could be trimmed to vault-relative for polish.
4. **Zero-match search returns `"files": null`** rather than `[]` —
   cosmetic Go JSON artifact; clients should treat null as empty.

## Vault hygiene

All test artifacts were created under `mcp-test/`, then permanently
deleted (including the `.trash` entries) and the empty folders removed;
`ob` confirmed the remote deletions. The vault is back to its pre-test
state.
