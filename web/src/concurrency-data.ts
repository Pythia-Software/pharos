export type Interval = [number, number];
export type ConcurrencyThread = {
  id: string; conversation_id: string; work_id: string; workspace_id: string; workspace_name: string;
  repository: string; title: string; provider: string; model: string; agent_depth: number;
  turns: Interval[]; model_spans: Interval[]; root_conversation_id?: string;
  continuous_spans?: Interval[];
};
export type ConcurrencyData = {
  generated_at: string; timezone: string; period_start: string; period_end: string; days: string[];
  threads: ConcurrencyThread[];
  coverage: { conversations: number; mirrors_removed: number; unexplained_waits_excluded: number; timing: string; catalog: string };
};
export type ConcurrencyMode = "model" | "user";
export type ConcurrencyCounting = "workspace" | "agent";
export type Segment = { start: number; end: number; unit: string; workspace: string; bottom: number; conversation: string; running: boolean };
export type ConcurrencyMetrics = { average: number; parallel: number; peak: number; working: number };
export const minute = 60_000;

export function formatConcurrencyDuration(ms: number): string {
  if (ms < minute) return `${Math.round(ms / 1000)}s`;
  const minutes = Math.round(ms / minute);
  return minutes < 60 ? `${minutes}m` : `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export function mergeIntervals(intervals: Interval[], gap = 0): Interval[] {
  const result: Interval[] = [];
  for (const [start, end] of intervals.filter(([a, b]) => Number.isFinite(a) && Number.isFinite(b) && b > a).slice().sort((a, b) => a[0] - b[0])) {
    const previous = result.at(-1);
    if (previous && start - previous[1] <= gap) previous[1] = Math.max(previous[1], end);
    else result.push([start, end]);
  }
  return result;
}

export function clipIntervals(intervals: Interval[], bounds: Interval): Interval[] {
  return intervals.filter(([a, b]) => a < bounds[1] && b > bounds[0]).map(([a, b]) => [Math.max(a, bounds[0]), Math.min(b, bounds[1])]);
}

export function concurrencyUnit(thread: ConcurrencyThread, counting: ConcurrencyCounting): string {
  return counting === "workspace" ? thread.workspace_id : thread.root_conversation_id ?? thread.id;
}

export function concurrencySpans(threads: ConcurrencyThread[], mode: ConcurrencyMode, gap: number, counting: ConcurrencyCounting = "workspace"): Map<string, Interval[]> {
  const units = new Map<string, Interval[]>();
  for (const thread of threads) {
    if (mode === "user" && thread.agent_depth > 0) continue;
    const spans = mode === "model" ? thread.model_spans : thread.continuous_spans ?? mergeIntervals(thread.turns, gap * minute);
    const unit = concurrencyUnit(thread, counting);
    let collected = units.get(unit);
    if (!collected) { collected = []; units.set(unit, collected); }
    for (const span of spans) collected.push(span);
  }
  // Gap bridging happens within a conversation before counting-unit union: a
  // different conversation cannot manufacture a continuous work thread.
  return new Map(Array.from(units, ([id, spans]) => [id, mergeIntervals(spans)]));
}

export function workspaceSpans(threads: ConcurrencyThread[], mode: ConcurrencyMode, gap: number): Map<string, Interval[]> {
  return concurrencySpans(threads, mode, gap);
}

// Table rows always describe root conversations, independently of the chart's
// counting choice. Linked delegated model activity belongs to that root only.
export function conversationTimings(threads: ConcurrencyThread[], gap: number): Map<string, { model: number; threaded: number }> {
  const model = concurrencySpans(threads, "model", gap, "agent");
  const threaded = concurrencySpans(threads, "user", gap, "agent");
  const total = (spans: Interval[] = []) => spans.reduce((sum, [a, b]) => sum + b - a, 0);
  return new Map(threads.filter(thread => thread.agent_depth === 0).map(thread => [thread.id, {
    model: total(model.get(thread.id)), threaded: total(threaded.get(thread.id)),
  }]));
}

export function workSessions(threads: ConcurrencyThread[]): Interval[] {
  return mergeIntervals(threads.flatMap(t => t.agent_depth === 0 ? [...t.turns, ...t.model_spans] : t.model_spans), 60 * minute);
}

export function concurrencyMetrics(units: Map<string, Interval[]>, sessions: Interval[], bounds: Interval): ConcurrencyMetrics {
  const events: Array<[number, number, number]> = [];
  for (const spans of units.values()) for (const [a, b] of clipIntervals(spans, bounds)) events.push([a, 1, 0], [b, -1, 0]);
  for (const [a, b] of clipIntervals(sessions, bounds)) events.push([a, 0, 1], [b, 0, -1]);
  events.sort((a, b) => a[0] - b[0]);
  let count = 0, working = 0, area = 0, parallel = 0, peak = 0, inSession = 0, previous = bounds[0];
  for (let i = 0; i < events.length;) {
    const at = events[i][0], duration = at - previous;
    area += duration * count;
    if (duration > 0) peak = Math.max(peak, count);
    if (inSession > 0) {
      working += duration;
      if (count >= 2) parallel += duration;
    }
    while (i < events.length && events[i][0] === at) { count += events[i][1]; inSession += events[i][2]; i++; }
    previous = at;
  }
  const elapsed = bounds[1] - bounds[0];
  return { average: elapsed > 0 ? area / elapsed : 0, parallel: working ? parallel / working : 0, peak, working };
}

export function stackSegments(threads: ConcurrencyThread[], mode: ConcurrencyMode, gap: number, order: string[], bounds: Interval, counting: ConcurrencyCounting = "workspace", compact = false): Segment[] {
  type Event = { at: number; unit: string; conversation: string; delta: number; running: boolean };
  const events: Event[] = [];
  const add = (thread: ConcurrencyThread, spans: Interval[], running: boolean) => {
    // Replay earlier activity so zooming crops the existing layout rather than
    // assigning different rows at the left edge of the zoomed view.
    const unit = concurrencyUnit(thread, counting);
    for (const [a, b] of clipIntervals(spans, [-Infinity, bounds[1]])) events.push({ at: a, unit, conversation: thread.id, delta: 1, running }, { at: b, unit, conversation: thread.id, delta: -1, running });
  };
  for (const thread of threads) {
    if (mode === "model") add(thread, mergeIntervals(thread.model_spans), false);
    else {
      if (thread.agent_depth === 0) {
        add(thread, thread.continuous_spans ?? mergeIntervals(thread.turns, gap * minute), false);
        add(thread, mergeIntervals(thread.turns), true);
      }
    }
  }
  events.sort((a, b) => a.at - b.at);
  const repositories = new Map<string, string>();
  const byId = new Map(threads.map(thread => [thread.id, thread]));
  for (const thread of threads.slice().sort((a, b) => a.agent_depth - b.agent_depth)) {
    const unit = concurrencyUnit(thread, counting);
    if (!repositories.has(unit)) repositories.set(unit, thread.repository || thread.workspace_id);
  }
  const repositoryOrder = new Map<string, number>();
  for (const unit of order) {
    const repo = repositories.get(unit)!;
    if (!repositoryOrder.has(repo)) repositoryOrder.set(repo, repositoryOrder.size);
  }
  // Group simultaneous arrivals by repo; retain caller order within a repo.
  const layoutOrder = order.slice().sort((a, b) => repositoryOrder.get(repositories.get(a)!)! - repositoryOrder.get(repositories.get(b)!)!);
  const rank = new Map(layoutOrder.map((unit, index) => [unit, index]));
  let previousRows = new Map<string, number>();
  const lastRows = new Map<string, number>(), rowRepositories = new Map<number, string>();
  const packRows = (units: string[]) => {
    const rows = new Map<string, number>(), free = new Set(units.map((_, row) => row));
    const place = (unit: string, row: number) => { rows.set(unit, row); free.delete(row); };
    // Keep continuing units in place wherever the new packed height allows.
    for (const unit of units) {
      const row = previousRows.get(unit);
      if (row !== undefined && free.has(row)) place(unit, row);
    }
    const pending = units.filter(unit => !rows.has(unit));
    const nearest = (unit: string, candidates: number[]) => {
      const target = lastRows.get(unit) ?? 0;
      return candidates.sort((a, b) => Math.abs(a - target) - Math.abs(b - target) || a - b)[0];
    };
    // A returning unit gets its old row first if that row still belongs to
    // its repo. Then other units can inherit the repo's vacated rows.
    for (const unit of pending) {
      const row = lastRows.get(unit);
      if (row !== undefined && free.has(row) && rowRepositories.get(row) === repositories.get(unit)) place(unit, row);
    }
    for (const unit of pending) {
      if (rows.has(unit)) continue;
      const matching = Array.from(free).filter(row => rowRepositories.get(row) === repositories.get(unit));
      if (matching.length) place(unit, nearest(unit, matching));
    }
    for (const unit of pending) {
      if (!rows.has(unit)) place(unit, nearest(unit, Array.from(free)));
    }
    for (const [unit, row] of rows) {
      lastRows.set(unit, row); rowRepositories.set(row, repositories.get(unit)!);
    }
    previousRows = rows;
    return rows;
  };
  const active = new Map<string, Set<string>>(), running = new Map<string, Set<string>>(), chosen = new Map<string, string>();
  const segments: Segment[] = [];
  const open = new Map<string, Segment>();
  let rows = new Map<string, number>(), units: string[] = [];
  for (let i = 0; i < events.length;) {
    const at = events[i].at;
    let layoutChanged = false;
    while (i < events.length && events[i].at === at) {
      const event = events[i++], map = event.running ? running : active;
      if (!map.has(event.unit)) { map.set(event.unit, new Set()); if (!event.running) layoutChanged = true; }
      if (event.delta > 0) map.get(event.unit)!.add(event.conversation);
      else map.get(event.unit)!.delete(event.conversation);
      if (!map.get(event.unit)!.size) {
        map.delete(event.unit);
        if (!event.running) layoutChanged = true;
      }
    }
    for (const [unit, segment] of open) if (!active.has(unit)) { segment.end = Math.max(segment.start, at); open.delete(unit); }
    const end = events[i]?.at;
    if (end === undefined || end <= at) continue;
    for (const unit of chosen.keys()) if (!active.has(unit)) chosen.delete(unit);
    // Only concurrent units participate in this event. Scanning the entire
    // catalog at every boundary made a multi-day snapshot quadratic.
    if (layoutChanged) {
      units = Array.from(active.keys()).filter(unit => rank.has(unit)).sort((a, b) => rank.get(a)! - rank.get(b)!);
      rows = packRows(units);
    }
    for (const unit of units) {
      const conversations = active.get(unit)!;
      const previous = chosen.get(unit);
      const conversation = previous && conversations.has(previous) ? previous : conversations.values().next().value!;
      chosen.set(unit, conversation);
      if (end > bounds[0]) {
        const bottom = rows.get(unit)!, isRunning = mode === "model" || !!running.get(unit)?.size;
        const previous = open.get(unit);
        if (compact && previous && previous.bottom === bottom && previous.conversation === conversation && previous.running === isRunning) continue;
        if (previous) previous.end = Math.max(previous.start, at);
        const segment = { start: Math.max(at, bounds[0]), end: compact ? bounds[1] : end, unit, workspace: byId.get(conversation)!.workspace_id, conversation, bottom, running: isRunning };
        segments.push(segment);
        if (compact) open.set(unit, segment);
      }
    }
  }
  return segments;
}

export function zonedDayBounds(day: string, timezone: string): Interval {
  const [year, month, date] = day.split("-").map(Number);
  const formatter = new Intl.DateTimeFormat("en-US", { timeZone: timezone, timeZoneName: "longOffset" });
  const midnight = (utc: number) => {
    let instant = utc;
    for (let i = 0; i < 3; i++) {
      const name = formatter.formatToParts(instant).find(part => part.type === "timeZoneName")?.value ?? "GMT";
      const offset = /GMT([+-])(\d{2}):(\d{2})/.exec(name);
      const minutes = offset ? (Number(offset[2]) * 60 + Number(offset[3])) * (offset[1] === "-" ? -1 : 1) : 0;
      instant = utc - minutes * minute;
    }
    return instant;
  };
  const utc = Date.UTC(year, month - 1, date);
  return [midnight(utc), midnight(utc + 24 * 60 * minute)];
}

// Integrate simultaneous occupancy, then sample its trailing time average.
// Averaging elapsed time (rather than event samples) gives idle gaps and long
// intervals their actual weight. The ribbons retain their exact identities.
export function concurrencyAverage(spans: Map<string, Interval[]>, bounds: Interval, windowMs: number, steps = 600, historyStart = bounds[0]): Array<{ at: number; value: number }> {
  if (!(windowMs > 0) || !Number.isFinite(windowMs) || bounds[1] <= bounds[0]) return [];
  const first = Math.max(historyStart, bounds[0] - windowMs);
  const changes = new Map<number, number>([[first, 0], [bounds[1], 0]]);
  for (const intervals of spans.values()) for (const [start, end] of clipIntervals(intervals, [first, bounds[1]])) {
    changes.set(start, (changes.get(start) ?? 0) + 1); changes.set(end, (changes.get(end) ?? 0) - 1);
  }
  let total = 0, count = 0, previous = first;
  const events = [...changes].sort((a, b) => a[0] - b[0]).map(([at, change]) => {
    total += (at - previous) * count; count += change; previous = at;
    return { at, total, count };
  });
  const integral = (at: number) => {
    let low = 0, high = events.length - 1;
    while (low < high) { const middle = Math.ceil((low + high) / 2); if (events[middle].at <= at) low = middle; else high = middle - 1; }
    const event = events[low];
    return event.total + (at - event.at) * event.count;
  };
  const samples = Math.max(1, Math.min(2000, Math.floor(steps)));
  return Array.from({ length: samples + 1 }, (_, index) => {
    const at = bounds[0] + (bounds[1] - bounds[0]) * index / samples, start = Math.max(first, at - windowMs);
    return { at, value: at === start ? events[0].count : (integral(at) - integral(start)) / (at - start) };
  });
}

export function ribbonPath(segments: Segment[], x: (time: number) => number, y: (count: number) => number): string {
  // Each disjoint region is its own polygon. Vertical steps preserve integer
  // concurrency; smoothing would suggest fractional workspaces or fake time.
  const groups: Segment[][] = [];
  for (const segment of segments) {
    const last = groups.at(-1);
    if (last && last.at(-1)!.end === segment.start) {
      const previous = last.at(-1)!;
      // Other units' events do not add corners to this ribbon. Keep exact
      // hit-test segments, but omit collinear vertices from the SVG geometry.
      if (previous.bottom === segment.bottom) last[last.length - 1] = { ...previous, end: segment.end };
      else last.push(segment);
    }
    else groups.push([segment]);
  }
  return groups.map(group => {
    const points = [...group.flatMap(s => [[x(s.start), y(s.bottom + 1)], [x(s.end), y(s.bottom + 1)]]), ...group.slice().reverse().flatMap(s => [[x(s.end), y(s.bottom)], [x(s.start), y(s.bottom)]])];
    return points.map(([px, py], i) => `${i ? "L" : "M"}${px.toFixed(2)},${py.toFixed(2)}`).join("") + "Z";
  }).join("");
}
