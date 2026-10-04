---
name: obsidian-tasks
description: Read, create, update and complete the user's reminders and tasks, which live in their Obsidian vault and are shared by every assistant. Use whenever the user mentions reminders, todos, tasks, "remind me", "what's outstanding", "what's due", or asks to mark something done — and whenever a conversation produces a follow-up worth remembering.
---

# Obsidian tasks and reminders

The user's single task list lives in their Obsidian vault, in one note
(`Reminders.md` unless the server is configured otherwise). Every assistant
they use — Claude, ChatGPT, Claude Code, local agents — reads and writes the
same note through the Obsidian MCP server's task tools, so a reminder made in
one place is visible everywhere.

Treat these tasks as first-class state, not as text in a note.

## Rules

1. **Use the task tools, never the note tools, for reminders.** Call
   `list_tasks`, `get_task`, `create_task`, `update_task`, `complete_task`,
   `reopen_task` and `delete_task`. Do not `read_note`, `edit_note` or
   `append_note` the reminders note, and do not edit it on disk when you have
   filesystem access. The tools assign IDs, keep subtasks consistent and
   enforce the record format; text edits bypass all of that.
2. **A reminder must be useful without this conversation.** `context` is
   mandatory: say what was decided or observed, why the follow-up matters,
   and where the details live. Never create "do the thing". Link related notes
   with `related` and name the origin in `source` (a meeting, a chat, a note,
   with a date).
3. **Close with a handoff, not a tick.** `complete_task` needs a
   `resolution`: how it was resolved, what changed or was delivered, why that
   resolution was chosen, and anything left over, with links to resulting
   notes, PRs or records. If subtasks are still open the tool refuses — close
   or delete them first, deliberately.
4. **Capture follow-ups as they appear.** When a conversation produces
   something to do later, offer to add it (or add it when the user has asked
   you to manage their tasks). Prefer one reminder with subtasks over several
   loosely related reminders.
5. **Due dates only when real.** Set `due`/`due_time` for a genuine deadline
   or intended follow-up point, not as decoration.

## Vault and file

Call `list_vaults` once to learn the vault name; it is required by every
task tool. Tasks are filed under headings (`section`); new top-level tasks go
to `Inbox` unless the user has a convention. Subtasks are made with
`parent_id`.

## Typical calls

What is outstanding:

```json
{ "tool": "list_tasks", "arguments": { "vault": "Me" } }
```

Add a reminder from a conversation:

```json
{ "tool": "create_task", "arguments": {
  "vault": "Me",
  "title": "Renew the donkeywork.dev domain",
  "context": "The registrar sent a 30-day notice on 2026-10-01. Auto-renew is off on purpose because the card on file expired; renewing manually needs the new card from [[Billing]].",
  "related": ["Domains", "Billing"],
  "source": "Planning call, 2026-10-04",
  "due": "2026-10-20", "due_time": "09:00"
} }
```

Close it:

```json
{ "tool": "complete_task", "arguments": {
  "vault": "Me", "id": "rem-3f9a",
  "resolution": "Renewed for two years with the new card; receipt filed in [[Billing]]. Auto-renew left off deliberately — revisit in 2028."
} }
```

## When the tools are unavailable

If the Obsidian MCP server is not connected, say so and ask the user to
reconnect it rather than editing the note by other means. Falling back to
text edits is how context gets lost and IDs drift.
