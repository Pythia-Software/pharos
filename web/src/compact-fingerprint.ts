// Standalone compact-image prototype. Deliberately not wired into the live
// fingerprint or timeline: selection and appearance are being reviewed first.
import {conversationRibbon, parallelEnvelopes, ribbonSegment} from './conversation-fingerprint-model';
import {mainSession, stateColor, type LabelConversation, type LabelTool, type Taxonomy} from './conversation-labels';
import {eventGroup, glyphPath, layoutEventArcs, linkedTarget, weakTarget, type AgentEventLink, type ArcLayout, type EventLayout, type LayoutEvent} from './event-link-layout';
import {mergeAgentEvents} from './event-merge';
import {laneCandidates, markGap, placeMarks} from './ribbon-marks';

export const compactFingerprintProfiles = {
 wide: {ratio: 8, events: 500, arcs: 4},
 card: {ratio: 4, events: 500, arcs: 6},
} as const;
export const compactFingerprintMaximumEvents = 500;
export type CompactFingerprintPacking = {scale: 1; gap: number; stacks: number};
export type CompactFingerprintProfile = keyof typeof compactFingerprintProfiles;
export type CompactFingerprintInput = {conversation: LabelConversation; events: AgentEventLink[]; taxonomy?: Taxonomy};
type Candidate = {event: AgentEventLink; x: number; targetX: number | null; target: string | null; strength: number; phase: number; session: string};
type ToolCandidate = {tool: LabelTool; x: number; endX: number | null; durationMs: number | null; phase: number; error: boolean};
export type CompactArc = ArcLayout & {sweep: 'rise' | 'return'};
type CompactEventLayout = Omit<EventLayout, 'arcs'> & {arcs: CompactArc[]};
export type CompactFingerprintPrepared = ReturnType<typeof prepareCompactFingerprint>;
export type CompactFingerprintLayout = ReturnType<typeof layoutCompactFingerprint>;
const phaseOf = (x: number) => Math.min(7, Math.max(0, Math.floor(x * 8)));
const time = (value: string | null | undefined) => {const n = value ? Date.parse(value) : NaN; return Number.isFinite(n) ? n : null;};
const clamp = (n: number, lo = 0, hi = 1) => Math.max(lo, Math.min(hi, n));
const errorStatus = (status: string) => /error|fail|block|interrupt/i.test(status);
const toolTiming = (tool: LabelTool) => {
 const start = time(tool.start), end = time(tool.end);
 const durationMs = start !== null && end !== null && end >= start ? end - start
  : tool.duration_ms !== null && tool.duration_ms !== undefined && Number.isFinite(tool.duration_ms) && tool.duration_ms >= 0 ? tool.duration_ms : null;
 return {durationMs, end: start !== null && durationMs !== null ? start + durationMs : null};
};
/** Compact-only curve variants. Chronology and target identities stay fixed;
 * the target's stable hash varies the side and which end meets the axis. */
const arcStyle = (key: string) => {let value = 2166136261; for (let i = 0; i < key.length; i++) value = Math.imul(value ^ key.charCodeAt(i), 16777619); return value >>> 0;};
export function compactArcY(arc: CompactArc, axis: number, x: number) {
 const u = clamp((arc.sweep === 'return' ? arc.x1 - x : x - arc.x0) / arc.rise);
 return axis - arc.side * arc.lift * (1 - (1 - u) * (1 - u));
}
function variedArc(source: ArcLayout, axis: number): CompactArc {
 const style = arcStyle(source.key), arc: CompactArc = {...source, side: style & 1 ? -1 : 1, sweep: style & 2 ? 'return' : 'rise'};
 const bend = Math.min(arc.rise, arc.x1 - arc.x0), xs = Array.from({length: 17}, (_, i) => arc.sweep === 'rise' ? arc.x0 + bend * i / 16 : arc.x1 - bend + bend * i / 16);
 xs.unshift(arc.x0); xs.push(arc.x1);
 arc.path = 'M' + [...new Set(xs)].map(x => `${Number(x.toFixed(2))} ${Number(compactArcY(arc, axis, x).toFixed(2))}`).join('L');
 return arc;
}

/** Read the complete input once; classification and target matching belong to
 * the source projection, not to image encoding. No prefix sampling here. */
export function prepareCompactFingerprint(input: CompactFingerprintInput) {
 const {conversation, events, taxonomy = []} = input;
 const activities = conversation.activity ?? [], messages = conversation.messages;
 const messageByID = new Map(conversation.messages.map(m => [m.id, m]));
 const marks = mergeAgentEvents(events);
 // The label ribbon has one equally spaced slot per retained message. Tool
 // calls annotate that axis: inserting calls must not displace message colors
 // or create empty label columns. A tool-only conversation has its own neutral
 // sequence instead of acquiring invented message classifications.
 type Observation = {id: string; at: number | null; order: number; message?: typeof messages[number]; tool?: LabelTool};
 const stream: Observation[] = messages.map((message, order) => ({id: 'message:' + message.id, at: time(message.at), order, message}));
 const hasClock = stream.some(o => o.at !== null);
 const positionedTools = activities.filter(tool => tool.message_id && messageByID.has(tool.message_id) || !messages.length || hasClock && time(tool.start) !== null);
 for (const [order, tool] of positionedTools.entries()) {
  if (messages.length) continue;
  stream.push({id: 'tool:' + tool.id, at: time(tool.start), order, tool});
 }
 stream.sort((a, b) => a.at === null ? b.at === null ? a.order - b.order || a.id.localeCompare(b.id) : 1
  : b.at === null ? -1 : a.at - b.at || Number(!!b.message) - Number(!!a.message) || a.order - b.order || a.id.localeCompare(b.id));
 const positions = new Map<string, number>(), toolPositions = new Map<string, number>();
 stream.forEach((o, i) => {const x = (i + .5) / stream.length; if (o.message) positions.set(o.message.id, x); if (o.tool) toolPositions.set(o.tool.id, x);});
 const clocked = stream.flatMap(o => o.at !== null ? [{at: o.at, x: o.message ? positions.get(o.message.id)! : toolPositions.get(o.tool!.id)!}] : []);
 // Interpolate time only within each fixed message interval. Leading/trailing
 // calls use the outer half of the first/last timed message's slot, preserving
 // tool-only tails without stretching the label ribbon.
 const anchors = [...clocked];
 if (clocked.length) {
  let first = clocked[0].at, last = clocked.at(-1)!.at;
  for (const tool of positionedTools) {const at = time(tool.start), end = toolTiming(tool).end; if (at !== null) {first = Math.min(first, at); last = Math.max(last, at, end ?? at);}}
  if (first < clocked[0].at) anchors.unshift({at: first, x: Math.max(0, clocked[0].x - .5 / stream.length)});
  if (last > clocked.at(-1)!.at) anchors.push({at: last, x: Math.min(1, clocked.at(-1)!.x + .5 / stream.length)});
 }
 const projectTime = (at: number): number | null => {
  if (!anchors.length) return null;
  let lo = 0, hi = anchors.length;
  while (lo < hi) {const mid = (lo + hi) >>> 1; if (anchors[mid].at < at) lo = mid + 1; else hi = mid;}
  const right = anchors[Math.min(lo, anchors.length - 1)], left = anchors[Math.max(0, lo - 1)];
  const share = right.at === left.at ? 0 : clamp((at - left.at) / (right.at - left.at));
  return left.x + (right.x - left.x) * share;
 };
 for (const tool of positionedTools) {
  if (tool.message_id && positions.has(tool.message_id)) {toolPositions.set(tool.id, positions.get(tool.message_id)!); continue;}
  if (!messages.length) continue;
  const at = time(tool.start); if (at === null) continue;
  const x = projectTime(at); if (x !== null) toolPositions.set(tool.id, x);
 }
 // Bound drawing size without discarding the ending or stretching a label
 // run logarithmically. Each column covers an equal share of message evidence.
 const count = Math.min(40, stream.length), bands = Array.from({length: count}, (_, i) => {
  const observations = stream.slice(Math.floor(i * stream.length / count), Math.floor((i + 1) * stream.length / count));
  return {x: i / count, width: 1 / count, messages: observations.flatMap(o => o.message ? [o.message] : [])};
 });
 const breakdowns = bands.map(band => ribbonSegment(band.messages, taxonomy));
 // Retain the existing recorded-branch envelope, sampled on the new axis.
 // The shared timeline/message models and their rendering are untouched.
 const tokens = conversationRibbon(conversation), overlapByMessage = new Map(tokens.flatMap(token => token.messages.map(m => [m.id, token.parallelism] as const)));
 const branches = bands.map(band => ({x: band.x, width: band.width, token: {parallelism: Math.max(0, ...band.messages.map(m => overlapByMessage.get(m.id) ?? 0))}}));
 const candidates: Candidate[] = marks.flatMap(event => {
  const x = positions.get(event.message_id); if (x === undefined) return [];
  const target = linkedTarget(event), tx = target ? positions.get(target.message_id!) : undefined;
  // A link must refer to earlier represented evidence. Missing or later targets
  // are observations, never fabricated arcs back to the left edge.
  const linked = tx !== undefined && tx < x;
  return [{event, x, targetX: linked ? tx : null, target: linked ? target!.message_id! : null,
   strength: linked ? weakTarget(target!) ? 1 : 2 : 0,
   phase: phaseOf(x), session: messageByID.get(event.message_id)?.session_id ?? mainSession(conversation)}];
 });
 const tools: ToolCandidate[] = positionedTools.map(tool => {const x = toolPositions.get(tool.id)!, timing = toolTiming(tool), endX = timing.end === null ? null : projectTime(timing.end); return {tool, x, endX: endX === null ? null : Math.max(x, endX), durationMs: timing.durationMs, phase: phaseOf(x), error: errorStatus(tool.status)};});
 return {input, bands, branches, breakdowns, positions, candidates, tools, stream,
  totals: {messages: conversation.messages.length, events: events.length, mergedEvents: marks.length, tools: activities.length, unpositionedTools: activities.length - tools.length, branches: new Set([...conversation.messages.map(m => m.session_id ?? mainSession(conversation)), ...activities.map(t => t.session_id)]).size}};
}

function eventGeometry(candidates: Candidate[], links: ReadonlySet<string>, width: number, height: number, radius: number, packing: CompactFingerprintPacking) {
 const values: LayoutEvent[] = candidates.filter(c => links.has(c.event.id)).map(c => ({id: c.event.id, x: c.x * width, targetX: c.targetX === null ? null : c.targetX * width, targetKey: c.target, weak: c.strength === 1}));
 const layout = layoutEventArcs(values, {width, height, marker: radius * 1.1, minLift: radius * 2.2 + packing.scale, maxLift: height / 2 - radius * 1.1 - 2 * packing.scale, gap: packing.gap});
 const arcs = layout.arcs.map(a => variedArc(a, layout.axis)), byKey = new Map(arcs.map(a => [a.key, a])), radii = new Map(candidates.map(c => [c.event.id, radius * (c.target ? 1.1 : 1)]));
 const markers = placeMarks(candidates.map(c => {
  const arc = c.target && links.has(c.event.id) ? byKey.get(c.target) : undefined, x = c.x * width;
  const r = radii.get(c.event.id)!;
  return {id: c.event.id, x, r: extent(r, packing.scale), candidates: arc ? [{x, y: compactArcY(arc, layout.axis, x)}] : laneCandidates(height, extent(r, packing.scale), packing.gap).map(y => ({x, y}))};
 }), {gap: packing.gap}).map(mark => ({...mark, r: radii.get(mark.id)!, arc: links.has(mark.id) ? candidates.find(c => c.event.id === mark.id)?.target ?? null : null}));
 return {...layout, arcs, markers};
}

const extent = (radius: number, scale = 1) => radius * 1.18 + .5 * scale;
/** Limit both vertical stacks and the fraction of each label column covered
 * by symbols. Curves remain thin; glyphs cannot blanket a message column. */
function clearCoverage(marks: {x: number; x1?: number; r: number}[], width: number, height: number, columns: number, stacks = 2) {
 const edges = marks.flatMap(m => [[m.x - m.r, 1], [(m.x1 ?? m.x) + m.r, -1]] as const).sort((a, b) => a[0] - b[0] || a[1] - b[1]);
 let stacked = 0; for (const [, delta] of edges) {stacked += delta; if (stacked > stacks) return false;}
 const cell = width / Math.max(1, columns), covered = new Map<number, number>();
 for (const m of marks) for (let i = Math.max(0, Math.floor((m.x - m.r) / cell)); i <= Math.min(columns - 1, Math.floor(((m.x1 ?? m.x) + m.r) / cell)); i++) {
  const area = Math.max(0, Math.min((i + 1) * cell, (m.x1 ?? m.x) + m.r) - Math.max(i * cell, m.x - m.r)) * m.r * 2;
  covered.set(i, (covered.get(i) ?? 0) + area);
 }
 return [...covered.values()].every(area => area <= cell * height * .28);
}

/** Strict compact placement. Reject the whole tentative addition if it would
 * require a third nested arc, colliding marks/anchors, or a curve through a
 * different mark. Own marker-to-arc connections are intentional. */
function clearGeometry(layout: CompactEventLayout, width: number, height: number, columns: number, packing: CompactFingerprintPacking) {
 if (layout.arcs.some(a => a.level > 2)) return false;
 const marks = layout.markers.map(m => ({...m, r: extent(m.r, packing.scale)}));
 if (marks.some(m => m.overlap || m.x - m.r < 0 || m.x + m.r > width || m.y - m.r < 0 || m.y + m.r > height)) return false;
 if (!clearCoverage(marks, width, height, columns, packing.stacks)) return false;
 for (let i = 0; i < marks.length; i++) for (let j = i + 1; j < marks.length; j++) if (markGap(marks[i], marks[j]) < packing.scale) return false;
 for (let i = 0; i < layout.arcs.length; i++) for (const b of layout.arcs.slice(i + 1)) {
  const a = layout.arcs[i];
  if (Math.abs(a.x0 - b.x0) < 2.5 * packing.scale) return false;
  const lo = Math.max(a.x0, b.x0), hi = Math.min(a.x1, b.x1);
  for (let x = lo; x <= hi; x += .5) if (Math.abs(compactArcY(a, layout.axis, x) - compactArcY(b, layout.axis, x)) < packing.gap) return false;
 }
 for (const arc of layout.arcs) {
  for (const mark of marks) {
   if (mark.arc === arc.key) continue;
   // Sampling a bounded curve at subpixel spacing is cheap after selection.
   for (let x = Math.max(arc.x0, mark.x - mark.r - 1); x <= Math.min(arc.x1, mark.x + mark.r + 1); x += .4) {
    if (Math.hypot(x - mark.x, compactArcY(arc, layout.axis, x) - mark.y) < mark.r + packing.scale) return false;
   }
   if (Math.abs(mark.x - arc.x0) < mark.r + packing.scale && Math.abs(mark.y - compactArcY(arc, layout.axis, arc.x0)) < mark.r + 3 * packing.scale) return false;
  }
 }
 return true;
}

type CompactMark = {x: number; x1: number; y: number; r: number};
const toolTailRadius = .6;
function clearToolGeometry(mark: CompactMark, occupied: CompactMark[], layout: CompactEventLayout, width: number, height: number, columns: number, packing: CompactFingerprintPacking, extraCoverage: CompactMark[] = []) {
 if (mark.x - mark.r < 0 || mark.x1 + mark.r > width || mark.y - mark.r < 0 || mark.y + mark.r > height || occupied.some(m => markGap(mark, m) < packing.gap)) return false;
 if (!clearCoverage([...occupied, ...extraCoverage, mark], width, height, columns, packing.stacks)) return false;
 return layout.arcs.every(arc => {
  const anchorY = compactArcY(arc, layout.axis, arc.x0);
  if (arc.x0 > mark.x - mark.r - 1 && arc.x0 < mark.x1 + mark.r + 1 && Math.abs(anchorY - mark.y) < mark.r + 3) return false;
  for (let x = Math.max(arc.x0, mark.x - mark.r - 1); x <= Math.min(arc.x1, mark.x1 + mark.r + 1); x += .4) {
   const dx = Math.max(0, mark.x - x, x - mark.x1);
   if (Math.hypot(dx, compactArcY(arc, layout.axis, x) - mark.y) < mark.r + 1) return false;
  }
  return true;
 });
}

/** Greedy coverage, recomputed after each accepted mark. Strong relationships
 * win first; novelty spreads the remaining budget across kinds, groups,
 * earlier targets, sessions and eight phases. Stable ties survive rerenders. */
export function layoutCompactFingerprint(prepared: CompactFingerprintPrepared, profile: CompactFingerprintProfile = 'wide', options: {width?: number; eventLimit?: number; arcLimit?: number; showEvents?: boolean} = {}) {
 const spec = compactFingerprintProfiles[profile], width = options.width ?? 320, height = width / spec.ratio;
 if (!Number.isFinite(width) || width < 160 || width > 4096) throw new Error('Compact fingerprint width must be 160–4096 CSS pixels');
 const requested = options.eventLimit ?? spec.events;
 const eventLimit = Number.isFinite(requested) ? clamp(Math.floor(requested), 0, compactFingerprintMaximumEvents) : spec.events;
 // A larger budget permits more marks, never smaller marks or thinner strokes.
 // If full-size symbols cannot fit, geometry and coverage reject the addition.
 const packing: CompactFingerprintPacking = {scale: 1, gap: 1.5, stacks: Math.max(2, Math.min(10, Math.ceil(eventLimit / 50)))};
 const baseRadius = Math.max(1.8, Math.min(3.2, width / 140)), radius = baseRadius, bandY = 0, bandHeight = height;
 const arcLimit = Math.max(0, Math.min(spec.arcs, Math.floor(options.arcLimit ?? spec.arcs)));
 const selected: Candidate[] = [], rejected = new Set<string>();
 let links = new Set<string>(), layout = eventGeometry([], links, width, height, radius, packing);
 const kinds = new Set<string>(), groups = new Set<string>(), targets = new Set<string>(), phases = new Set<number>(), sessions = new Set<string>();
 const usedKinds = new Map<string, number>(), usedTargets = new Map<string, number>();
 const candidates = options.showEvents === false ? [] : prepared.candidates;
 const score = (c: Candidate) => c.strength * 100 + (!kinds.has(c.event.kind) ? 28 : 0) + (!groups.has(c.event.group) ? 18 : 0)
  + (c.target && !targets.has(c.target) ? 22 : 0) + (!phases.has(c.phase) ? 24 : 0) + (!sessions.has(c.session) ? 12 : 0)
  - (usedKinds.get(c.event.kind) ?? 0) * 5 - (c.target ? usedTargets.get(c.target) ?? 0 : 0) * 3 + clamp(c.event.probability);
 while (selected.length < eventLimit) {
  let best: Candidate | undefined, bestScore = -Infinity;
  for (const c of candidates) {
   if (rejected.has(c.event.id)) continue;
   const value = score(c);
   if (value > bestScore || value === bestScore && c.event.id < best!.event.id) {best = c; bestScore = value;}
  }
  if (!best) break;
  rejected.add(best.event.id);
  const nextLinks = new Set(links);
  if (best.target && (layout.arcs.some(a => a.key === best.target) || layout.arcs.length < arcLimit)) nextLinks.add(best.event.id);
  let next = eventGeometry([...selected, best], nextLinks, width, height, radius, packing);
  if (!clearGeometry(next, width, height, prepared.bands.length, packing)) {
   if (!nextLinks.delete(best.event.id)) continue;
   next = eventGeometry([...selected, best], nextLinks, width, height, radius, packing);
   if (!clearGeometry(next, width, height, prepared.bands.length, packing)) continue;
  }
  selected.push(best); layout = next; links = nextLinks;
  kinds.add(best.event.kind); groups.add(best.event.group); phases.add(best.phase); sessions.add(best.session);
  usedKinds.set(best.event.kind, (usedKinds.get(best.event.kind) ?? 0) + 1);
  if (best.target) {targets.add(best.target); usedTargets.set(best.target, (usedTargets.get(best.target) ?? 0) + 1);}
 }
 const toolRadius = baseRadius * .82 * 1.1, toolLimit = selected.length >= 4 ? 4 : 8;
 const tools: (ToolCandidate & {drawX: number; drawEndX: number; y: number})[] = [], skippedTools = new Set<string>(), categories = new Set<string>(), toolPhases = new Set<number>();
 const eventMarks = layout.markers.map(m => ({...m, r: extent(m.r, packing.scale)}));
 const longestTool = prepared.tools.reduce((longest, t) => Math.max(longest, t.durationMs ?? 0), 1);
 const dotOf = (t: {drawX: number; y: number}): CompactMark => ({x: t.drawX, x1: t.drawX, y: t.y, r: toolRadius + .5});
 const tailEnd = (t: ToolCandidate) => t.endX === null ? t.x * width : Math.min(width - toolTailRadius, t.endX * width);
 while (tools.length < toolLimit) {
  let best: ToolCandidate | undefined, bestScore = -Infinity;
  for (const c of prepared.tools) {
   if (skippedTools.has(c.tool.id)) continue;
   const value = (c.error ? 1000 : 0) + 90 * Math.log1p(c.durationMs ?? 0) / Math.log1p(longestTool)
    + (!categories.has(c.tool.category) ? 30 : 0) + (!toolPhases.has(c.phase) ? 22 : 0);
   if (value > bestScore || value === bestScore && c.tool.id < best!.tool.id) {best = c; bestScore = value;}
  }
  if (!best) break;
  skippedTools.add(best.tool.id);
  const drawX = best.x * width, occupied = [...eventMarks, ...tools.map(dotOf)];
  const lanes = laneCandidates(height, toolRadius + .5, packing.gap).filter(y => clearToolGeometry(dotOf({drawX, y}), occupied, layout, width, height, prepared.bands.length, packing));
  // Prefer a lane that can carry the full recorded duration. A blocked tail
  // never prevents the dot itself from representing an important call.
  const end = tailEnd(best), y = lanes.find(y => end - drawX > toolRadius + 2 && clearToolGeometry({x: drawX, x1: end, y, r: toolTailRadius}, occupied, layout, width, height, prepared.bands.length, packing, [dotOf({drawX, y})])) ?? lanes[0];
  if (y === undefined) continue;
  tools.push({...best, drawX, drawEndX: drawX, y}); categories.add(best.tool.category); toolPhases.add(best.phase);
 }
 const tails: CompactMark[] = [];
 for (const tool of tools) {
  const end = tailEnd(tool);
  if (end - tool.drawX <= toolRadius + 2) continue;
  const others = [...eventMarks, ...tools.filter(t => t !== tool).map(dotOf), ...tails];
  const y = [...new Set([tool.y, ...laneCandidates(height, toolRadius + .5, packing.gap)])].find(y =>
   clearToolGeometry(dotOf({drawX: tool.drawX, y}), others, layout, width, height, prepared.bands.length, packing)
   && clearToolGeometry({x: tool.drawX, x1: end, y, r: toolTailRadius}, others, layout, width, height, prepared.bands.length, packing, [dotOf({drawX: tool.drawX, y})]));
  if (y === undefined) continue;
  tool.y = y; tool.drawEndX = end; tails.push({x: tool.drawX, x1: end, y, r: toolTailRadius});
 }
 return {prepared, profile, width, height, bandY, bandHeight, radius, layout, selected, tools, toolRadius, toolTailRadius, packing, eventLimit,
  diversity: {kinds: kinds.size, groups: groups.size, targets: targets.size, phases: phases.size, sessions: sessions.size, toolCategories: categories.size}};
}

const escapeXML = (s: string) => s.replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&apos;'}[c]!));
const n = (value: number) => Number(value.toFixed(2));
const eventPalettes = {light: ['#b0102c', '#806400', '#2fa24f', '#1849b8'], dark: ['#ff7070', '#d2b658', '#5edc82', '#9fb6ff']};
export const compactToolColors: Record<string, string> = {read: '#6d8a93', write: '#a361c4', edit: '#a361c4', run: '#d2853d', execute: '#d2853d', git: '#348b8a', network: '#638bd5', delegate: '#9e78c3', wait: '#93918d', poll: '#93918d', other: '#80858b'};

/** Self-contained image: no external CSS, JavaScript, font, or document state.
 * The same drawing is served as SVG or rasterized at any requested width. */
export function compactFingerprintSVG(result: CompactFingerprintLayout, options: {pixelWidth?: number; theme?: 'light' | 'dark'} = {}) {
 const {prepared, width, height, bandY, bandHeight, layout, selected, tools, toolRadius, toolTailRadius, packing} = result;
 const pixelWidth = options.pixelWidth ?? width, theme = options.theme ?? 'light';
 if (!Number.isFinite(pixelWidth) || pixelWidth <= 0 || pixelWidth > 8192) throw new Error('Invalid image width');
 const ink = theme === 'dark' ? '#e9e2d8' : '#332e29', muted = theme === 'dark' ? '#696a6a' : '#c4c3bf';
 const color = (id: string) => {const value = stateColor(id); return value === 'transparent' ? value : value.startsWith('#') ? value : muted;};
 const segments = prepared.bands.map((band, i) => ({...band, x: band.x * width, width: band.width * width, i}));
 const bands = segments.map(s => {let y = bandY; return prepared.breakdowns[s.i].slices.map(slice => {const top = y, h = bandHeight * slice.share; y += h; return `<rect x="${n(s.x)}" y="${n(top)}" width="${n(s.width + .05)}" height="${n(h)}" fill="${color(slice.colorID)}"/>`;}).join('');}).join('');
 const envelopes = parallelEnvelopes(prepared.branches.map(b => ({...b, x: b.x * width, width: b.width * width}))).map(e => `<path d="${e.path}" transform="translate(0 ${bandY}) scale(1 ${bandHeight / 38})"/>`).join('');
 // Transform the envelope's clip, not the hatch fill: stripe pitch and angle
 // stay consistent in both aspect ratios instead of stretching vertically.
 const hatch = envelopes ? `<rect width="${width}" height="${height}" fill="url(#branch)" clip-path="url(#branch-envelope)"/>` : '';
 const arcs = layout.arcs.map(a => {const anchorY = compactArcY(a, layout.axis, a.x0);return `<path d="${a.path}" fill="none" stroke="${ink}" stroke-width="${n(.75 * packing.scale)}" opacity=".72"${a.weak ? ' stroke-dasharray="2 2"' : ''}/><path d="M${n(a.x0)} ${n(anchorY - 2 * packing.scale)}v${n(4 * packing.scale)}" stroke="${ink}" stroke-width="${n(.9 * packing.scale)}"/>`;}).join('');
 const byID = new Map(selected.map(c => [c.event.id, c]));
 const markers = layout.markers.map(m => {const c = byID.get(m.id)!, group = eventGroup(c.event.group), i = ['friction', 'learning', 'second-look', 'choices'].indexOf(group.key), fill = eventPalettes[theme][i]; return `<path d="${glyphPath(group.shape, m.x, m.y, m.r)}" fill="${c.target ? fill : 'none'}" stroke="${c.target ? 'none' : fill}" stroke-width="${n(.9 * packing.scale)}"/>`;}).join('');
 const tails = tools.filter(t => t.drawEndX > t.drawX).map(t => `<path d="M${n(t.drawX)} ${n(t.y)}H${n(t.drawEndX)}" fill="none" stroke="${t.error ? eventPalettes[theme][0] : compactToolColors[t.tool.category] ?? compactToolColors.other}" stroke-width="${n(toolTailRadius * 2)}" stroke-linecap="round" opacity=".8"/>`).join('');
 const dots = tools.map(c => `<circle cx="${n(c.drawX)}" cy="${n(c.y)}" r="${n(toolRadius)}" fill="${compactToolColors[c.tool.category] ?? compactToolColors.other}"${c.error ? ` stroke="${eventPalettes[theme][0]}" stroke-width="1"` : ''}/>`).join('');
 const totals = prepared.totals, summary = `${totals.messages} messages, ${totals.branches} branches; showing ${selected.length} of ${totals.events} events across ${result.diversity.kinds} kinds and ${result.diversity.phases} phases, ${layout.arcs.length} relationships; ${tools.length} representative calls of ${totals.tools} tools, ${tools.filter(t => t.drawEndX > t.drawX).length} duration tails`;
 return `<svg xmlns="http://www.w3.org/2000/svg" width="${n(pixelWidth)}" height="${n(pixelWidth * height / width)}" viewBox="0 0 ${width} ${height}" role="img"><title>${escapeXML(summary)}</title><defs><pattern id="branch" width="4" height="4" patternUnits="userSpaceOnUse" patternTransform="rotate(40)"><path d="M0 0V4" stroke="${ink}" stroke-opacity=".42" stroke-width="1"/></pattern><clipPath id="branch-envelope">${envelopes}</clipPath></defs><rect width="${width}" height="${height}" fill="${muted}" opacity=".12"/>${bands}${hatch}${arcs}${tails}${markers}${dots}</svg>`;
}
