import assert from 'node:assert/strict';
import {test} from 'node:test';
import {setImmediate} from 'node:timers/promises';
import {fingerprintImageCache} from '../../../src/fingerprint-image-model';

const request = (id, extra = {}) => ({entity: 'conversation', id: String(id), profile: 'wide', events: true, ...extra});
const image = (r, state = 'ready', suffix = '') => ({entity: r.entity, id: r.id, profile: r.profile, state, images: {light: '/image/' + r.id + suffix}});
const deferred = () => {let resolve; const promise = new Promise(done => {resolve = done;}); return {promise, resolve};};

test('a fingerprint column batches 501 distinct rows, coalesces duplicate cells, and separates event variants', async t => {
 const calls = [], cache = fingerprintImageCache(async (items, events) => {calls.push({items, events}); return items.map(r => image(r));});
 t.after(() => cache.dispose());
 for (let i = 0; i < 501; i++) cache.subscribe(request(i), () => {});
 cache.subscribe(request(0), () => {});
 cache.subscribe(request(0, {events: false}), () => {});
 cache.subscribe(request(0, {entity: 'workspace', profile: 'card'}), () => {});
 await setImmediate();
 assert.deepEqual(calls.map(c => [c.items.length, c.events]), [[250, true], [250, true], [2, true], [1, false]]);
 assert.equal(cache.snapshot(request(500)).image.state, 'ready');
});

test('pending and failed fingerprints retry as one batch and unmounted cells stop polling', async t => {
 t.mock.timers.enable({apis: ['setTimeout', 'Date']});
 const calls = []; let fail = false, state = 'pending';
 const cache = fingerprintImageCache(async items => {calls.push(items.map(r => r.id)); if (fail) throw new Error('offline'); return items.map(r => image(r, state));});
 t.after(() => cache.dispose());
 const offA = cache.subscribe(request('a'), () => {}), offB = cache.subscribe(request('b'), () => {});
 await setImmediate();
 assert.equal(calls.length, 1);
 offB(); fail = true;
 t.mock.timers.tick(2000); await setImmediate();
 assert.deepEqual(calls[1], ['a']);
 assert.ok(cache.snapshot(request('a')).error);
 assert.equal(cache.snapshot(request('a')).image.state, 'pending', 'keep the last usable image through transport errors');
 fail = false; state = 'ready';
 t.mock.timers.tick(5000); await setImmediate();
 assert.equal(cache.snapshot(request('a')).error, undefined);
 assert.equal(cache.snapshot(request('a')).image.state, 'ready');
 offA(); t.mock.timers.tick(60000); await setImmediate();
 assert.equal(calls.length, 3);
});

test('refresh ignores an in-flight old reply and reads the new generation before publishing', async t => {
 const old = deferred(), fresh = deferred(), calls = [];
 const cache = fingerprintImageCache(items => {calls.push(items); return calls.length === 1 ? old.promise : fresh.promise;});
 t.after(() => cache.dispose());
 cache.subscribe(request('a'), () => {}); await setImmediate();
 cache.invalidate(); old.resolve([image(request('a'), 'ready', '-old')]); await setImmediate();
 assert.equal(calls.length, 2);
 assert.equal(cache.snapshot(request('a')).image, undefined);
 fresh.resolve([image(request('a'), 'ready', '-new')]); await setImmediate();
 assert.equal(cache.snapshot(request('a')).image.images.light, '/image/a-new');
});

test('missing responses and image failures retry without retaining an unusable URL', async t => {
 t.mock.timers.enable({apis: ['setTimeout', 'Date']});
 let omit = true;
 const cache = fingerprintImageCache(async items => omit ? [] : items.map(r => image(r)));
 t.after(() => cache.dispose());
 cache.subscribe(request('a'), () => {}); await setImmediate();
 assert.ok(cache.snapshot(request('a')).error);
 omit = false; t.mock.timers.tick(5000); await setImmediate();
 assert.ok(cache.snapshot(request('a')).image);
 cache.failed(request('a'));
 assert.equal(cache.snapshot(request('a')).image, undefined);
 t.mock.timers.tick(5000); await setImmediate();
 assert.ok(cache.snapshot(request('a')).image);
});
