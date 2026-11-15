'use strict';
// DOM-free prototype contract. The processing owner publishes snapshots; results
// register the revision vector they actually rendered. UI never invents job state.
globalThis.ReadinessPrototype = (() => {
  const NOW = Date.parse('2026-11-07T17:24:00Z');
  const MINUTE = 60_000;
  const definitions = [
    { id: 'index', label: 'Read new conversations', domain: 'conversations', needs: [], unit: 'files', total: 8, schedule: 'Every 5 minutes', pool: 'cpu', explanation: 'Reads new and changed conversations. Saving the original files is a separate step.' },
    { id: 'tools', label: 'Update tools & usage', domain: 'tools', needs: ['index'], unit: 'calls', total: 319, schedule: 'Regularly, as needed', pool: 'cpu', explanation: 'Updates tool-use totals for workspaces with new activity.' },
    { id: 'writing', label: 'Update writing estimates', domain: 'writing', needs: ['index'], unit: 'messages', total: 128, schedule: 'Frequently', pool: 'cpu', explanation: 'Checks new and changed messages to estimate how much you wrote. Older estimates are refined overnight.' },
    { id: 'labels', label: 'Label messages', domain: 'labels', needs: ['writing'], unit: 'messages', total: 432, schedule: 'Hourly · after 10 quiet minutes', pool: 'gpu', explanation: 'Labels messages after the writing estimates finish. This example waits until the graphics processor has room for another update.' },
    { id: 'facts', label: 'Prepare labeled results', domain: 'facts', needs: ['labels'], unit: 'conversations', total: null, schedule: 'After labels change', pool: 'cpu', explanation: 'Prepares labels for charts and tables. The total is counted when this step starts.' },
  ];
  const datasets = {
    writing_messages: { label: 'Writing estimates', domains: ['conversations', 'writing'] },
    tool_calls: { label: 'Tool calls', domains: ['conversations', 'tools'] },
    people_message_labels: { label: 'Message labels', domains: ['conversations', 'writing', 'labels', 'facts'] },
    conversations: { label: 'Conversations', domains: ['conversations'] },
  };
  const age = ms => ms < MINUTE ? 'less than a minute' : ms < 60 * MINUTE ? `${Math.floor(ms / MINUTE)} minute${ms<2*MINUTE?'':'s'}` : ms < 1440 * MINUTE ? `${Math.floor(ms / (60 * MINUTE))} hour${ms<120*MINUTE?'':'s'}` : `${Math.floor(ms / (1440 * MINUTE))} day${ms<2880*MINUTE?'':'s'}`;
  const shortAge = ms => ms < 60 * MINUTE ? `${Math.max(1, Math.floor(ms / MINUTE))}m` : ms < 1440 * MINUTE ? `${Math.floor(ms / (60 * MINUTE))}h` : `${Math.floor(ms / (1440 * MINUTE))}d`;
  const clone = value => structuredClone(value);

  class Store {
    constructor(scenario = 'minutes', missing = false) { this.reset(scenario, missing); }
    reset(scenario, missing = false) {
      this.scenario = scenario;
      this.results = new Map();
      this.operations = [];
      this.rejectNext = scenario === 'unavailable';
      this.sequence = 1;
      this.requested = false;
      this.totals = { index: 8, tools: 319, writing: missing ? 1332 : 128, labels: 432, facts: null };
      const lag = ({ minutes: 3, hours: 187, days: 2 * 1440 + 83, paused: 187, busy: 43, cpu: 43, gpu: 43, running: 43, failed: 187, 'rendered-old': 43, unavailable: 43 })[scenario] ?? 0;
      this.snapshot = { now: NOW, checkedAt: NOW - 2 * MINUTE, nextCheckAt: NOW + 3 * MINUTE, available: scenario !== 'unknown', mode: scenario === 'paused' ? 'paused' : 'on', quiet: scenario !== 'busy', connected: true,
        pools: { cpu: { used: scenario === 'cpu' ? 2 : 0, external: scenario === 'cpu' ? 2 : 0, limit: 2, holder: scenario === 'cpu' ? 'Tools & usage in another library' : 'No updates running' }, gpu: { used: scenario === 'gpu' ? 1 : 0, external: scenario === 'gpu' ? 1 : 0, limit: 1, holder: scenario === 'gpu' ? 'Agent Experience verifier' : 'No updates running' } }, domains: {} };
      for (const def of definitions) {
        const behind = lag > 0 && (def.id !== 'index' || ['hours', 'days'].includes(scenario)) && !(def.id === 'tools' && !['hours', 'days'].includes(scenario));
        this.snapshot.domains[def.domain] = { input: behind ? 2 : 1, published: 1, firstKnownChangeAt: behind ? NOW - lag * MINUTE : null, changes: behind ? [{revision:2,at:NOW-lag*MINUTE}] : [], publishedAt: NOW - (behind ? lag + 2 : 2) * MINUTE, evidence: 'verified', pending: behind ? this.totals[def.id] : 0 };
      }
      // Unrelated work must never color a page whose results do not depend on it.
      this.snapshot.domains.backup = { input: 2, published: 1, firstKnownChangeAt: NOW - 4 * 1440 * MINUTE, publishedAt: NOW - 5 * 1440 * MINUTE, evidence: 'verified', pending: 1 };
      if (scenario === 'clear' || scenario === 'unknown') for (const domain of Object.values(this.snapshot.domains)) { if (domain === this.snapshot.domains.backup) continue; domain.published = domain.input; domain.firstKnownChangeAt = null; }
      if (scenario === 'rendered-old') for (const domain of Object.values(this.snapshot.domains)) { if (domain === this.snapshot.domains.backup) continue; domain.published = domain.input; domain.firstKnownChangeAt = null; domain.publishedAt = NOW - MINUTE; }
      if (missing) for (const id of ['writing', 'labels', 'facts']) { const d = this.snapshot.domains[id]; d.published = null; d.publishedAt = null; d.input = 2; d.pending = definitions.find(def => def.domain === id).total; d.firstKnownChangeAt = null; }
      if (scenario === 'failed') this.failed = 'writing'; else this.failed = null;
      if (scenario === 'running') { this.queue(['writing_messages']); this.advance(); this.advance(); }
      return this;
    }
    receipt(dataset, extra = [], older = false) {
      const ids = [...new Set([...(datasets[dataset]?.domains ?? []), ...extra.flatMap(id => datasets[id]?.domains ?? [])])];
      return Object.fromEntries(ids.map(id => [id, older && this.snapshot.domains[id].published !== null ? this.snapshot.domains[id].published - 1 : this.snapshot.domains[id].published]));
    }
    // Replacement of the full mounted set is atomic, so unmounting removes stale
    // registrations and a page transition cannot mix two scopes in the header.
    register(results) { this.results = new Map(results.map(result => [result.id, clone(result)])); }
    selection(resultId = null) { return resultId ? [...this.results.values()].filter(result => result.id === resultId) : [...this.results.values()]; }
    domainsFor(results) { return [...new Set(results.flatMap(result => Object.keys(result.consumed)))]; }
    status(resultId = null) {
      const results = this.selection(resultId), ids = this.domainsFor(results), snap = this.snapshot;
      if (!snap.available || !results.length || !ids.length || ids.some(id => !snap.domains[id] || snap.domains[id].evidence !== 'verified')) return { kind: 'unknown', tone: 'unknown', label: 'Update status unavailable', sentence: 'Pharos can’t tell whether the data shown here is up to date.', ids, results, lag: null };
      const missing = results.some(result => Object.values(result.consumed).some(revision => revision === null));
      const old = results.some(result => Object.entries(result.consumed).some(([id, revision]) => revision !== snap.domains[id].published));
      const changed = ids.filter(id => snap.domains[id].input !== snap.domains[id].published);
      const times = changed.map(id => snap.domains[id].firstKnownChangeAt).filter(value => value !== null);
      const lag = times.length ? Math.max(...times.map(at => snap.now - at)) : 0;
      const busy = this.operations.some(operation => !['done'].includes(operation.state) && ids.includes(definitions.find(def => def.id === operation.id).domain));
      const stages=definitions.filter(def=>ids.includes(def.domain)).map(def=>({def,...this.stage(def)}));
      const condition=['failed','paused','manual','blocked','waiting'].map(state=>stages.find(stage=>stage.state===state)).find(Boolean);
      if (missing) return { kind: 'missing', tone: 'hours', label: 'No results yet', sentence: 'Pharos needs to finish these steps before it can show results.', ids, results, lag: null, busy, condition:condition?.state, reason:condition?.reason };
      if (changed.length) return { kind: 'behind', tone: lag >= 1440 * MINUTE ? 'days' : lag >= 60 * MINUTE ? 'hours' : 'minutes', label: age(lag) + ' behind', sentence: 'Some changes are not included in these results yet.', ids, results, lag, busy, condition:condition?.state, reason:condition?.reason };
      if (old) return { kind: 'old-result', tone: 'hours', label: 'Showing saved results', sentence: 'Refresh this view to load the newer results.', ids, results, lag: null };
      return { kind: 'clear', tone: 'clear', label: `Up to date as of ${age(snap.now - snap.checkedAt)} ago`, sentence: 'Activity since then may not appear here yet.', ids, results, lag: null, busy, condition:condition?.state, reason:condition?.reason };
    }
    required(resultId = null) {
      const ids = this.status(resultId).ids, required = new Set();
      const visit = def => { if (required.has(def.id)) return; required.add(def.id); for (const id of def.needs) visit(definitions.find(item => item.id === id)); };
      for (const def of definitions) if (ids.includes(def.domain)) visit(def);
      return definitions.filter(def => required.has(def.id));
    }
    queue(datasetIds, resultId = null) {
      if (!this.snapshot.available) return { error: 'Update status is unavailable. Reconnect the library to request an update.' };
      if (this.rejectNext) { this.rejectNext = false; return { error: 'The update request didn’t go through. Try again.' }; }
      const selected = datasetIds ? definitions.filter(def => [...new Set(datasetIds.flatMap(id => datasets[id].domains))].includes(def.domain)) : this.required(resultId);
      const need = new Set();
      const visit = def => { for (const id of def.needs) visit(definitions.find(item => item.id === id)); const domain = this.snapshot.domains[def.domain]; if (domain.published !== domain.input) need.add(def.id); };
      selected.forEach(visit);
      let added = 0;
      for (const def of definitions) if (need.has(def.id) && !this.operations.some(op => op.id === def.id && op.state !== 'done')) {
        this.operations.push({ id: def.id, state: 'queued', done: this.failed === def.id ? 58 : 0, total: this.totals[def.id], target: this.snapshot.domains[def.domain].input }); added++;
      }
      this.requested = true;
      this.failed = null;
      this.sequence++;
      return { added };
    }
    checkSources() {
      if (!this.snapshot.available) return { error: 'Update progress is unavailable.' };
      if (this.operations.some(op => op.id === 'index' && op.state !== 'done')) return { added: 0 };
      this.operations.push({ id: 'index', state: 'queued', done: 0, total: null, target: this.snapshot.domains.conversations.input });
      this.requested = true; this.sequence++; return { added: 1 };
    }
    stage(def) {
      const domain = this.snapshot.domains[def.domain], operation = this.operations.find(op => op.id === def.id && op.state !== 'done');
      const total = this.totals[def.id];
      if (!this.snapshot.available) return { state: 'unknown', done: null, total: null, reason: 'Update progress is unavailable.' };
      if (this.failed === def.id) return { state: 'failed', done: 58, total, reason: 'The last update stopped. Finished work has been saved.' };
      if (operation) {
        const waiting = def.needs.find(id => { const d = this.snapshot.domains[definitions.find(item => item.id === id).domain]; return d.input !== d.published; });
        if (waiting) return { ...operation, state: 'waiting', reason: `Waiting for “${definitions.find(item => item.id === waiting).label}” to finish.` };
        const pool = this.snapshot.pools[def.pool];
        if (operation.state !== 'running' && pool.used >= pool.limit) return { ...operation, state: 'blocked', reason: `Waiting for ${pool.holder} to finish using the ${def.pool==='cpu'?'processor':'graphics processor'}.` };
        return { ...operation, reason: operation.state === 'running' ? 'Running on this Mac.' : 'Waiting for its turn. This request won’t change your update schedule.' };
      }
      if (domain.published === domain.input) return { state: 'complete', done: total, total, reason: 'Includes changes found at the last check.' };
      if (this.snapshot.mode === 'paused') return { state: 'paused', done: 0, total, reason: 'Automatic updates are paused. You can still request an update in settings.' };
      if (this.snapshot.mode === 'manual') return { state: 'manual', done: 0, total, reason: 'Runs only when requested.' };
      const waiting=def.needs.find(id=>{const input=this.snapshot.domains[definitions.find(item=>item.id===id).domain];return input.input!==input.published;});
      if(waiting)return {state:'waiting',done:0,total,reason:`Waiting for “${definitions.find(item=>item.id===waiting).label}” to finish.`};
      if (!this.snapshot.quiet && def.id === 'labels') return { state: 'waiting', done: 0, total, reason: 'No new activity for 6 minutes. Can start after 4 more quiet minutes.' };
      const pool=this.snapshot.pools[def.pool];
      if(pool.used>=pool.limit)return {state:'blocked',done:0,total,reason:`Waiting for ${pool.holder} to finish using the ${def.pool==='cpu'?'processor':'graphics processor'}.`};
      return { state: 'pending', done: 0, total, reason: def.schedule + (def.id === 'labels' ? ' · next check planned for 11:00 AM.' : ' · next check at 10:27 AM.') };
    }
    advance() {
      const running = this.operations.find(op => op.state === 'running');
      if (running) {
        if (running.done === 0) { running.done = running.total === null ? 17 : Math.floor(running.total * .46); this.sequence++; return 'progress'; }
        const domain = this.snapshot.domains[definitions.find(def => def.id === running.id).domain];
        domain.published = running.target; domain.publishedAt = this.snapshot.now;
        if (running.id === 'index') { this.snapshot.checkedAt = this.snapshot.now; this.snapshot.nextCheckAt = this.snapshot.now + 5 * MINUTE; }
        domain.changes = domain.changes.filter(change => change.revision > running.target);
        domain.firstKnownChangeAt = domain.changes.length ? Math.min(...domain.changes.map(change => change.at)) : null;
        if (domain.input === running.target) domain.pending = 0;
        const pool = this.snapshot.pools[definitions.find(def => def.id === running.id).pool];
        pool.used = Math.max(0, pool.used - 1);
        if (pool.used === 0) pool.holder = 'No updates running';
        running.state = 'done'; this.sequence++; return 'completed';
      }
      for (const def of definitions) {
        const op = this.operations.find(item => item.id === def.id && item.state === 'queued');
        if (op && this.stage(def).state === 'queued') { op.state = 'running'; const pool = this.snapshot.pools[def.pool]; pool.used++; if (!pool.external) pool.holder = def.label; this.sequence++; return 'started'; }
      }
      return 'blocked';
    }
    arrive() { for (const id of ['writing','labels','facts']) { const d = this.snapshot.domains[id]; d.input++; d.changes.push({revision:d.input,at:this.snapshot.now}); d.firstKnownChangeAt ??= this.snapshot.now; d.pending = (d.pending ?? 0) + 12; } this.sequence++; }
    release() { for (const pool of Object.values(this.snapshot.pools)) { pool.used -= pool.external; pool.external = 0; if (!pool.used) pool.holder = 'No updates running'; } this.sequence++; }
  }

  return { Store, definitions, datasets, age, shortAge, MINUTE };
})();
