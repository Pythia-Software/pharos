import { test } from 'node:test';
import assert from 'node:assert/strict';
import { authorshipCategories } from '../../../src/authorship-categories';
import { authorshipRuleLabel, explainAuthorshipRule } from '../../../src/authored';
import messages from '../../../../schemas/messages.schema.json';
import writingMessages from '../../../../schemas/writing_messages.schema.json';
import peopleMessageLabels from '../../../../schemas/people_message_labels.schema.json';

test('stored instruction-block rules keep their specific explanation', () => {
  const generic = authorshipCategories.find(category => category.key === 'harness').description;
  const instruction = explainAuthorshipRule('Conductor system instruction', 'harness');
  assert.notEqual(instruction, generic);
  for (const rule of ['Harness context', 'System reminder', 'Environment context', 'Project instructions', 'Plugin recommendations']) {
    assert.equal(explainAuthorshipRule(rule, 'harness'), instruction, rule);
  }
  assert.equal(explainAuthorshipRule('Unrecognized rule', 'harness'), generic);
});

test('a stored rule is shown under its display name, and the Rules filters agree', () => {
  assert.equal(authorshipRuleLabel('Harness context'), 'App instructions');
  assert.equal(authorshipRuleLabel('System reminder'), 'System reminder');
  assert.equal(explainAuthorshipRule('Harness context', 'harness'), explainAuthorshipRule('System reminder', 'harness'), 'the stored value still finds its explanation');
  for (const [name, schema] of Object.entries({ messages, writingMessages, peopleMessageLabels })) {
    const options = schema.fields.find(field => field.filter?.values?.options?.includes('No other rule matched')).filter.values.options;
    for (const option of options) {
      const value = typeof option === 'string' ? option : option.value, label = typeof option === 'string' ? option : option.label;
      assert.equal(label, authorshipRuleLabel(value), `${name}: ${value}`);
    }
    assert.ok(options.some(option => option.value === 'Harness context'), `${name} keeps the stored value`);
  }
});
