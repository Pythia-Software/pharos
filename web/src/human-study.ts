import { EMPTY_QUERY, applyQuery, type FieldSchema, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import { queryURL } from "./query-links";

export type HumanStudy = "volume" | "message-average" | "conversation-average" | "workspace-average" | "conversation-lengths" | "contribution-patterns" | "specific-feedback" | "examples" | "hours";
export type StudyMessage = { id: string; sent_at: string; typed_words: number; pasted_words: number; attachment_words: number; conversation_id: string; work_id?: string; workspace_id?: string; repository_name?: string; text?: string; text_truncated?: boolean; segments?: { text: string; category?: string }[]; [key: string]: unknown };
export type StudyWorkspace = { id: string; location?: string; repository_name?: string };
export type HumanStudyPreview = {
  kind: "trend" | "composition" | "hours" | "profile"; value?: number; valueLabel: string; sampleLabel: string; format?: "percent" | "USD"; pointValueLabel?: string; trendLabel?: string; timeZone?: string;
  baseline?: number; points?: (number | null)[]; pointTimes?: number[]; lookbackDays?: number;
  rows?: { label: string; value: number; count?: number }[];
  evidence?: { id: string; text: string; url: string; label: string }[]; detail?: string;
  workspaceRows?: { label: string; location: string; count: number; total: number; value: number; url: string }[];
};
const day = 86400000;
type EvidenceBatch = { rows: StudyMessage[]; now: number };
const messageCache = new Map<string, { at: number; batch: EvidenceBatch }>();
const directoryCache = new Map<string, { at: number; rows: StudyWorkspace[] }>();
export function clearHumanStudyCache() { messageCache.clear(); directoryCache.clear(); }
const positive = (message: StudyMessage) => message.typed_words > 0;
const human = (message: StudyMessage) => message.typed_words > 0 || message.pasted_words > 0 || message.attachment_words > 0;
export function ownText(message: StudyMessage): string {
  // Never use injected instructions or copied agent output as human evidence.
  return (message.segments ?? []).filter(span => span.category === "typed").map(span => span.text).join(" ").trim();
}
export function contributionSignals(message: StudyMessage) {
  const typed = ownText(message), normalized = typed.toLowerCase().replace(/[.!?,;:]+/g, " ").replace(/\s+/g, " ").trim();
  const continuation = !message.text_truncated && message.pasted_words === 0 && message.attachment_words === 0 && /^(?:(?:yes|yep|ok|okay|great|thanks|thank you|sounds good|looks good)(?: please)?(?: continue| go ahead| proceed| keep going)?|please continue|continue|go ahead|proceed|keep going|do it)$/.test(normalized);
  const cue = /\b(?:should|must|instead|expected|expect|actually|incorrect|wrong|rename|remove|replace|change|fix|prefer|want|need|missing|doesn.t|does not|rather than)\b/i.test(typed);
  const detail = /\b(?:because|when|if|rather than|instead of|for example|for instance)\b|\d|`[^`]+`|"[^"]+"|“[^”]+”|(?:[\w.-]+\/)+[\w.-]+|\bhttps?:\/\//i.test(typed) || message.pasted_words > 0 || message.attachment_words > 0;
  const visible = (message.segments ?? []).filter(span => ["typed", "pasted", "attachment"].includes(span.category ?? "")).map(span => span.text).join(" ");
  const image = /\(image attachment\)|\.(?:png|jpe?g|gif|webp|heic)\b/i.test(visible);
  const references = visible.match(/@⟦[^⟧]+⟧\([^)]+\)|\[[^\]]+\]\([^)]+\)|\bhttps?:\/\/[^\s)]+|(?:[\w.-]+\/)+[\w.-]+\.(?:md|txt|csv|json|pdf|ts|tsx|js|py|go|xlsx)\b/gi) ?? [];
  const file = references.some(reference => !/\.(?:png|jpe?g|gif|webp|heic)\b/i.test(reference));
  return { continuation, feedback: !message.text_truncated && !continuation && cue && detail, image, file, paste: message.pasted_words > 0, typed };
}
export function workspaceIdentity(workspace?: StudyWorkspace): string | undefined {
  const location = workspace?.location?.trim().replace(/\/+$/, "");
  return location?.startsWith("/") ? JSON.stringify([workspace?.repository_name ?? "", location]) : undefined;
}
const unique = (values: (string | undefined)[]) => new Set(values.filter((value): value is string => Boolean(value))).size;
const sum = (rows: StudyMessage[]) => rows.reduce((n, row) => n + row.typed_words, 0);
function directoryMessageFilters(rows: StudyMessage[]): WhereTerm[] {
  const workIds = new Set<string>(), workspaceIds = new Set<string>();
  for (const row of rows) {
    if (row.work_id != null) workIds.add(row.work_id);
    else if (row.workspace_id != null) workspaceIds.add(row.workspace_id);
  }
  const work = [...workIds].map(id => ({ field: "work_id", op: "=" as const, value: id }));
  if (!workspaceIds.size) return [{ any: work }];
  // Match the same work_id ?? workspace_id precedence as directory grouping.
  // CNF expresses work-match OR (missing work_id AND workspace-match).
  return [
    { any: [...work, { field: "work_id", op: "is_null", value: "" }] },
    { any: [...work, ...[...workspaceIds].map(id => ({ field: "workspace_id", op: "=" as const, value: id }))] },
  ];
}
export function buildHumanStudy(kind: HumanStudy, allRows: StudyMessage[], workspaces: StudyWorkspace[], days: number, now: number, timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone): HumanStudyPreview {
  const byId = new Map(workspaces.map(workspace => [workspace.id, workspace]));
  const identity = (row: StudyMessage) => workspaceIdentity(byId.get(row.work_id ?? row.workspace_id ?? ""));
  const at = (row: StudyMessage) => Date.parse(row.sent_at);
  const window = (end: number) => allRows.filter(row => at(row) >= end - days * day && at(row) < end);
  const current = window(now), writing = current.filter(positive);
  const pointTimes = Array.from({ length: 31 }, (_, index) => now - (30 - index) * day);
  if (kind === "conversation-lengths") {
    const totals = new Map<string, number>();
    for (const row of writing) totals.set(row.conversation_id, (totals.get(row.conversation_id) ?? 0) + row.typed_words);
    const bands = [{ label: "1–49 words", max: 50 }, { label: "50–199 words", max: 200 }, { label: "200–499 words", max: 500 }, { label: "500–999 words", max: 1000 }, { label: "1,000–1,999 words", max: 2000 }, { label: "2,000+ words", max: Infinity }];
    const counts = bands.map(() => 0);
    for (const total of totals.values()) counts[bands.findIndex(band => total < band.max)]++;
    return { kind: "profile", valueLabel: "Human words per conversation", sampleLabel: `${totals.size} conversations · ${writing.length} written messages`,
      rows: bands.map((band, index) => ({ label: band.label, count: counts[index], value: totals.size ? counts[index] / totals.size : 0 })),
      detail: `Fixed word-count bands use each conversation's human-written total over the last ${days} days. Bar length is the share of conversations in that band; a single very long conversation cannot stretch the other bands flat. Conversations without human writing are excluded.` };
  }
  if (["volume", "message-average", "conversation-average", "workspace-average"].includes(kind)) {
    const measure = (rows: StudyMessage[]) => {
      let selected = rows.filter(positive);
      if (kind === "workspace-average") selected = selected.filter(row => identity(row));
      const denominator = kind === "volume" ? 1 : kind === "message-average" ? selected.length : kind === "conversation-average" ? unique(selected.map(row => row.conversation_id)) : unique(selected.map(identity));
      return denominator ? sum(selected) / denominator : null;
    };
    // Thirty historical endpoints, each the same elapsed N-day window, plus
    // today's endpoint. Empty volume windows are zero; undefined means stay gaps.
    const points = pointTimes.map(end => measure(window(end)));
    const historical = points.slice(0, -1).filter((value): value is number => value !== null);
    // A quiet current window can still have history; entirely absent writing
    // evidence should keep this card out of recommendations.
    const hasVolumeHistory = kind !== "volume" || points.some(point => point !== null && point > 0);
    const selected = kind === "workspace-average" ? writing.filter(row => identity(row)) : writing;
    const denominator = kind === "workspace-average" ? `${unique(selected.map(identity))} workspace directories · ${unique(selected.map(row => row.conversation_id))} conversations` : kind === "conversation-average" ? `${unique(writing.map(row => row.conversation_id))} conversations · ${writing.length} messages` : `${writing.length} written messages`;
    return { kind: "trend", value: hasVolumeHistory ? points.at(-1) ?? undefined : undefined, valueLabel: kind === "volume" ? `words / trailing ${days}d` : kind === "message-average" ? "words / message" : kind === "conversation-average" ? "words / conversation" : "words / workspace directory", sampleLabel: denominator,
      points, pointTimes, lookbackDays: days, baseline: historical.length ? historical.reduce((a, b) => a + b, 0) / historical.length : undefined,
      detail: kind === "workspace-average" ? `${writing.length - selected.length} written messages lack a mapped directory and are excluded. Workspaces sharing a repository and exact directory are combined, regardless of conversation or work ID.` : `Each point is a trailing ${days}-day ${kind === "volume" ? "word total" : "ratio of window totals"}. The norm is the mean of the prior 30 daily endpoints${kind === "volume" ? ", including zero-writing windows" : "; empty denominators remain gaps"}.` };
  }
  if (kind === "hours") {
    const format = new Intl.DateTimeFormat("en-US", { hour: "numeric", hourCycle: "h23", timeZone });
    const hours = Array.from({ length: 24 }, (_, hour) => ({ label: `${hour.toString().padStart(2, "0")}:00`, value: 0 }));
    for (const row of writing) hours[Number(format.format(new Date(row.sent_at)))].value += row.typed_words;
    return { kind: "hours", timeZone, valueLabel: "Words by local hour", rows: hours, sampleLabel: `${writing.length} written messages · ${timeZone}`, detail: "All dates in the selected window are combined by local hour of day. Empty hours are zero. Repeated daylight-saving hours contribute to the same clock hour." };
  }
  const contributions = current.filter(human), assessed = contributions.filter(row => !row.text_truncated);
  const signals = assessed.map(row => ({ row, ...contributionSignals(row) }));
  const examples = signals.filter(signal => signal.image || signal.file || signal.paste);
  const feedback = signals.filter(signal => signal.feedback && signal.row.typed_words > 0);
  const evidenceRows = kind === "examples" ? examples.slice(-2).reverse() : kind === "contribution-patterns" ? [...signals.filter(signal => signal.continuation).slice(-1), ...feedback.slice(-1)] : feedback.slice(-2).reverse();
  const evidence = evidenceRows.map(signal => {
    const { row, typed } = signal;
    const context = (row.segments ?? []).filter(span => ["attachment", "pasted"].includes(span.category ?? "")).map(span => span.text).join(" ");
    const label = kind === "examples" ? [signal.image ? "Image / screenshot" : "", signal.paste ? "Pasted text / code" : "", signal.file ? "File / link reference" : ""].filter(Boolean).join(" · ") : signal.continuation ? "Brief continuation" : "Concrete feedback cue";
    return { id: row.id, text: (kind === "examples" ? [typed, context].filter(Boolean).join("\n") : typed).slice(0, 220), label, url: queryURL("writing_messages", { ...EMPTY_QUERY, where: [{ field: "id", op: "=", value: row.id }] }) };
  });
  const ratio = (n: number, denominator = assessed.length) => denominator ? n / denominator : 0;
  const samples = `${assessed.length} assessed contributions · ${contributions.length - assessed.length} clipped messages excluded`;
  if (kind === "specific-feedback") {
    const authored = signals.filter(signal => signal.row.typed_words > 0);
    const matched = new Set(allRows.filter(row => row.typed_words > 0 && !row.text_truncated && contributionSignals(row).feedback).map(row => row.id));
    const points = pointTimes.map(end => {
      const assessed = window(end).filter(row => row.typed_words > 0 && !row.text_truncated);
      return assessed.length ? assessed.filter(row => matched.has(row.id)).length / assessed.length : null;
    });
    const historical = points.slice(0, -1).filter((point): point is number => point !== null);
    return { kind: "trend", format: "percent", value: authored.length ? feedback.length / authored.length : undefined, valueLabel: "with concrete feedback cues", sampleLabel: `${feedback.length} of ${authored.length} assessed written messages`, evidence, points, pointTimes, lookbackDays: days,
      baseline: historical.length ? historical.reduce((a, b) => a + b, 0) / historical.length : undefined,
      detail: `Each point is the share of assessed human-written messages matching concrete feedback cues during the trailing ${days} days. The norm averages the preceding 30 daily ratios; empty denominators remain gaps. Requires a direction/correction/constraint cue plus a reason, condition, number, quoted target, path, URL, paste, or attachment. Clipped messages are excluded. These are candidates for inspection, not a quality score.` };
  }
  if (kind === "examples") {
    const groups = new Map<string, { rows: StudyMessage[]; workspace: StudyWorkspace; count: number }>();
    for (const signal of signals) {
      const key = identity(signal.row), workspace = byId.get(signal.row.work_id ?? signal.row.workspace_id ?? "");
      if (!key || !workspace) continue;
      const group = groups.get(key) ?? { rows: [], workspace, count: 0 };
      group.rows.push(signal.row);
      if (signal.image || signal.file || signal.paste) group.count++;
      groups.set(key, group);
    }
    const workspaceRows = [...groups.values()].sort((a, b) => b.rows.length - a.rows.length || (a.workspace.location ?? "").localeCompare(b.workspace.location ?? "")).slice(0, 5).map(group => ({
      label: `${group.workspace.repository_name ?? "Unspecified"} / ${group.workspace.location?.replace(/\/+$/, "").split("/").at(-1)}`, location: group.workspace.location ?? "", count: group.count, total: group.rows.length, value: group.count / group.rows.length,
      url: queryURL("writing_messages", { ...EMPTY_QUERY, where: [...directoryMessageFilters(group.rows), { any: ["typed_words", "pasted_words", "attachment_words"].map(field => ({ field, op: ">" as const, value: "0" })) }, { field: "sent_at", op: ">=", value: `-${days}d` }, { field: "sent_at", op: "<", value: "+0s" }] }, { days, field: "sent_at", mode: "relative" }),
    }));
    return { kind: "composition", value: assessed.length ? ratio(examples.length) : undefined, valueLabel: "with supplied context", sampleLabel: samples, evidence, workspaceRows,
    rows: [{ label: "Images / screenshots", value: ratio(signals.filter(s => s.image).length), count: signals.filter(s => s.image).length }, { label: "Pasted text / code", value: ratio(signals.filter(s => s.paste).length), count: signals.filter(s => s.paste).length }, { label: "File references / links", value: ratio(signals.filter(s => s.file).length), count: signals.filter(s => s.file).length }],
    detail: "Share of assessed human contributions in this window, including context-only messages. A message may include multiple formats, so rows overlap. Directory comparisons show the five most active mapped directories, using each directory’s assessed contributions as its denominator. Images include screenshots and other images; pointers and pastes do not prove an example was useful. Code/file-change attribution is not inferred." };
  }
  const continuation = signals.filter(s => s.continuation).length, concrete = feedback.length, contextOnly = signals.filter(s => !s.row.typed_words).length;
  return { kind: "composition", valueLabel: "Observed contribution patterns", sampleLabel: samples, evidence, rows: [
    { label: "Brief continuations", count: continuation, value: ratio(continuation) },
    { label: "Concrete feedback cues", count: concrete, value: ratio(concrete) },
    { label: "Supplied context only", count: contextOnly, value: ratio(contextOnly) },
    { label: "Other written input", count: signals.length - continuation - concrete - contextOnly, value: ratio(signals.length - continuation - concrete - contextOnly) },
  ], detail: "Continuation requires an exact short approval/proceed phrase with no paste or attachment. Other short messages can be substantive. Concrete feedback uses explicit cues, not message length; the remaining messages are unclassified, not low value." };
}

async function postRows<T>(dataset: string, request: unknown, signal?: AbortSignal): Promise<{ rows: T[]; total: number }> {
  const controller = new AbortController(), abort = () => controller.abort(signal?.reason);
  signal?.addEventListener("abort", abort, { once: true });
  if (signal?.aborted) abort();
  const timer = setTimeout(() => controller.abort(new Error("Contribution evidence timed out. Try a shorter window.")), 20000);
  try {
    const response = await fetch(`/api/query/${dataset}/rows-v2`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ version: 2, profile: "qt-sqlite-v1", ...request as object }), signal: controller.signal });
    const body = await response.json();
    if (!response.ok) throw new Error(body.error?.message ?? body.error ?? "Contribution evidence could not be loaded.");
    return body;
  } finally { clearTimeout(timer); signal?.removeEventListener("abort", abort); }
}
export function createHumanStudyLoader(now: number, schema: FieldSchema, signal?: AbortSignal) {
  const batches = new Map<number, Promise<EvidenceBatch>>();
  // Directory IDs must come from the same window's evidence batch.
  const directories = new Map<number, Promise<StudyWorkspace[]>>();
  function load(days: number) {
    const lookback = days + 30;
    let pending = batches.get(lookback);
    if (!pending) {
      pending = (async () => {
        const key = `${typeof location === "undefined" ? "local" : location.origin}:${schema.name}:${lookback}`;
        const cached = messageCache.get(key);
        if (cached && Date.now() - cached.at < 60000) return cached.batch;
        const from = new Date(now - lookback * day).toISOString(), until = new Date(now).toISOString();
        const read = (offset: number) => postRows<StudyMessage>("writing_messages", { select: schema.fields.map(field => field.name), where: [{ field: "sent_at", op: ">=", value: from }, { field: "sent_at", op: "<", value: until }, { any: ["typed_words", "pasted_words", "attachment_words"].map(field => ({ field, op: ">", value: "0" })) }], orderBy: [{ field: "sent_at", dir: "asc" }, { field: "id", dir: "asc" }], limit: 500, offset }, signal);
        const first = await read(0), rows = [...first.rows];
        if (first.total > 20000) throw new Error("This contribution study exceeds 20,000 messages. Choose a shorter window; no partial score is shown.");
        const offsets = Array.from({ length: Math.max(0, Math.ceil(first.total / 500) - 1) }, (_, index) => (index + 1) * 500);
        let cursor = 0;
        await Promise.all(Array.from({ length: 3 }, async () => {
          while (cursor < offsets.length) {
            const page = await read(offsets[cursor++]);
            if (page.total !== first.total) throw new Error("The contribution evidence changed while loading. Refresh to try again.");
            rows.push(...page.rows);
          }
        }));
        const unique = [...new Map(rows.map(row => [row.id, row])).values()];
        if (unique.length !== first.total) throw new Error("Contribution evidence was incomplete. Refresh to try again; no partial score is shown.");
        const batch = { rows: unique.sort((a, b) => a.sent_at.localeCompare(b.sent_at) || a.id.localeCompare(b.id)), now };
        if (!signal?.aborted) {
          if (messageCache.size >= 2) messageCache.delete(messageCache.keys().next().value!);
          messageCache.set(key, { at: Date.now(), batch });
        }
        return batch;
      })();
      batches.set(lookback, pending);
    }
    return pending;
  }
  return async (kind: HumanStudy, query: QueryState, days: number): Promise<HumanStudyPreview> => {
    const batch = await load(days), allRows = batch.rows, source = query.grouping?.source ?? query;
    const where = source.where.filter(term => !("field" in term && term.field === "sent_at" && !term.negated && (term.op === ">=" && term.value === `-${days}d` || term.op === "<" && term.value === "+0s")));
    const filtered = applyQuery(allRows, { ...EMPTY_QUERY, where, limit: 20000 }, schema, { now: batch.now }).rows as StudyMessage[];
    let workspaces: StudyWorkspace[] = [];
    if (kind === "workspace-average" || kind === "examples") {
      let pending = directories.get(days);
      if (!pending) {
        pending = (async () => {
          const ids = [...new Set(allRows.map(row => row.work_id ?? row.workspace_id).filter(Boolean))].sort();
          const key = JSON.stringify([typeof location === "undefined" ? "local" : location.origin, ids]);
          const cached = directoryCache.get(key);
          if (cached && Date.now() - cached.at < 60000) return cached.rows;
          const rows: StudyWorkspace[] = [], offsets = Array.from({ length: Math.ceil(ids.length / 50) }, (_, index) => index * 50);
          let cursor = 0;
          await Promise.all(Array.from({ length: 3 }, async () => { while (cursor < offsets.length) {
            const index = offsets[cursor++];
            const where: WhereTerm[] = [{ any: ids.slice(index, index + 50).map(id => ({ field: "id", op: "=" as const, value: id! })) }];
            const page = await postRows<StudyWorkspace>("library", { select: ["id", "location", "repository_name"], where, orderBy: [{ field: "id", dir: "asc" }], limit: 100, offset: 0 }, signal);
            if (page.total > page.rows.length) throw new Error("Workspace directory lookup was incomplete. No partial average is shown.");
            rows.push(...page.rows);
          } }));
          if (!signal?.aborted) {
            if (directoryCache.size >= 2) directoryCache.delete(directoryCache.keys().next().value!);
            directoryCache.set(key, { at: Date.now(), rows });
          }
          return rows;
        })();
        directories.set(days, pending);
      }
      workspaces = await pending;
    }
    return buildHumanStudy(kind, filtered, workspaces, days, batch.now);
  };
}
