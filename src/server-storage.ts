import type { Metadata } from "./metadata";

const kStorageModeKey = "edna-storage";

const noteRevisions = new Map<string, string>();
let metadataRev: string | undefined;

interface NotesList {
  all: string[];
  encrypted: string[];
}

interface ContentResponse {
  content: string;
  rev: string;
}

interface RevResponse {
  rev: string;
}

export function shouldUseServerStorage(): boolean {
  let params = new URLSearchParams(window.location.search);
  let storage = params.get("storage");
  if (storage === "server") {
    localStorage.setItem(kStorageModeKey, "server");
    return true;
  }
  if (storage === "local" || storage === "fs") {
    localStorage.removeItem(kStorageModeKey);
    return false;
  }
  const stored = localStorage.getItem(kStorageModeKey);
  if (stored === null) {
    return true;
  }
  return stored === "server";
}

export function setServerStorageEnabled(enabled: boolean) {
  if (enabled) {
    localStorage.setItem(kStorageModeKey, "server");
  } else {
    localStorage.removeItem(kStorageModeKey);
  }
}

async function fetchJSON<T>(input: RequestInfo | URL, init?: RequestInit): Promise<T> {
  let rsp = await fetch(input, init);
  if (!rsp.ok) {
    let msg = `${rsp.status} ${rsp.statusText}`;
    try {
      let data = await rsp.json();
      if (data?.error) {
        msg = data.error;
      }
    } catch (_) {
      // ignore non-json error bodies
    }
    throw new Error(msg);
  }
  return (await rsp.json()) as T;
}

function noteURL(name: string, rev?: string): string {
  let params = new URLSearchParams({ name });
  if (rev) {
    params.set("rev", rev);
  }
  return `/api/note?${params.toString()}`;
}

export async function serverListNotes(): Promise<string[][]> {
  let res = await fetchJSON<NotesList>("/api/notes");
  return [res.all || [], res.encrypted || []];
}

export async function serverLoadNote(name: string): Promise<string | undefined> {
  try {
    let res = await fetchJSON<ContentResponse>(noteURL(name));
    noteRevisions.set(name, res.rev);
    return res.content;
  } catch (e) {
    if (e instanceof Error && e.message.includes("not found")) {
      return undefined;
    }
    throw e;
  }
}

export async function serverSaveNote(name: string, content: string) {
  let rev = noteRevisions.get(name);
  let res = await fetchJSON<RevResponse>(noteURL(name, rev), {
    method: "PUT",
    body: content,
    headers: { "Content-Type": "text/plain; charset=utf-8" },
  });
  noteRevisions.set(name, res.rev);
}

export async function serverCreateNote(name: string, content: string) {
  let res = await fetchJSON<RevResponse>("/api/note/create", {
    method: "POST",
    body: JSON.stringify({ name, content }),
    headers: { "Content-Type": "application/json" },
  });
  noteRevisions.set(name, res.rev);
}

export async function serverDeleteNote(name: string) {
  await fetchJSON(noteURL(name), { method: "DELETE" });
  noteRevisions.delete(name);
}

export async function serverRenameNote(oldName: string, newName: string, content: string) {
  let res = await fetchJSON<RevResponse>("/api/note/rename", {
    method: "POST",
    body: JSON.stringify({ oldName, newName, content, rev: noteRevisions.get(oldName) }),
    headers: { "Content-Type": "application/json" },
  });
  noteRevisions.delete(oldName);
  noteRevisions.set(newName, res.rev);
}

export async function serverLoadMetadata(): Promise<Metadata> {
  let res = await fetchJSON<ContentResponse>("/api/notes/metadata");
  metadataRev = res.rev;
  return JSON.parse(res.content || "[]");
}

export async function serverSaveMetadata(metadata: Metadata): Promise<Metadata> {
  let url = "/api/notes/metadata";
  if (metadataRev) {
    url += `?rev=${encodeURIComponent(metadataRev)}`;
  }
  let content = JSON.stringify(metadata, null, 2);
  let res = await fetchJSON<RevResponse>(url, {
    method: "PUT",
    body: content,
    headers: { "Content-Type": "application/json" },
  });
  metadataRev = res.rev;
  return metadata;
}
