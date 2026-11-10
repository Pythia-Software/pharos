// Development only: the service does not load JS or Node at runtime.
import {build} from 'esbuild';
import {createHash} from 'node:crypto';
import {readFileSync, writeFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {fingerprintCases} from './tests/fixtures/compact-fingerprints.mjs';
const file = new URL('../testdata/fingerprint-rendering.json',import.meta.url);
const previous = JSON.parse(readFileSync(file,'utf8'));
const bundled = await build({stdin:{contents:"export {prepareCompactFingerprint,layoutCompactFingerprint,compactFingerprintSVG} from './src/compact-fingerprint';",resolveDir:fileURLToPath(new URL('.',import.meta.url))},bundle:true,platform:'node',format:'esm',write:false});
const model = await import('data:text/javascript;base64,'+Buffer.from(bundled.outputFiles[0].contents).toString('base64'));
const cases = fingerprintCases().map(sample=>{
 const prepared = model.prepareCompactFingerprint(sample.input), images = [];
 for (const events of [true,false]) for (const profile of ['wide','card']) {
  const layout = model.layoutCompactFingerprint(prepared,profile,{showEvents:events});
  for (const theme of ['light','dark']) {
   const svg = model.compactFingerprintSVG(layout,{pixelWidth:1024,theme}), svg_sha256 = createHash('sha256').update(svg).digest('hex');
   const old = previous.find(c=>c.name===sample.name)?.images.find(i=>i.profile===profile&&i.theme===theme&&i.events===events&&i.svg_sha256===svg_sha256);
   images.push({profile,theme,events,svg_sha256,png_sha256:old?.png_sha256??''});
  }
 }
 return {...sample,images};
});
if (process.argv.includes('--check')) {
 if (JSON.stringify(cases)!==JSON.stringify(previous)) throw new Error('Fingerprint fixtures differ from the TypeScript reference; run generate-fingerprint-fixtures and the Go parity update.');
} else {
 writeFileSync(file,JSON.stringify(cases)+'\n');
 console.log(`${cases.length} synthetic inputs; SVG hashes updated. Complete PNG hashes with PHAROS_UPDATE_FINGERPRINTS=1 go test -run '^TestFingerprintRendererParity$' ./internal/archive, then review the fixture diff.`);
}
