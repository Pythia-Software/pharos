import { test } from 'node:test';
import assert from 'node:assert/strict';
import { authorshipCategories } from '../../../src/authorship-categories';
import { explainAuthorshipRule } from '../../../src/authored';

test('stored instruction-block rules keep their specific explanation', () => {
  const generic = authorshipCategories.find(category => category.key === 'harness').description;
  const instruction = explainAuthorshipRule('Conductor system instruction', 'harness');
  assert.notEqual(instruction, generic);
  for (const rule of ['Harness context', 'System reminder', 'Environment context', 'Project instructions', 'Plugin recommendations']) {
    assert.equal(explainAuthorshipRule(rule, 'harness'), instruction, rule);
  }
  assert.equal(explainAuthorshipRule('Unrecognized rule', 'harness'), generic);
});
