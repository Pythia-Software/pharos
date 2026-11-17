import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { assets, loadClassicScript, plain } from '../helpers/classic-script.mjs';
// What the guide writes for these names. Go checks the same two
// configurations against the command that reads them (bucket setup test).
import fixture from '../../../../testdata/bucket-setup.json';

const setup = loadClassicScript('bucket-setup.js').window.pharosBucketSetup;
const step = (made, id) => made.steps.find(one => one.id === id);
const file = (made, name) => step(made, 'repository').blocks.find(block => block.file === name).text;

test('the guide writes the configurations Go accepts', () => {
  const made = setup.guide(fixture.input);
  assert.deepEqual(plain(made.problems), []);
  assert.ok(made.steps.every(one => one.ready));
  assert.deepEqual(JSON.parse(file(made, '.niteshift/pharos-push.json')), fixture.push);
  assert.deepEqual(JSON.parse(setup.readConfig(made.bucket, fixture.prefix)), fixture.read);
});

test('every name reaches the commands, and the shell accepts them', () => {
  const made = setup.guide(fixture.input), commands = step(made, 'create').blocks[0].text;
  for (const name of ['project', 'bucket', 'organization']) assert.ok(commands.includes(fixture.input[name]), name);
  assert.ok(!/<[a-z-]+>/.test(commands + file(made, '.niteshift/pharos-push.json')), 'no placeholder is left');
  const directory = mkdtempSync(join(tmpdir(), 'pharos-bucket-setup-'));
  try {
    for (const [name, text] of [['create.sh', commands], ['push.sh', file(made, '.niteshift/pharos-push.sh')], ['check.sh', step(made, 'check').blocks[0].text]]) {
      writeFileSync(join(directory, name), text);
      const checked = spawnSync('sh', ['-n', join(directory, name)], {encoding: 'utf8'});
      assert.equal(checked.status, 0, `${name}: ${checked.stderr}`);
    }
  } finally {
    rmSync(directory, {recursive: true, force: true});
  }
});

// This repository pushes its own Niteshift tasks with the files the guide
// writes, so what a client is told to commit is what is run here.
test('the repository runs the script the guide writes', () => {
  const repository = join(assets, '..', '..', '..', '.niteshift');
  const made = setup.guide(fixture.input);
  assert.equal(readFileSync(join(repository, 'pharos-push.sh'), 'utf8'), file(made, '.niteshift/pharos-push.sh'));
  assert.equal(readFileSync(join(repository, 'services.yaml'), 'utf8').replace(/^#.*\n/gm, ''), file(made, '.niteshift/services.yaml').replace(/^#.*\n/gm, ''));
});

test('missing and unsafe names hold their steps back', () => {
  const empty = setup.guide({});
  assert.deepEqual(plain(empty.steps.map(one => one.ready)), [false, false, true]);
  assert.equal(empty.bucket, '');
  // The project number comes from step 1, so step 1 does not wait for it.
  const first = setup.guide({...fixture.input, number: ''});
  assert.deepEqual(plain(first.steps.map(one => one.ready)), [true, false, true]);
  // A value that would change the meaning of a pasted command is refused.
  const unsafe = setup.guide({...fixture.input, bucket: 'team"; rm -rf ~; "'});
  assert.equal(unsafe.problems.length, 1);
  assert.equal(step(unsafe, 'create').ready, false);
  assert.ok(!step(unsafe, 'create').blocks[0].text.includes('rm -rf'));
});

test('a connected bucket says what has come from it', () => {
  const bucket = {id: 'b', type: 'gcs', bucket: 'team-pharos', prefix: 'pharos/me', machines: 0};
  assert.equal(setup.bucketLine(bucket).name, 'Google Cloud Storage · team-pharos/pharos/me');
  assert.match(setup.bucketLine(bucket).status, /Nothing collected yet/);
  assert.match(setup.bucketLine({...bucket, collected_at: '2026-11-15T10:00:00Z'}).status, /No machine has pushed/);
  assert.match(setup.bucketLine({...bucket, machines: 1, collected_at: '2026-11-15T10:00:00Z'}).status, /from 1 machine\./);
  assert.match(setup.connectedLine({checked_in: 2}), /2 machines have checked in/);
  assert.match(setup.connectedLine({checked_in: 0}), /No machine has checked in/);
});
