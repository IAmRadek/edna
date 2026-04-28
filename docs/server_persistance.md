# Server Persistence

## Scope

This document covers a single-user server persistence mode for Edna, intended to run behind Tailscale with no application-level authentication.

The goal is not a full sync product. The goal is to let the existing Go server persist notes on the server filesystem so the same note workspace can be used from multiple devices over a private network.

## Current Architecture

Edna is already a Svelte/Vite frontend served by a Go HTTP server.

- Frontend boot starts in `src/main.ts:27`.
- The Svelte app mounts in `src/main.ts:118`.
- The Go HTTP server is built in `server/server.go:121`.
- Route dispatch is centralized in `server/server.go:128`.
- The server already has a persistent data directory via `getDataDirMust()` in `server/main.go:55`.
- Logs use a subdirectory under that data dir in `server/main.go:70`.

The existing backend handles utility APIs, static assets, log events, currency rates, and Go playground operations. It does not currently expose note CRUD APIs.

## Current Client Persistence

Note persistence is client-owned today.

`src/notes.ts:57` switches between two modes:

- Browser `localStorage`
- Browser File System Access API via `FileSystemDirectoryHandle`

The important note operations are already mostly centralized:

- `loadNoteNames()` in `src/notes.ts:406`
- `saveNote()` in `src/notes.ts:478`
- `createNoteWithName()` in `src/notes.ts:509`
- `appendToNote()` in `src/notes.ts:532`
- `loadNote()` in `src/notes.ts:596`
- `deleteNote()` in `src/notes.ts:735`
- `renameNote()` in `src/notes.ts:750`

This makes server persistence a moderate refactor, not a rewrite.

## Data Model

Notes are plain text documents using Edna block markers.

- Plain notes use `.edna.txt` from `src/constants.ts:21`.
- Encrypted notes use `.encr.edna.txt` from `src/constants.ts:22`.
- `fixUpNoteContent()` ensures note content starts with a block header in `src/notes.ts:430`.
- File names are encoded/decoded through `filenamify` helpers used by `notePathFromNameFS()` in `src/notes.ts:140`.

Metadata is stored as JSON:

- Metadata filename is `__metadata.edna.json` in `src/metadata.ts:6`.
- Note metadata schema is in `src/metadata.ts:8`.
- Metadata includes star/archive state, shortcuts, folded ranges, and selection.
- Metadata loads in `src/metadata.ts:44`.
- Metadata saves in `src/metadata.ts:65`.

Settings and stats are separate from notes:

- Settings load from `localStorage` in `src/settings.svelte.ts:225`.
- Settings save to `localStorage` in `src/settings.svelte.ts:297`.
- Stats load from `localStorage` in `src/state.ts:22`.

For an initial server persistence feature, settings and stats can remain local unless the desired behavior is a fully shared workspace experience.

## Proposed Solution

Add a third persistence backend:

```text
localStorage | browser filesystem | server filesystem
```

Keep the existing public frontend functions and route their internals through a storage adapter. This avoids touching most UI/editor call sites.

Recommended TypeScript shape:

```ts
interface NotesStorage {
  kind: "local" | "fs" | "server";
  listNotes(): Promise<{ all: string[]; encrypted: string[] }>;
  loadNote(name: string): Promise<string | undefined>;
  saveNote(name: string, content: string, rev?: string): Promise<{ rev?: string }>;
  createNote(name: string, content: string): Promise<void>;
  deleteNote(name: string): Promise<void>;
  renameNote(oldName: string, newName: string, content: string): Promise<void>;
  loadMetadata(): Promise<Metadata>;
  saveMetadata(metadata: Metadata, rev?: string): Promise<{ rev?: string }>;
}
```

Then update:

- `src/notes.ts` to delegate note operations to the active storage backend.
- `src/metadata.ts` to delegate metadata load/save to the active storage backend.
- `src/main.ts` boot flow to initialize server storage when enabled.

## Server API

Use JSON for metadata/list operations and plain text for note bodies.

Suggested endpoints:

```text
GET    /api/notes
GET    /api/notes/{encodedName}
PUT    /api/notes/{encodedName}
POST   /api/notes
DELETE /api/notes/{encodedName}
POST   /api/notes/{encodedName}/rename

GET    /api/notes/metadata
PUT    /api/notes/metadata
```

Alternative: avoid path encoding issues by using query parameters:

```text
GET    /api/note?name=...
PUT    /api/note?name=...
DELETE /api/note?name=...
```

For this codebase, query parameters are safer because note names may include spaces and punctuation. The server should accept note names, not raw filesystem paths.

## Server Storage Layout

Store notes under the existing data directory:

```text
data/
  notes/
    scratch.edna.txt
    inbox.edna.txt
    __metadata.edna.json
```

On Linux production this becomes:

```text
/home/data/edna/notes/
```

because `getDataDirMust()` switches to `/home/data/edna` in production Linux mode at `server/main.go:61`.

## Safety Requirements

Even with Tailscale and no auth, the server-side filesystem boundary must be strict.

Required:

- Do not accept raw filenames or paths from the client.
- Accept note names and convert/validate them server-side.
- Reject path traversal.
- Only read/write files under `getDataDirMust()/notes`.
- Use atomic writes: write temp file, fsync if practical, rename into place.
- Preserve `.edna.txt`, `.encr.edna.txt`, and `__metadata.edna.json` conventions.
- Add request body size limits.

Recommended:

- Add a `-server-notes` or `-storage server` flag.
- Add an explicit `-listen` flag before binding outside localhost.
- Keep localhost binding as the default for desktop/local use.

Current binding logic is in `server/server.go:226`: Linux binds to `:9325`, while Windows/Mac bind to `localhost:9325`. For Tailscale, an explicit listen address is clearer than relying on OS behavior.

## Conflict Detection

Do not rely on silent last-write-wins unless data loss is acceptable.

Minimal conflict detection:

- Server returns a revision with each note read. This can be a hash, mtime+size, or monotonic version.
- Client sends the revision back on save.
- Server returns `409 Conflict` if the file changed since the client loaded it.

This protects against:

- Two browser tabs.
- Two devices over Tailscale.
- Mobile/laptop stale sessions.

The editor save path is already centralized:

- `src/components/MultiBlockEditor.svelte:148` passes note content to `saveNote()`.
- `src/editor/editor.ts:189` has save dedupe logic around current editor content.

That is the right place to attach and update per-note revisions.

## Encryption

Current encryption is tied to browser filesystem mode:

- `isUsingEncryption()` checks filesystem/encrypted note state in `src/notes.ts:893`.
- Encrypted reads/writes happen in `readEncryptedFS()` and `writeEncryptedFS()` around `src/notes.ts:630` and `src/notes.ts:668`.

For server persistence, the simplest compatible approach is client-side encryption:

- Keep encrypted note content opaque to the server.
- Server stores `.encr.edna.txt` bytes exactly like filesystem mode.
- Password handling remains client-side.

Do not add server-side encryption for the initial version. It complicates the trust model and is unnecessary for the Tailscale/no-auth target.

## Migration

Useful migration paths:

- Browser/localStorage to server notes.
- Browser filesystem directory to server notes.
- Server notes back to local export, probably via the existing zip export path.

The existing filesystem migration code in `switchToStoringNotesOnDisk()` at `src/notes.ts:822` is the closest model. A server migration should follow the same behavior:

- List destination notes.
- Copy notes.
- If content differs, create a unique name instead of overwriting.
- Move `__metadata.edna.json`.
- Reload note names.

## Implementation Plan

1. Add Go note storage helpers.
   - Create `server/notes.go`.
   - Add `getNotesDirMust()`.
   - Add note name to filename conversion/validation.
   - Add list/read/write/delete/rename helpers.

2. Add Go HTTP handlers.
   - Register under `server/server.go:128`.
   - Support list/load/save/create/delete/rename/metadata.
   - Return JSON errors consistently.
   - Add body size limits.

3. Add frontend server storage adapter.
   - Add `src/server-storage.ts` or similar.
   - Implement fetch wrappers.
   - Track note revisions.
   - Surface `409 Conflict` cleanly.

4. Refactor client persistence selection.
   - Keep exported functions in `src/notes.ts`.
   - Move localStorage and filesystem implementations behind a common interface.
   - Add server backend selection.

5. Move metadata behind the same storage boundary.
   - Refactor `src/metadata.ts:44` and `src/metadata.ts:65`.

6. Add configuration UI/flag.
   - Minimal first version can be a query/localStorage setting.
   - Better version adds a command/settings option for server-backed notes.

7. Add focused tests.
   - Go tests for filename validation, path traversal rejection, CRUD, rename conflicts.
   - Frontend unit tests for adapter behavior and conflict handling.

Per repository instruction, do not run `go test` automatically; signal when it should be run.

## Effort Estimate

For single-user, no auth, Tailscale:

- Minimal working server notes: 1-2 days.
- Polished storage mode with migration and UI states: 3-5 days.
- Optimistic conflict detection: add 1-2 days.

Full multi-user auth/sync is out of scope and would be a product-sized feature.

## Recommendation

Build this as server filesystem persistence first, not a database-backed sync system.

The current note model is already file-oriented, the Go server already has a data directory, and the frontend note operations are centralized enough to add a third backend cleanly. The main engineering risk is silent overwrite, so include revision-based conflict detection in the first serious version.
