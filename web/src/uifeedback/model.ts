// Application-independent v1 wire types. Keep this namespace aligned with
// internal/uifeedback/model.go; Pharos maps its annotations at the boundary.
export interface Screenshot { mime: "image/png" | "image/jpeg"; data: string }
export interface Annotation {
  id: string; comment: string; label?: string; selector?: string; text?: string; url?: string; captured_at: string;
  bounds: { x: number; y: number; width: number; height: number };
  viewport: { width: number; height: number; device_pixel_ratio: number };
  screenshot?: Screenshot;
}
export interface Diagnostics { browser?: string; requests?: string; service?: string }
export interface Submission {
  version: 1; id: string; application: { name: string; version?: string }; kind: "bug" | "feature" | "prompt" | "feedback";
  comment: string; email?: string; annotations: Annotation[]; diagnostics?: Diagnostics;
}
export interface Entry extends Submission { received_at: string; tags: string[]; revision: string }
// General reports may omit annotations or store JSON null, including older
// reports. Normalize this optional wire field at the inbox boundary.
export function normalizeEntry(entry: Omit<Entry, "annotations"> & { annotations?: Annotation[] | null }): Entry {
  return { ...entry, annotations: entry.annotations ?? [] };
}
export interface Summary {
  id: string; application: Submission["application"]; kind: Submission["kind"]; comment: string; email?: string;
  received_at: string; annotation_count: number; screenshot_count: number; has_diagnostics: boolean; tags: string[]; revision: string;
}
const sensitive = /^(?:token|access_token|refresh_token|authorization|api[_-]?key|key|secret|password|code|state)$/i;
export function safeURL(raw: string): string {
  try { const u = new URL(raw); if (!["http:", "https:"].includes(u.protocol)) return ""; u.username = ""; u.password = "";
    for (const key of [...u.searchParams.keys()]) if (sensitive.test(key)) u.searchParams.delete(key);
    u.hash = ""; return u.href;
  } catch { return ""; }
}
export function boundedLog(raw: string): string {
  const text = raw.replace(/Bearer\s+[^\s"']+/gi, "Bearer [redacted]")
    .replace(/((?:access_token|refresh_token|token|authorization|api[_-]?key|secret|password)["']?\s*[:=]\s*["']?)[^\s&"']+/gi, "$1[redacted]")
    .replace(/\/Users\/[^/\s]+/g, "/Users/[redacted]");
  const bytes = new TextEncoder().encode(text); if (bytes.length <= 65536) return text;
  // Leave room for replacement of a partial UTF-8 rune at the tail boundary.
  return new TextDecoder().decode(bytes.slice(-65532));
}
export function changeTags(existing: string[], value: string, remove = false): string[] {
  const tag = value.trim(); return remove ? existing.filter(t => t !== tag) : [...new Set([...existing, tag])];
}
// Retry identity is tied to the exact reviewed payload, not just an open dialog.
// An edited report must acquire a fresh ID after an uncertain send.
export function pendingSubmission(value: Omit<Submission, "id">, previous: Submission | null, uuid: () => string): Submission {
  if (previous) { const { id, ...body } = previous; if (JSON.stringify(body) === JSON.stringify(value)) return previous; }
  return { ...value, id: uuid() };
}
