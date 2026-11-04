import importlib.util
import json
import pathlib
import sqlite3
import struct
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('label_trajectories', pathlib.Path(__file__).with_name('label-trajectories.py'))
lab = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lab)


class LabelTrajectoriesTests(unittest.TestCase):
    def test_model_order_and_invalid_probabilities(self):
        order = ['P', 'P.request', 'C.report']
        self.assertEqual(lab.decode_labels(5, struct.pack('<fff', .9, .8, .7), order), [['P', .9], ['C.report', .7]])
        for raw in (b'', struct.pack('<fff', float('nan'), .8, .7)):
            with self.assertRaises(ValueError): lab.decode_labels(5, raw, order)

    def test_typed_excerpt_uses_utf8_offsets(self):
        text = 'café: Ask me\nmore'
        spans = json.dumps([{'start': len('café: '.encode()), 'end': len(text.encode()), 'category': 'typed'}])
        value, source, truncated = lab.excerpt(text, 'H-typed+ctx', spans, 6)
        self.assertEqual((value, source, truncated), ('Ask me', 'Typed text excerpt', True))

    def fixture(self, path):
        db = sqlite3.connect(path)
        db.executescript('''
            CREATE TABLE message_labels_models(model,labels_json);
            CREATE TABLE message_labels(message_id,conversation_id,segment,labels,probs,model,chunks,labeled_at);
            CREATE TABLE conversations(id,workspace_id,native_id,aliases_json,agent_depth,started_at,provider,parent_id,agent_nickname);
            CREATE TABLE workspaces(id,title,repository_id);
            CREATE TABLE repositories(id,display_name);
            CREATE TABLE message_labels_dirty(conversation_id);
            CREATE TABLE message_labels_conversations(conversation_id);
            CREATE TABLE conversation_group_membership(conversation_id,active);
            CREATE TABLE messages(id,conversation_id,selected,kind,role,text,source_order,created_at,call_id);
            CREATE TABLE message_authorship(message_id,spans_json);
            CREATE TABLE agent_sessions(id,conversation_id,parent_id,child_conversation_id,delegation_message_id,native_id,kind,depth,started_at);
            CREATE TABLE agent_session_messages(message_id,agent_session_id,source_order);
        ''')
        db.execute('INSERT INTO message_labels_models VALUES(?,?)', ('fixture-model', '["P","P.request"]'))
        db.execute("INSERT INTO workspaces VALUES('work','Fixture',NULL)")
        for cid in ['main', 'mirror', 'dirty', 'child']:
            db.execute('INSERT INTO conversations VALUES(?,?,?,?,?,?,?,?,?)', (cid, 'work', cid, '[]', int(cid == 'child'), '2026-01-01', 'codex', 'main' if cid == 'child' else None, None))
            db.execute('INSERT INTO message_labels_conversations VALUES(?)', (cid,))
            db.execute('INSERT INTO agent_sessions VALUES(?,?,?,?,?,?,?,?,?)', (cid+'-session', cid, None, None, None, 'main', 'root' if cid != 'child' else 'subagent', 0, None))
            for i in range(2):
                mid = f'{cid}-{i}'
                db.execute("INSERT INTO messages VALUES(?,?,1,'message','assistant','Fixture',?,'2026-01-01',NULL)", (mid, cid, 1-i))
                db.execute("INSERT INTO message_labels VALUES(?,?,'AR',3,?,'fixture-model',1,'2026-01-01')", (mid, cid, struct.pack('<ff', .95, .9)))
                db.execute('INSERT INTO agent_session_messages VALUES(?,?,?)', (mid, cid+'-session', 1-i))
        db.execute("INSERT INTO message_labels_dirty VALUES('dirty')")
        db.execute("INSERT INTO conversation_group_membership VALUES('mirror',0)")
        db.execute("INSERT INTO messages VALUES('unlabeled','main',1,'message','user','Gap',2,'2026-01-01',NULL)")
        db.execute("INSERT INTO messages VALUES('call','main',1,'delegation','assistant','Call',3,'2026-01-01','native-call')")
        db.execute("INSERT INTO messages VALUES('receipt','main',1,'delegation_result','tool','Async launch acknowledged',4,'2026-01-01','native-call')")
        db.execute("INSERT INTO agent_sessions VALUES('empty-handle','main','main-session','child','call','native-call','subagent',1,NULL)")
        db.commit()
        return db

    def test_read_only_export_retains_nested_order_and_resolves_empty_handles_once(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory)/'catalog.sqlite3'
            db = self.fixture(path)
            before = path.read_bytes()
            result = lab.extract(path, limit=10, minimum=2)
            self.assertEqual([c['id'] for c in result['conversations']], ['main'])
            c = result['conversations'][0]
            self.assertEqual(result['version'], 2)
            self.assertEqual(c['main_session'], 'main-session')
            self.assertEqual([m['id'] for m in c['messages'] if m['session_id'] == c['main_session']], ['main-1', 'main-0', 'unlabeled', 'call', 'receipt'])
            self.assertEqual([m['id'] for m in c['messages'] if m['session_id'] == 'child-session'], ['child-1', 'child-0'])
            self.assertEqual({s['id'] for s in c['sessions']}, {'main-session', 'child-session'})
            child = next(s for s in c['sessions'] if s['id'] == 'child-session')
            self.assertEqual(child['parent_id'], 'main-session')
            self.assertEqual(child['call_message_id'], 'call')
            self.assertEqual(child['result_message_id'], 'receipt')
            self.assertEqual(child['link_evidence'], 'delegation handle')
            self.assertEqual(result['source']['models'], ['fixture-model'])
            self.assertEqual(path.read_bytes(), before)
            self.assertFalse(path.with_suffix('.sqlite3-wal').exists())
            db.close()

    def test_generation_model_is_distinct_from_the_label_encoder(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory)/'catalog.sqlite3'
            db = self.fixture(path)
            db.execute('ALTER TABLE messages ADD COLUMN model TEXT')
            db.execute("UPDATE messages SET model='generation-model' WHERE id='main-0'")
            db.commit()
            before = path.read_bytes()
            result = lab.extract(path, limit=10, minimum=2)
            m = next(m for m in result['conversations'][0]['messages'] if m['id']=='main-0')
            self.assertEqual(m['generation_model'], 'generation-model')
            self.assertEqual(result['source']['models'], ['fixture-model'])
            self.assertEqual(before, path.read_bytes())
            db.close()

    def test_dirty_child_labels_are_withheld_and_results_are_not_invented(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory)/'catalog.sqlite3'
            db = self.fixture(path)
            db.execute("INSERT INTO message_labels_dirty VALUES('child')")
            db.execute("DELETE FROM messages WHERE id='receipt'")
            for handle in ['unseen-one', 'unseen-two']:
                db.execute("INSERT INTO agent_sessions VALUES(?, 'child', 'child-session', NULL, NULL, ?, 'subagent', 1, NULL)", (handle, handle))
            db.commit()
            result = lab.extract(path, limit=10, minimum=2, roots={'main'})
            c = result['conversations'][0]
            child = next(s for s in c['sessions'] if s['id'] == 'child-session')
            self.assertTrue(child['labels_pending'])
            self.assertIsNone(child['result_message_id'])
            messages = [m for m in c['messages'] if m['session_id'] == child['id']]
            self.assertEqual(len(messages), 2)
            self.assertTrue(all(not m['labels'] and m['chunks'] == 0 for m in messages))
            self.assertTrue(result['source']['retained_roots'])
            handles = [s for s in c['sessions'] if s['id'].startswith('unseen-')]
            self.assertEqual(len({s['name'] for s in handles}), 2)
            self.assertTrue(all(s['name'].startswith('Unresolved call ') for s in handles))
            db.close()

    def test_tool_ledger_keeps_native_session_and_recorded_boundaries_without_writes(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory)/'catalog.sqlite3'
            db = self.fixture(path)
            db.executescript('''CREATE TABLE tool_calls(id,conversation_id,agent_session_id,call_message_id,
                tool_name,tool_category,command_category,status,started_at,ended_at,duration_ms);
                INSERT INTO tool_calls VALUES('tool','main','main-session','tool-message','exec_command','shell','git','ok','2026-01-01T00:00:02Z','2026-01-01T00:00:04Z',2000);
                INSERT INTO tool_calls VALUES('child-tool','child','child-session','child-message','read_file','read','','ok','2026-01-01T00:00:05Z',NULL,NULL);''')
            db.commit()
            before = path.read_bytes()
            c = lab.extract(path, limit=10, minimum=2)['conversations'][0]
            self.assertEqual({t['id'] for t in c['activity']}, {'tool','child-tool'})
            tool = next(t for t in c['activity'] if t['id']=='tool')
            self.assertEqual(tool['session_id'], 'main-session')
            self.assertEqual(tool['message_id'], 'tool-message')
            self.assertEqual(tool['duration_ms'], 2000)
            self.assertIsNone(next(t for t in c['activity'] if t['id']=='child-tool')['end'])
            self.assertEqual(path.read_bytes(), before)
            db.close()


if __name__ == '__main__': unittest.main()
