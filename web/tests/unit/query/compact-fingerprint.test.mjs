import assert from 'node:assert/strict';
import {test} from 'node:test';
import {compactArcY as arcY, compactFingerprintSVG, layoutCompactFingerprint, prepareCompactFingerprint} from '../../../src/compact-fingerprint';
import {markGap} from '../../../src/ribbon-marks';

const message = (i, extra = {}) => ({id: 'm' + i, role: i % 3 ? 'assistant' : 'user', kind: 'message', at: new Date(1700000000000 + i * 10000).toISOString(), session_id: i % 2 ? 'child' : 'main', labels: [[i % 2 ? 'C.report.product' : 'P.request.work.implement', .9]], chunks: 1, text: '', text_source: 'Test', segment: '', truncated: false, ...extra});
const conversation = (n = 100) => ({id: 'conversation', work_id: 'work', main_session: 'main', title: 'Test', repository: '', provider: 'test', started_at: null, messages: Array.from({length: n}, (_, i) => message(i))});
const event = (i, at, extra = {}) => ({id: 'e' + i, message_id: 'm' + at, kind: 'obstacle', label: 'Obstacle', group: 'Friction', probability: .9, source: 'thinking', sentence: 'Test event', span: [i * 100, i * 100 + 60], target: {method: 'quote', strong: true, message_id: 'm' + Math.max(0, at - 12)}, ...extra});
const tool = (i, at, category = 'read', status = 'complete') => ({id: 't' + i, session_id: 'main', name: category, category, status, start: new Date(1700000000000 + at * 10000).toISOString(), end: null});

test('compact selection preserves kinds, phases and branches instead of the first repeated events', () => {
 const c = conversation(120), repeated = Array.from({length: 100}, (_, i) => event(i, 20 + i % 3));
 const rare = [event(200, 65, {kind: 'discovery', group: 'Reactions and learning'}), event(201, 100, {kind: 'pushback', group: 'Choices you may not see'})];
 const p = prepareCompactFingerprint({conversation: c, events: [...repeated, ...rare]});
 const layout = layoutCompactFingerprint(p, 'card');
 assert.ok(layout.selected.some(c => c.event.id === 'e200'));
 assert.ok(layout.selected.some(c => c.event.id === 'e201'));
 assert.ok(layout.diversity.kinds >= 3);
 assert.ok(layout.diversity.phases >= 3);
 assert.ok(layout.selected.length < repeated.length, 'dense repeated marks are actually filtered');
});

test('tool volume cannot displace equal-pitch message colors or create empty label columns', () => {
 const c = conversation(3);
 c.messages[2].at = new Date(1700000000000 + 86400000).toISOString();
 c.messages.forEach(m => {m.labels = [['C.report.product', .9]];});
 c.activity = [tool(0, .5), {...tool(1, 1), message_id: 'm1'}];
 const p = prepareCompactFingerprint({conversation: c, events: []});
 assert.equal(p.stream.length, 3, 'only message evidence sets the label axis');
 assert.deepEqual(['m0', 'm1', 'm2'].map(id => p.positions.get(id)), [1 / 6, .5, 5 / 6]);
 assert.ok(Math.abs(p.tools[0].x - 1 / 3) < 1e-12, 'a timed call interpolates between fixed message positions');
 assert.equal(p.tools[1].x, .5, 'an anchored call shares its message position');
 const baseline = prepareCompactFingerprint({conversation: {...c, activity: []}, events: []});
 c.activity.push(...Array.from({length: 80}, (_, i) => tool(i + 2, .6 + i / 100)));
 const crowded = prepareCompactFingerprint({conversation: c, events: []});
 assert.deepEqual(crowded.positions, baseline.positions);
 assert.deepEqual(crowded.bands, baseline.bands);
 assert.deepEqual(crowded.breakdowns, baseline.breakdowns);
 assert.ok(crowded.bands.every(b => b.messages.length > 0));
 assert.deepEqual(crowded.bands.flatMap(b => b.messages.map(m => m.id)), c.messages.map(m => m.id));
 assert.equal(layoutCompactFingerprint(p).bandY, 0);
});

test('explicit ceilings of 100 and 500 retain full-size markers and stop when the ribbon is full', () => {
 const c = conversation(1200), events = Array.from({length: 500}, (_, i) => event(i, 100 + i * 2, {target: null}));
 const p = prepareCompactFingerprint({conversation: c, events});
 for (const profile of ['wide', 'card']) for (const eventLimit of [100, 500]) {
  const baseline = layoutCompactFingerprint(p, profile, {eventLimit: 20, arcLimit: 0});
  const r = layoutCompactFingerprint(p, profile, {eventLimit, arcLimit: 0});
  assert.equal(r.eventLimit, eventLimit);
  assert.equal(r.radius, baseline.radius, 'the ceiling never changes marker size');
  assert.equal(r.packing.scale, 1, 'strokes and spacing keep their original scale');
  assert.equal(r.packing.gap, baseline.packing.gap);
  assert.equal(r.toolRadius, baseline.toolRadius);
  assert.ok(r.selected.length > 20, 'no old profile cap');
  assert.ok(r.selected.length < eventLimit, 'a full ribbon never shrinks markers to fill the budget');
  assert.equal(r.layout.markers.length, r.selected.length);
  assert.equal(new Set(r.selected.map(c => c.event.id)).size, r.selected.length);
  assert.ok(r.layout.markers.every(m => m.r === baseline.radius));
  assert.ok(compactFingerprintSVG(r).includes('stroke-width="0.9"'), 'event stroke thickness stays legible');
  for (const m of r.layout.markers) assert.equal(m.x, r.selected.find(c => c.event.id === m.id).x * r.width);
  const boxes = r.layout.markers.map(m => ({...m, r: m.r * 1.18 + .5 * r.packing.scale}));
  for (let i = 0; i < boxes.length; i++) for (const b of boxes.slice(i + 1)) assert.ok(markGap(boxes[i], b) >= r.packing.scale);
  const cell = r.width / p.bands.length;
  for (let i = 0; i < p.bands.length; i++) assert.ok(boxes.reduce((sum, m) => sum + Math.max(0, Math.min((i + 1) * cell, m.x + m.r) - Math.max(i * cell, m.x - m.r)) * m.r * 2, 0) <= cell * r.height * .28);
  const roomy = layoutCompactFingerprint(p, profile, {width: 4096, eventLimit, arcLimit: 0});
  assert.equal(roomy.selected.length, eventLimit, 'larger ceilings remain available when full-size markers fit');
 }
 assert.equal(layoutCompactFingerprint(p, 'wide', {eventLimit: 0}).selected.length, 0);
 assert.equal(layoutCompactFingerprint(p, 'wide', {eventLimit: Infinity}).eventLimit, 500);
 assert.equal(layoutCompactFingerprint(p, 'wide', {eventLimit: 99999}).eventLimit, 500);
 const linked = prepareCompactFingerprint({conversation: c, events: events.map(e => ({...e, target: {method: 'quote', strong: true, message_id: 'm0'}}))});
 const dense = layoutCompactFingerprint(linked, 'card', {eventLimit: 100, arcLimit: 1});
 assert.ok(dense.selected.length > 20, 'sampling relationship arcs never silently reinstates a small event cap');
 assert.ok(dense.layout.arcs.length <= 1);
 assert.ok(dense.selected.some(c => !dense.layout.markers.find(m => m.id === c.event.id).arc && c.target), 'known relationships remain represented when their arcs are omitted');
});

test('compact marks, curves and tools share the full ribbon without collisions at small and large widths', () => {
 const c = conversation(100);
 c.activity = Array.from({length: 60}, (_, i) => ({...tool(i, i * 1.7, ['read', 'write', 'run', 'network'][i % 4], i === 40 ? 'error' : 'complete'), duration_ms: i % 12 === 0 ? 180000 : 2000}));
 const events = Array.from({length: 100}, (_, i) => event(i, 15 + i % 85, {target: i % 4 ? {method: 'quote', strong: true, message_id: 'm' + (i % 14)} : null}));
 const p = prepareCompactFingerprint({conversation: c, events});
 for (const profile of ['wide', 'card']) for (const width of [180, 320, 512]) {
  const r = layoutCompactFingerprint(p, profile, {width});
  assert.equal(r.bandY, 0);
  assert.equal(r.bandHeight, r.height, 'the label ribbon retains its entire height');
  assert.equal(r.layout.axis, r.height / 2, 'events use the ribbon centerline');
  assert.equal(r.eventLimit, 500, 'dense full-size selection is the default in both aspect ratios');
  assert.ok(r.selected.length <= 500);
  assert.ok(r.layout.arcs.length <= (profile === 'wide' ? 4 : 6));
  for (const a of r.layout.arcs) {assert.ok(a.level <= 2); assert.ok([1, -1].includes(a.side));}
  for (let i = 0; i < r.layout.arcs.length; i++) for (const b of r.layout.arcs.slice(i + 1)) {
   const a = r.layout.arcs[i];
   assert.ok(Math.abs(a.x0 - b.x0) >= 2.5, 'earlier-target anchors remain distinct');
   for (let x = Math.max(a.x0, b.x0); x <= Math.min(a.x1, b.x1); x += .5) assert.ok(Math.abs(arcY(a, r.layout.axis, x) - arcY(b, r.layout.axis, x)) >= 1.5, 'curves neither cross nor obscure one another');
  }
  for (const m of r.layout.markers) {
   assert.ok(!m.overlap);
   assert.ok(m.y - m.r * 1.18 >= 0 && m.y + m.r * 1.18 <= r.height, 'event stays inside the label ribbon');
   assert.ok(m.x >= m.r * 1.18 && m.x <= width - m.r * 1.18);
   assert.equal(m.x, r.selected.find(c => c.event.id === m.id).x * width, 'collision handling never shifts ordinal position');
   for (const a of r.layout.arcs) if (a.key !== m.arc) {
    for (let x = Math.max(a.x0, m.x - m.r - 1); x <= Math.min(a.x1, m.x + m.r + 1); x += .5) assert.ok(Math.hypot(x - m.x, arcY(a, r.layout.axis, x) - m.y) >= m.r * 1.18 + .5);
   }
  }
  for (let i = 0; i < r.layout.markers.length; i++) for (const b of r.layout.markers.slice(i + 1)) assert.ok(markGap(r.layout.markers[i], b) >= 1);
  for (const t of r.tools) {
   assert.ok(t.y - r.toolRadius >= 0 && t.y + r.toolRadius <= r.height, 'tool shares the same ribbon');
   assert.equal(t.drawX, t.x * width);
   const dot = {x: t.drawX, x1: t.drawX, y: t.y, r: r.toolRadius + .5};
   for (const m of r.layout.markers) assert.ok(markGap(dot, {...m, r: m.r * 1.18 + .5}) >= 1.5);
   for (const a of r.layout.arcs) for (let x = Math.max(a.x0, dot.x - dot.r - 1); x <= Math.min(a.x1, dot.x + dot.r + 1); x += .4) assert.ok(Math.hypot(x - dot.x, arcY(a, r.layout.axis, x) - dot.y) >= dot.r + 1);
  }
  for (let i = 0; i < r.tools.length; i++) for (const b of r.tools.slice(i + 1)) assert.ok(Math.hypot(r.tools[i].drawX - b.drawX, r.tools[i].y - b.y) >= 2 * r.toolRadius + 2.5);
  const tails = r.tools.filter(t => t.drawEndX > t.drawX).map(t => ({x: t.drawX, x1: t.drawEndX, y: t.y, r: r.toolTailRadius, id: t.tool.id}));
  for (const tail of tails) {
   assert.ok(tail.x1 + tail.r <= width);
   for (const m of r.layout.markers) assert.ok(markGap(tail, {...m, r: m.r * 1.18 + .5}) >= r.packing.gap);
   for (const t of r.tools.filter(t => t.tool.id !== tail.id)) assert.ok(markGap(tail, {x: t.drawX, x1: t.drawX, y: t.y, r: r.toolRadius + .5}) >= r.packing.gap);
   for (const a of r.layout.arcs) for (let x = Math.max(a.x0, tail.x - tail.r - 1); x <= Math.min(a.x1, tail.x1 + tail.r + 1); x += .4) assert.ok(Math.hypot(Math.max(0, tail.x - x, x - tail.x1), arcY(a, r.layout.axis, x) - tail.y) >= tail.r + 1);
  }
  for (let i = 0; i < tails.length; i++) for (const t of tails.slice(i + 1)) assert.ok(markGap(tails[i], t) >= r.packing.gap);
  const boxes = [...r.layout.markers.map(m => ({x: m.x, x1: m.x, r: m.r * 1.18 + .5})), ...r.tools.map(t => ({x: t.drawX, x1: t.drawX, r: r.toolRadius + .5})), ...tails];
  for (let x = 0; x <= width; x += .25) assert.ok(boxes.filter(m => x > m.x - m.r && x < m.x1 + m.r).length <= r.packing.stacks, 'vertical stacks stay bounded at the dense ceiling');
  const cell = width / p.bands.length;
  for (let i = 0; i < p.bands.length; i++) {
   const area = boxes.reduce((sum, m) => sum + Math.max(0, Math.min((i + 1) * cell, m.x1 + m.r) - Math.max(i * cell, m.x - m.r)) * m.r * 2, 0);
   assert.ok(area <= cell * r.height * .28, 'symbols leave most of each message column visible');
  }
 }
});

test('strong earlier relationships win over unlinked observations and missing targets do not invent arcs', () => {
 const c = conversation();
 const events = [event(0, 20, {target: null}), event(1, 45), event(2, 70, {target: {method: 'quote', message_id: 'missing', strong: true}}), event(3, 10, {target: {method: 'quote', message_id: 'm90', strong: true}})];
 const p = prepareCompactFingerprint({conversation: c, events});
 assert.equal(layoutCompactFingerprint(p, 'wide', {eventLimit: 1}).selected[0].event.id, 'e1');
 for (const id of ['e2', 'e3']) assert.equal(p.candidates.find(c => c.event.id === id).target, null);
 assert.equal(layoutCompactFingerprint(p, 'wide', {arcLimit: 0}).layout.arcs.length, 0);
 const varied = prepareCompactFingerprint({conversation: conversation(400), events: Array.from({length: 8}, (_, i) => event(i, i * 45 + 25, {target: {method: 'quote', strong: true, message_id: 'm' + (i * 45 + 3)}}))});
 const r = layoutCompactFingerprint(varied, 'card', {width: 512});
 assert.ok(r.layout.arcs.length >= 4);
 assert.equal(new Set(r.layout.arcs.map(a => a.side)).size, 2, 'curves sweep both above and below');
 assert.equal(new Set(r.layout.arcs.map(a => a.sweep)).size, 2, 'both target and event ends can meet the centerline');
 assert.deepEqual(layoutCompactFingerprint(prepareCompactFingerprint({...varied.input, events: [...varied.input.events].reverse()}), 'card', {width: 512}).layout, r.layout, 'arc variants stay deterministic');
 for (const a of r.layout.arcs) assert.equal(a.x0, varied.positions.get(a.key) * r.width, 'target chronology stays intact');
 for (const m of r.layout.markers) {
  assert.equal(m.r, r.radius * 1.1, 'filled shapes receive only a small increase');
  if (m.arc) assert.equal(m.y, arcY(r.layout.arcs.find(a => a.key === m.arc), r.layout.axis, m.x));
 }
});

test('tool-only and event-free conversations retain tool category, phase and failure context', () => {
 const c = conversation(0);
 c.activity = Array.from({length: 40}, (_, i) => tool(i, i, ['read', 'write', 'run', 'network'][i % 4], i === 34 ? 'error' : 'complete'));
 const result = layoutCompactFingerprint(prepareCompactFingerprint({conversation: c, events: []}));
 assert.equal(result.prepared.totals.tools, 40);
 assert.ok(result.tools.length > 4 && result.tools.length <= 8);
 assert.equal(result.diversity.toolCategories, 4);
 assert.ok(result.tools.some(t => t.error));
 assert.ok(new Set(result.tools.map(t => t.phase)).size >= 5);
 assert.ok(compactFingerprintSVG(result).includes('0 messages'));
 const active = conversation(60);
 active.activity = Array.from({length: 12}, (_, i) => ({...tool(i, i * 4 + 1, 'read', [2, 6].includes(i) ? 'error' : 'complete'), duration_ms: i === 8 ? 180000 : 100}));
 const r = layoutCompactFingerprint(prepareCompactFingerprint({conversation: active, events: []}));
 assert.ok(r.tools.slice(0, 2).every(t => t.error), 'multiple failed calls win before successful calls');
 assert.ok(r.tools.some(t => t.tool.id === 't8'), 'a long-running call outranks redundant short successes');
 assert.ok(r.tools.some(t => t.tool.id === 't8' && t.drawEndX > t.drawX), 'a long duration is drawn when a clear lane exists');
 const unknown = prepareCompactFingerprint({conversation: {...active, activity: [{...tool(99, 1), start: null, message_id: 'm1', duration_ms: 600000}]}, events: []});
 assert.equal(unknown.tools[0].endX, null, 'a known duration without a start time never invents a tail');
});

test('full histories, tool-only tails and deterministic selection survive the compact projection', () => {
 const c = conversation(2501);
 c.messages.at(-1).labels = [['C.report.completion', .99]];
 c.activity = [tool(0, -10), tool(1, 2550), {...tool(2, 200), start: null}];
 const input = {conversation: c, events: [event(1, 100), event(2, 2500)]}, before = JSON.stringify(input);
 const p = prepareCompactFingerprint(input), result = layoutCompactFingerprint(p);
 assert.equal(p.totals.messages, 2501);
 assert.ok(p.positions.has('m2500'));
 assert.ok(p.breakdowns.some(b => b.slices.some(s => s.id === 'C.report.completion')));
 assert.ok(p.tools[0].x < p.positions.get('m0'));
 assert.ok(p.tools[1].x > p.positions.get('m2500'));
 assert.equal(p.totals.unpositionedTools, 1, 'an untimed call without a message anchor does not acquire an invented phase');
 assert.deepEqual(layoutCompactFingerprint(prepareCompactFingerprint({...input, events: [...input.events].reverse()})).selected.map(c => c.event.id), result.selected.map(c => c.event.id));
 assert.equal(JSON.stringify(input), before, 'input evidence is never changed');
});

test('the same self-contained SVG drawing supports themes and raster sizes, including empty inputs', () => {
 const p = prepareCompactFingerprint({conversation: conversation(), events: [event(1, 40)]}), r = layoutCompactFingerprint(p);
 const small = compactFingerprintSVG(r, {pixelWidth: 256}), large = compactFingerprintSVG(r, {pixelWidth: 1024}), dark = compactFingerprintSVG(r, {theme: 'dark'});
 assert.ok(small.includes('width="256" height="32"'));
 assert.ok(large.includes('width="1024" height="128"'));
 assert.equal(small.slice(small.indexOf('viewBox=')), large.slice(large.indexOf('viewBox=')));
 assert.notEqual(dark, compactFingerprintSVG(r));
 assert.ok(!/var\(|<script|<foreignObject|https?:\/\/(?!www\.w3\.org)/.test(large));
 assert.equal(layoutCompactFingerprint(p, 'wide', {showEvents: false}).selected.length, 0);
 assert.ok(compactFingerprintSVG(layoutCompactFingerprint(prepareCompactFingerprint({conversation: conversation(0), events: []}))).includes('0 representative calls'));
 assert.throws(() => layoutCompactFingerprint(p, 'wide', {width: 0}));
 assert.throws(() => compactFingerprintSVG(r, {pixelWidth: Infinity}));
});
