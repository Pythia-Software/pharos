// Embedded by the Go service. Keep selection, geometry and colors on the same
// implementation reviewed in the browser; no browser or Node runtime is needed.
import {compactFingerprintProfiles, compactFingerprintSVG, layoutCompactFingerprint, prepareCompactFingerprint, type CompactFingerprintInput, type CompactFingerprintProfile} from './compact-fingerprint';

// ES library methods used by the shared model, for the embedded VM.
if (!Array.prototype.at) Object.defineProperty(Array.prototype, 'at', {value: function(index: number) {const i = Math.trunc(index); return this[i < 0 ? this.length + i : i];}});
if (!Array.prototype.flatMap) Object.defineProperty(Array.prototype, 'flatMap', {value: function(fn: (v: unknown, i: number) => unknown[]) {const out: unknown[] = []; this.forEach((v: unknown, i: number) => {const a = fn(v, i); if (Array.isArray(a)) a.forEach(x => out.push(x)); else out.push(a);}); return out;}});

export function render(inputJSON: string, eventsMask = 1): string {
 const input = JSON.parse(inputJSON) as CompactFingerprintInput, prepared = prepareCompactFingerprint(input);
 const images: {profile: string; theme: string; events: boolean; svg: string}[] = [];
 for (const showEvents of [true, false]) {
  if (!(eventsMask & (showEvents ? 1 : 2))) continue;
  for (const profile of Object.keys(compactFingerprintProfiles) as CompactFingerprintProfile[]) {
   const layout = layoutCompactFingerprint(prepared, profile, {showEvents});
   for (const theme of ['light', 'dark'] as const) images.push({profile, theme, events: showEvents, svg: compactFingerprintSVG(layout, {pixelWidth: 1024, theme})});
  }
 }
 return JSON.stringify({images, messages: prepared.totals.messages, events: prepared.totals.events, tools: prepared.totals.tools, branches: prepared.totals.branches});
}
