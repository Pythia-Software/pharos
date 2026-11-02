import { cellTitle, formatted, localTime } from "./delegation";

type Metric = { value: number | null; quality: string; known: number; total: number };
type Spec = { id: string; label: string; unit: string; definition: string };
type Model = { model: string; provider: string; sessions: number; tokens: number };
type Scope = { start: number | null; end: number | null; sessions: number; models: Model[]; totals: Record<string, Metric> };
export type WorkPreview = { repository?: string; branch?: string; metrics: Spec[]; workspace: Scope; conversation?: Scope };
export type WorkPreviewAddress = { workspace: string; conversation?: string };

// Ask previews a work page or one of its conversations, in either spelling
// the site accepts. Any other view of a work page keeps a plain link.
export function askWorkPreviewAddress(address: string): WorkPreviewAddress | undefined {
  let url: URL;
  try { url = new URL(address, "http://pharos.invalid"); } catch { return; }
  if (url.origin !== "http://pharos.invalid") return;
  const parts = url.pathname.split("/").slice(1).map(part => { try { return decodeURIComponent(part); } catch { return ""; } });
  const keys = [...url.searchParams.keys()].filter(key => !["conversation", "highlight", "reveal"].includes(key));
  if (parts[0] !== "work" || !parts[1] || keys.length) return;
  const query = url.searchParams.get("conversation") || undefined;
  if (parts.length === 2) return { workspace: parts[1], ...query ? { conversation: query } : {} };
  if (parts.length === 4 && parts[2] === "conversation" && parts[3] && (!query || query === parts[3])) return { workspace: parts[1], conversation: parts[3] };
  return;
}

export const workPreviewEndpoint = ({ workspace, conversation }: WorkPreviewAddress) =>
  `/api/work/${encodeURIComponent(workspace)}/preview${conversation ? `?conversation=${encodeURIComponent(conversation)}` : ""}`;

// The linking agent supplies the title and why it matters; the preview helps
// the reader recognize the run in two short lines: where and when it ran,
// then who ran it and how much happened. Unknown measures are left out;
// partial ones carry the explorer's "+".
export type WorkPreviewPart = { text: string; title?: string };
export type WorkPreviewView = { where: WorkPreviewPart[]; what: WorkPreviewPart[] };

const day = new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
const dayWithYear = new Intl.DateTimeFormat(undefined, { year: "numeric", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
// Dated snapshot suffixes add length without helping recognition.
const modelName = (model: string) => model.replace(/-20\d{6}$/, "");

export function workPreviewView(preview: WorkPreview, now = Date.now()): WorkPreviewView {
  const scope = preview.conversation ?? preview.workspace;
  const specs = new Map(preview.metrics.map(spec => [spec.id, spec]));
  // A noun names a count ("3 files"); without one the value stands alone.
  const measure = (id: string, noun?: string): WorkPreviewPart[] => {
    const metric = scope.totals[id], spec = specs.get(id);
    if (metric?.value == null || !spec) return [];
    const shown = `${formatted(metric.value, spec.unit)}${metric.quality === "partial" ? "+" : ""}`;
    return [{ text: noun ? `${shown} ${metric.value === 1 && metric.quality !== "partial" ? noun : noun + "s"}` : shown, title: cellTitle(metric, spec) }];
  };
  // Each line truncates at its end, so the least distinctive part goes last.
  const where: WorkPreviewPart[] = [];
  if (scope.start != null) {
    const started = new Date(scope.start);
    where.push({ text: (started.getFullYear() === new Date(now).getFullYear() ? day : dayWithYear).format(started), title: localTime(scope.start) });
  }
  where.push(...measure("clock"));
  const place = [preview.repository, preview.branch].filter(Boolean).join(" · ");
  if (place) where.push({ text: place, title: place });
  const models = scope.models.slice(0, 2).map(model => modelName(model.model));
  const what: WorkPreviewPart[] = [];
  // Subagents are sessions another session in scope launched; independent
  // top-level conversations in the same work are not subagents.
  what.push(...measure("children", "subagent"));
  if (models.length) what.push({ text: models.join(", ") + (scope.models.length > 2 ? ` +${scope.models.length - 2}` : ""), title: scope.models.map(model => `${model.model} (${model.provider}): ${model.sessions} ${model.sessions === 1 ? "session" : "sessions"}`).join("\n") });
  what.push(...measure("turns", "turn"), ...measure("files", "file"), ...measure("cost"));
  return { where, what };
}
