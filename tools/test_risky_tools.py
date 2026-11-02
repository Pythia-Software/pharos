"""Behavioral tests for the offline risky-action research prototype."""
import argparse
import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location('risky_study', Path(__file__).parent / 'risky_tools/study.py')
study = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(study)


class RiskyToolTests(unittest.TestCase):
    def hits(self, command, cwd='/repo'):
        return study.command_hits({'command': command}, cwd)[0]

    def kinds(self, command):
        return {h['kind'] for h in self.hits(command)}

    def test_actions_and_benign_data(self):
        cases = [
            ('git push origin HEAD', {'external_publish_send'}),
            ('git -C /repo push --force-with-lease origin HEAD', {'history_rewrite'}),
            ('git push origin +HEAD:main', {'history_rewrite'}),
            ('git push --tags', {'external_publish_send'}),
            ('git branch -D old', {'history_rewrite'}),
            ('git branch --show-current', set()),
            ('git reset --hard HEAD', {'history_rewrite'}),
            ('git rebase master', {'history_rewrite'}),
            ('git rebase --abort', set()),
            ('git lfs migrate import --everything', {'history_rewrite'}),
            ('rm -rf /tmp/build', {'delete'}),
            ('echo "rm -rf /Users/me"', set()),
            ("rg 'git push --force' README.md", set()),
            ('git commit -m "rm -rf /data; npm publish"', set()),
            ('brew install wget', {'install_system'}),
            ('npm --prefix web install', set()),
            ('npm install -g foo', {'install_system'}),
            ('sudo -u root env X=1 chmod 600 /etc/config', {'install_system', 'permission_change'}),
            ('gh api repos/a/b -f title=new', {'external_publish_send'}),
            ('gh api -X GET repos/a/b -f per_page=1', set()),
            ('gh pr view 1 --json title,body', set()),
            ('gh pr create --body "rm -rf /tmp/a"', {'external_publish_send'}),
            ('curl -s https://example.com', set()),
            ('curl -sSfG --data-urlencode action=query https://example.com', set()),
            ("sqlite3 db \"VACUUM INTO '/tmp/copy.db'\"", set()),
            ('diskutil apfs listSnapshots /System/Volumes/Data', set()),
            ('git worktree remove --force /tmp/probe', {'delete'}),
            ('git stash drop', {'history_rewrite'}),
            ('curl -XPOST --json "{}" https://example.com', {'external_publish_send'}),
            ('curl --request=DELETE https://example.com', {'external_publish_send'}),
            ('curl -X GET --data foo https://example.com', set()),
            ('kill -0 123', set()),
            ('kill --signal=0 123', set()),
            ('kill -TERM 123', {'process_control'}),
            ('security find-identity -v -p codesigning', set()),
            ('security find-generic-password -w -s service', {'credential'}),
            ('cat ~/.aws/credentials', {'credential'}),
            ('echo "DROP TABLE x"', set()),
            ('sqlite3 db "SELECT \'DROP TABLE x\'"', set()),
            ('sqlite3 db "DROP TABLE x"', {'data_loss'}),
            ('sqlite3 db "DELETE FROM steps WHERE id=1"', {'data_loss'}),
            ('sqlite3 db "EXPLAIN DELETE FROM steps"', set()),
            ('prisma migrate deploy', {'data_loss'}),
            ('gsutil rm gs://bucket/path', {'delete'}),
        ]
        for command, expected in cases:
            with self.subTest(command=command):
                self.assertEqual(self.kinds(command), expected)

    def test_context_dry_runs_and_unknown_variables(self):
        self.assertEqual(self.hits('rm -rf /tmp/a /tmp/b')[0]['effective_severity'], 'low')
        self.assertEqual(self.hits('rm -rf /tmp/a /Users/me/data')[0]['effective_severity'], 'high')
        self.assertEqual(self.hits('rm -rf "$TMPDIR/a"')[0]['effective_severity'], 'high')
        self.assertEqual(self.hits('rm -rf .context/test')[0]['effective_severity'], 'low')
        self.assertEqual(self.hits('git push --force --dry-run')[0]['effective_severity'], 'low')
        self.assertEqual(self.hits('git clean -fdn')[0]['effective_severity'], 'low')
        self.assertEqual(self.hits('gh pr create --body="--dry-run"')[0]['dry_run'], False)
        self.assertEqual(self.hits('curl -X POST http://127.0.0.1:7140/a')[0]['effective_severity'], 'caution')

    def test_redirection_quotes_and_paths(self):
        self.assertEqual(self.kinds("echo '>' /etc/profile"), set())
        self.assertEqual(self.kinds('echo "cat > ~/.zshrc"'), set())
        self.assertEqual(self.kinds('echo hello > ~/.zshrc'), {'install_system'})
        self.assertEqual(self.kinds('cat <<EOF'), set())  # ledger removes body
        self.assertEqual(self.kinds('echo data > /repo/a.txt'), set())
        self.assertEqual(self.kinds('echo data > /Users/me/a.txt'), {'outside_repository_write'})
        self.assertEqual(self.kinds('echo data > /dev/null'), set())
        self.assertEqual(self.kinds('echo data 2>&1'), set())
        self.assertEqual(self.kinds('echo "$(echo x > /etc/profile)"'), set())

    def test_outcomes_are_not_side_effect_claims(self):
        call = dict(tool_category='command', status='error', exit_code=1)
        segments = [dict(command='rm data', position=0, operator='', exit_code=None),
                    dict(command='git push origin HEAD', position=1, operator='&&', exit_code=None)]
        hits, gaps = study.classify(call, segments)
        self.assertEqual(len(hits), 2)
        self.assertTrue(all(h['outcome'] == 'error_possible_partial' for h in hits))
        self.assertEqual(hits[1]['operator'], '&&')
        call.update(status='no_result', exit_code=None)
        self.assertTrue(all(h['outcome'] == 'no_result' for h in study.classify(call, segments)[0]))
        call.update(tool_name='write_stdin')
        hits, gaps = study.classify(call, segments)
        self.assertEqual(hits, [])
        self.assertIn('non_execution_call_with_commands', gaps)

    def test_clipped_segments_are_explicit_gaps(self):
        hits, gaps = study.command_hits({'command': 'git push ' + 'x' * 500 + '…'})
        self.assertEqual(hits, [])
        self.assertIn('ledger_segment_truncated', gaps)

    def test_file_edits_and_mcp(self):
        call = dict(tool_category='edit', tool_name='Write', path_absolute='/Users/me/.zshrc', path_scope='external')
        self.assertEqual({h['kind'] for h in study.classify(call, [])[0]}, {'install_system', 'outside_repository_write'})
        call.update(path_absolute='/repo/file.go', path_scope='repo')
        self.assertEqual(study.classify(call, [])[0], [])
        call.update(path_absolute=None)
        self.assertIn('edit_path_unavailable', study.classify(call, [])[1])
        call = dict(tool_name='mcp__codex_apps__github_update_pull_request', mcp_server='github')
        self.assertEqual(study.classify(call, [])[0][0]['severity'], 'routine')
        call['tool_name'] = 'mcp__unknown__write_doc'
        self.assertEqual(study.classify(call, [])[0], [])
        self.assertIn('mcp_semantics_not_classified', study.classify(call, [])[1])

    def test_redaction(self):
        text = 'curl -H "Authorization: Bearer sensitive" https://u:pass@example.com/?api_key=private'
        cleaned = study.redact(text)
        self.assertNotIn('sensitive', cleaned)
        self.assertNotIn('private', cleaned)
        self.assertNotIn('u:pass', cleaned)
        secret = 'example-sensitive-value with spaces and "quotes"'
        structured = {'safe': 'retained', 'nested': [
            {'api_key': secret, 'Authorization': 'Bearer '+secret, 'private_key': 'line one\n'+secret},
            {'accessToken': 1234567, 'client-secret': {'value': secret}},
        ], 'embedded': json.dumps({'refresh_token': secret}), 'token_count': 42}
        for cleaned in [json.dumps(study.scrub(structured)), study.redact(json.dumps(structured)),
                        study.redact(json.dumps(json.dumps(structured))),
                        study.redact('response: '+json.dumps({'api_key': secret})),
                        study.redact("password='secret containing spaces'"),
                        study.redact('Authorization: Bearer bearer-value')]:
            self.assertNotIn('example-sensitive-value', cleaned)
            self.assertNotIn('1234567', cleaned)
            self.assertNotIn('secret containing spaces', cleaned)
            self.assertNotIn('bearer-value', cleaned)
        scrubbed = study.scrub(structured)
        self.assertEqual(scrubbed['safe'], 'retained')
        self.assertEqual(scrubbed['token_count'], 42)
        self.assertEqual(study.scrub({'authorization': 'unknown'}), {'authorization': 'unknown'})
        self.assertEqual(json.loads(study.redact('{"authorization":"explicitly_requested"}')), {'authorization': 'explicitly_requested'})
        with tempfile.TemporaryDirectory() as directory:
            for name, writer, data in [('source.jsonl', study.jsonl_write, [structured]), ('packet.json', study.json_write, structured)]:
                target = Path(directory) / name
                writer(target, data)
                self.assertNotIn('example-sensitive-value', target.read_text())

    def test_evaluation_is_separate_from_predictions(self):
        sample = [{'id': 'a', 'cohorts': ['precision', 'random'], 'hits': [{'kind': 'delete'}]},
                  {'id': 'b', 'cohorts': ['random'], 'hits': []}]
        labels = [{'id': 'a', 'review': {'risk_kinds': []}},
                  {'id': 'b', 'review': {'risk_kinds': ['data_loss']}}]
        metrics = study.evaluation(sample, labels)
        self.assertEqual(metrics['precision']['estimate'], 0)
        self.assertEqual(metrics['recall']['estimate'], 0)
        self.assertEqual(metrics['false_negative_ids'], ['b'])
        labels[0]['review'].update(authorization='explicitly_requested')
        with self.assertRaisesRegex(ValueError, 'requires evidence'):
            study.evaluation(sample, labels)

    def test_scan_read_only_snapshot_and_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source, out = root / 'catalog.sqlite3', root / 'study'
            db = sqlite3.connect(source)
            db.executescript('''
                CREATE TABLE repositories(id TEXT PRIMARY KEY,display_name TEXT);
                CREATE TABLE workspaces(id TEXT PRIMARY KEY,repository_id TEXT,location TEXT);
                CREATE TABLE tool_mirror_workspaces(workspace_id TEXT PRIMARY KEY);
                CREATE TABLE messages(id TEXT PRIMARY KEY,conversation_id TEXT,role TEXT,kind TEXT,selected INT,source_order INT,text TEXT);
                CREATE TABLE tool_calls(id TEXT PRIMARY KEY,workspace_id TEXT,conversation_id TEXT,call_message_id TEXT,started_at TEXT,tool_name TEXT,tool_category TEXT,command TEXT,status TEXT,exit_code INT);
                CREATE TABLE tool_commands(tool_call_id TEXT,position INT,operator TEXT,command TEXT,exit_code INT);
                INSERT INTO repositories VALUES('r','<repo>');
                INSERT INTO workspaces VALUES('w','r','/repo');
                INSERT INTO workspaces VALUES('mirror','r','/repo');
                INSERT INTO tool_mirror_workspaces VALUES('mirror');
                INSERT INTO messages VALUES('user','c','user','message',1,0,'Please remove the old cache');
                INSERT INTO messages VALUES('msg','c','assistant','tool_call',1,1,'{}');
                INSERT INTO tool_calls VALUES('a','w','c','msg','2026-10-21T07:00:00Z','exec','command','rm -rf /tmp/cache','ok',0);
                INSERT INTO tool_calls VALUES('b','mirror','c','msg','2026-10-21T08:00:00Z','exec','command','rm /data','ok',0);
                INSERT INTO tool_calls VALUES('c','w','c','msg','2026-10-28T06:00:00Z','exec','command','rm /data','ok',0);
                INSERT INTO tool_commands VALUES('a',0,'','rm -rf /tmp/cache',NULL);
            ''')
            db.commit()
            db.close()
            before = source.read_bytes()
            study.scan(argparse.Namespace(catalog=str(source), out=str(out), timezone='America/Denver',
                                          start='2026-10-21', end='2026-10-28', sample=10))
            manifest = json.loads((out / 'manifest.json').read_text())
            self.assertEqual(manifest['total_calls'], 1)
            self.assertEqual(manifest['counts_by_kind'], {'delete': 1})
            self.assertEqual(source.read_bytes(), before)
            self.assertFalse(Path(str(source) + '-wal').exists())
            report = (out / 'report.html').read_text()
            self.assertIn('&lt;repo&gt;', report)
            self.assertIn('/work/w?conversation=c&amp;message=msg', report)
            sample = study.jsonl_read(out / 'review.jsonl')[0]
            self.assertIsNone(sample['review']['risk_kinds'])
            self.assertEqual(sample['authorization'], 'unknown')
            self.assertIn('Please remove', sample['previous_request']['text'])
            (root / '.pharos-service.lock').touch()
            with self.assertRaisesRegex(ValueError, 'Refusing a library'):
                study.open_snapshot(source)
            (root / '.pharos-service.lock').unlink()
            Path(str(source) + '-wal').write_bytes(b'pending')
            with self.assertRaisesRegex(ValueError, 'nonempty WAL'):
                study.open_snapshot(source)

    def test_private_output_and_git_boundary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            study.private_write(root / 'private', 'x')
            self.assertEqual((root / 'private').stat().st_mode & 0o777, 0o600)
            (root / '.git').mkdir()
            with self.assertRaisesRegex(ValueError, 'outside git'):
                study.outside_git(root / 'study')


# The second pass is intentionally independent of the service and its database.
import sys
sys.modules.setdefault('study', study)
ENRICH_SPEC = importlib.util.spec_from_file_location('risky_enrich', Path(__file__).parent / 'risky_tools/enrich.py')
enrich = importlib.util.module_from_spec(ENRICH_SPEC)
ENRICH_SPEC.loader.exec_module(enrich)


class RiskySubjectTests(unittest.TestCase):
    def state(self, cwd='/Users/example/conductor/workspaces/demo/one'):
        return enrich.State(cwd, '/Users/example/conductor/workspaces/demo/one', 'tool input')

    def actions(self, command):
        return enrich.classify_segment(command, self.state())[0]

    def test_subject_identity_and_boundary(self):
        state=self.state()
        own=state.target('.context/research')
        other=state.target('/Users/example/conductor/workspaces/demo/two/.context/research')
        self.assertNotEqual(own['identity'], other['identity'])
        self.assertEqual(own['scope'],'this checkout')
        self.assertEqual(other['scope'],'another checkout')
        self.assertEqual(state.target('/tmp/cache')['scope'],'temporary location')
        self.assertEqual(state.target('$D/data')['resolution'],'unresolved')
        self.assertEqual(state.target('/tmp/scene-*')['resolution'],'pattern')
        self.assertEqual(own['role'],'research / workspace scratch')
        self.assertNotIn('disposable',own['role'])

    def test_literal_bindings_quotes_and_symbolic_substitutions(self):
        state=self.state();state.env['S']='/tmp/scope'
        self.assertEqual(enrich.lex('rm "$S/scenes"',state),['rm','/tmp/scope/scenes'])
        self.assertEqual(enrich.lex("rm '$S/scenes'",state),['rm','$S/scenes'])
        self.assertEqual(enrich.lex('kill $(cat app.pid)',state),['kill','$(cat app.pid)'])
        self.assertEqual(enrich.lex('rm "$(mktemp -d)/data"',state),['rm','$(mktemp -d)/data'])
        signal=self.actions('kill $(cat app.pid)')[0]
        self.assertEqual(len(signal['targets']),1)
        self.assertEqual(signal['targets'][0]['resolution'],'unresolved')

    def test_workdir_association_is_local_to_nested_call(self):
        value={'input':'text(await tools.exec_command({workdir:"/repo/a",cmd:"rm first"}));text(await tools.exec_command({cmd:"rm second",workdir:"/repo/b"}));text(await tools.exec_command({cmd:"rm third"}));'}
        commands=enrich.recover(value,'/fallback')
        self.assertEqual([c['cwd'] for c in commands],['/repo/a','/repo/b','/fallback'])
        self.assertEqual(commands[0]['cwd_basis'],'tool input')
        self.assertEqual(enrich.recover({'input':{'cmd':'rm x','workdir':'/repo/sub'}},'/repo')[0]['cwd'],'/repo/sub')

    def test_credential_source_destination_and_mode(self):
        copy=self.actions('cp ~/.config/gcloud/credentials.db ~/.config/gcloud/configurations .build/gcloud/')[0]
        self.assertEqual(copy['source_count'],2)
        self.assertTrue(copy['destination']['identity'].endswith('/one/.build/gcloud'))
        self.assertEqual(copy['targets'][0]['role'],'credential material')
        self.assertIn('configuration',copy['targets'][1]['role'])
        chmod=self.actions('chmod -R u+rwX,go-rwx .build/gcloud')[0]
        self.assertEqual(chmod['mode'],'u+rwX,go-rwx')
        self.assertTrue(chmod['recursive'])

    def test_git_subject_uses_effective_checkout(self):
        first=self.actions('git -C /repo/a push --force-with-lease origin HEAD:main')[0]
        second=self.actions('git -C /repo/b push --force-with-lease origin HEAD:main')[0]
        self.assertEqual(first['repository_target']['identity'],'/repo/a')
        self.assertTrue(first['lease'])
        self.assertNotEqual(first['targets'][0]['identity'],second['targets'][0]['identity'])
        restore=self.actions('git -C /repo/a restore -- generated.go')[0]
        self.assertEqual(restore['targets'][0]['identity'],'/repo/a/generated.go')

    def test_api_semantics_and_help(self):
        metadata=self.actions('gh api -X PATCH repos/o/r/pulls/3 -f title=New')[0]
        state=self.actions('gh api -X PATCH repos/o/r/pulls/3 -f state=closed')[0]
        self.assertEqual(metadata['action'],'edit PR metadata')
        self.assertEqual(state['action'],'GitHub API mutation')
        self.assertEqual(self.actions('gcloud builds submit --help'),[])
        self.assertEqual(self.actions('curl -X GET --data x https://example.com'),[])
        self.assertEqual(self.actions('curl -X POST --data "--help" https://example.com')[0]['action'],'HTTP mutation')

    def test_sql_predicate_and_unknown_connection(self):
        sql=self.actions('sqlite3 /Users/example/.gemini/history.db "DELETE FROM steps WHERE idx = 321 AND step_type = 17;"')[0]
        self.assertEqual(sql['table'],'steps')
        self.assertEqual(sql['predicate'],'idx = 321 AND step_type = 17')
        unresolved=self.actions('psql -c "DROP TABLE users"')[0]['targets'][0]
        self.assertEqual(unresolved['resolution'],'unresolved')
        self.assertNotEqual(unresolved['scope'],'this checkout')

    def test_nested_result_match_precedes_running_sibling(self):
        call=dict(status='ok',exit_code=None,backgrounded=False,result_input=json.dumps({'details':{'commands':[{'command':'kill 123','exit_code':0}]},'content':[{'text':json.dumps({'session_id':456})}]}))
        self.assertIn('invocation exit 0',enrich.invocation_outcome(call,'kill 123',2))
        self.assertIn('unconfirmed',enrich.invocation_outcome(call,'server',2))
        call['result_input']=json.dumps({'content':[{'text':json.dumps({'session_id':None,'exit_code':0})}]})
        self.assertNotIn('running',enrich.invocation_outcome(call,'kill 123',1))

    def test_python_hash_transform_differs_from_plaintext_copy(self):
        source="python3 - <<'PY'\nfrom pathlib import Path\nimport json,hashlib\ncfg=json.loads(Path('service.json').read_text())\np=Path('.context/hashes.json')\np.write_text(json.dumps({'sha256':hashlib.sha256(cfg['api_key'].encode()).hexdigest()}))\np.chmod(0o600)\nPY"
        actions=enrich.python_effects(source,self.state())
        self.assertEqual([a['action'] for a in actions],['credential hash written','change permissions'])
        self.assertTrue(actions[0]['sources'][0]['identity'].endswith('/service.json'))
        self.assertEqual(actions[1]['mode'],'0o600')
        plaintext=source.replace("{'sha256':hashlib.sha256(cfg['api_key'].encode()).hexdigest()}","{'key':cfg['api_key'],'sha256':hashlib.sha256(cfg['api_key'].encode()).hexdigest()}")
        self.assertEqual(enrich.python_effects(plaintext,self.state())[0]['action'],'Python file write')
        self.assertEqual(enrich.python_effects("cat <<'PY'\np.write_text(secret)\nPY",self.state()),[])

    def test_existing_splitter_skips_fixture_data_and_respects_early_close(self):
        commands=["cat <<'OUTER'\nrm -rf /important\nOUTER\necho done", "cat <<'EOF'\nsample\nEOF\nrm -rf /tmp/exposed\nEOF"]
        parsed=enrich.split_many(commands)
        self.assertFalse(any(s['Program']=='rm' for s in parsed[0]))
        self.assertTrue(any(s['Program']=='rm' for s in parsed[1]))

    def test_episode_identity_conversation_and_time_boundaries(self):
        base=dict(conversation_id='c1',action='rebase',cwd='/repo/a',repository='demo',targets=[],repository_target={'identity':'/repo/a'},started_at='2026-10-21T00:00:00Z',call_id='a',id='a')
        related=dict(base,action='force push / delete ref',call_id='b',id='b',started_at='2026-10-21T00:05:00Z')
        other=dict(related,id='c',call_id='c',conversation_id='c2')
        later=dict(related,id='d',call_id='d',started_at='2026-10-22T00:00:00Z')
        episodes=enrich.make_episodes([base,related,other,later])
        self.assertEqual(len(episodes),3)
        relation=next(e for e in episodes if e['relations'])
        self.assertEqual(set(relation['actions']),{'a','b'})

        # Same-call order must come from invocation/segment position, not the
        # identical outer timestamp or lexicographic segment IDs.
        push_first=dict(related,id='push',call_id='same',position=2,started_at=base['started_at'])
        rebase_later=dict(base,id='rebase',call_id='same',position=10)
        self.assertFalse(enrich.make_episodes([push_first,rebase_later])[0]['relations'])



sys.path.insert(0, str(Path(__file__).parent / 'risky_tools'))
import cases as case_builder
from unittest.mock import patch


class RiskyCaseQueueTests(unittest.TestCase):
    def action(self, key, name='delete', target='/repo/research', scope='this checkout', minute=0, **extra):
        value=dict(id=key,call_id='call-'+key,conversation_id='conversation-a',workspace_id='workspace-a',repository='demo',
                   started_at=f'2026-10-21T00:{minute:02d}:00Z',action=name,targets=[dict(identity=target,raw=target,scope=scope,role='unknown',resolution='resolved')],
                   command=name+' '+target,cwd='/repo',cwd_basis='tool input',outcome='outer call completion reported; action effect unverified',
                   result_status='ok',exit_code=0,execution='invoked; effect unverified',invocation=0,position=0,reader_path='/work/w?conversation=c&message=m')
        value.update(extra);return value

    def record(self,a):
        return dict(id=a['call_id'],repository='demo',reader_path=a['reader_path'],started_at=a['started_at'],action_ids=[a['id']],gaps=[],original_hits=[])

    def test_signal_depends_on_action_and_boundary_not_known_ids(self):
        temp=self.action('new-a',target='/tmp/new-data',scope='temporary location',recursive=True)
        source=self.action('new-b',target='/repo/research',recursive=True)
        other=self.action('new-c',target='/other/research',scope='another checkout',recursive=True)
        dynamic=self.action('new-d',scope='unresolved',recursive=True)
        self.assertEqual(case_builder.signals(temp),[])
        self.assertIn('persistent_delete',case_builder.signals(source))
        self.assertIn('recursive_boundary_delete',case_builder.signals(other))
        self.assertIn('recursive_boundary_delete',case_builder.signals(dynamic))
        self.assertIn('not_proven_disposable',case_builder.deferred_reason(temp))
        self.assertEqual(case_builder.signals(dict(other,dry_run=True)),[])

    def test_copy_and_hash_have_different_triage(self):
        destination=dict(identity='/repo/build/auth',resolution='resolved')
        copy=self.action('unseen',name='credential copy',destination=destination)
        hashed=self.action('unseen-hash',name='credential hash written')
        self.assertEqual(case_builder.signals(copy),['credential_copy'])
        self.assertEqual(case_builder.signals(hashed),[])
        cases,_=case_builder.build_cases([copy,hashed],[self.record(copy),self.record(hashed)])
        self.assertEqual(len(cases),1)
        self.assertEqual(cases[0]['status'],'not investigated')
        self.assertEqual(cases[0]['authorization'],'unknown')

    def test_merge_and_metadata_are_separate(self):
        merge=self.action('merge',name='merge PR')
        metadata=self.action('metadata',name='edit PR metadata')
        self.assertIn('publication',case_builder.signals(merge))
        self.assertEqual(case_builder.signals(metadata),[])

    def test_uncertain_compound_call_is_one_case(self):
        items=[self.action('db',name='database mutation'),self.action('system',name='system / install change',command='diskutil eraseDisk APFS test disk9'),self.action('delete',target='/elsewhere',scope='personal / external',recursive=True)]
        for i,a in enumerate(items):a.update(call_id='one-call',position=i,outcome='accepted / running; completion unconfirmed')
        cases,decisions=case_builder.build_cases(items,[self.record(items[0])])
        self.assertEqual(len(cases),1)
        self.assertIn('compound_uncertain',cases[0]['rule_ids'])
        self.assertEqual(set(cases[0]['seed_action_ids']),{'db','system','delete'})
        self.assertTrue(all(d['case_ids']==[cases[0]['id']] for d in decisions))
        # Dry runs do not manufacture compound consequence families.
        items[0]['dry_run']=True
        cases,_=case_builder.build_cases(items,[self.record(items[0])])
        self.assertFalse(any('compound_uncertain' in c['rule_ids'] for c in cases))

    def test_entities_conversations_and_time_boundaries(self):
        a=self.action('a',name='credential copy',destination=dict(identity='/repo/a/auth',resolution='resolved'))
        b=dict(a,id='b',call_id='b',started_at='2026-10-21T01:00:00Z')
        c=dict(a,id='c',call_id='c',started_at='2026-10-21T07:00:00Z')
        d=dict(a,id='d',call_id='d',conversation_id='other-conversation')
        e=dict(a,id='e',call_id='e',destination=dict(identity='/repo/b/auth',resolution='resolved'))
        items=[a,b,c,d,e];records=[self.record(x) for x in items]
        cases,decisions=case_builder.build_cases(items,records)
        self.assertEqual(len(cases),4)
        together=next(c for c in cases if 'b' in c['seed_action_ids'])
        self.assertEqual(set(together['seed_action_ids']),{'a','b'})
        again,again_decisions=case_builder.build_cases(list(reversed(items)),list(reversed(records)))
        self.assertEqual(cases,again)
        self.assertEqual(decisions,again_decisions)

    def test_credential_permissions_link_requires_exact_destination(self):
        seed=self.action('copy',name='credential copy',destination=dict(identity='/repo/a/auth',resolution='resolved'))
        own=self.action('own',name='change permissions',target='/repo/a/auth/credentials.db',minute=2)
        other=self.action('other',name='change permissions',target='/repo/b/auth/credentials.db',minute=2)
        self.assertTrue(case_builder.related_to(seed,own,'credential_copy'))
        self.assertFalse(case_builder.related_to(seed,other,'credential_copy'))
        self.assertFalse(case_builder.related_to(dict(seed,destination=dict(identity='$D',resolution='unresolved')),own,'credential_copy'))

    def test_ledger_mentions_are_only_context_leads(self):
        seed=self.action('a',name='credential copy',target='/config/credentials.db',destination=dict(identity='/repo/auth',resolution='resolved'))
        case=case_builder.build_cases([seed],[self.record(seed)])[0][0]
        mention=dict(id='mention',conversation_id='conversation-a',workspace_id='w',call_message_id='m',started_at=seed['started_at'],command="echo 'do not remove /config/credentials.db'",status='ok')
        result=case_builder.ledger_mentions(case,{'a':seed},[mention,dict(mention,id='other',conversation_id='other')])
        self.assertEqual(len(result),1)
        self.assertIn('Not proof',result[0]['interpretation'])

    def test_packet_is_bounded_and_retains_unknowns(self):
        seed=self.action('copy',name='credential copy',destination=dict(identity='/repo/auth',resolution='resolved'))
        case=case_builder.build_cases([seed],[self.record(seed)])[0][0]
        source=dict(id=seed['call_id'],call_input='x'*10000,result_input='y'*10000)
        context=[dict(evidence_id='context-'+str(i),content=case_builder.clipped('z'*10000,1800)) for i in range(10)]
        with patch.dict(case_builder.RULES,packet_bytes=10000):
            packet=case_builder.make_packet(case,{'copy':seed},{seed['call_id']:source},{},context,{})
        self.assertLessEqual(len((json.dumps(packet,ensure_ascii=False,indent=2)+'\n').encode()),10000)
        self.assertTrue(packet['omissions'])
        self.assertEqual(packet['investigation_status'],'not run')
        prompt=case_builder.investigation_prompt(packet)
        self.assertIn('Do not execute archived commands',prompt)
        self.assertIn('untrusted evidence',prompt)
        self.assertIn(case['id'],prompt)
        source.update(call_input=json.dumps({'api_key': 'input-credential'}),
                      result_input=json.dumps({'Authorization': 'Bearer result-credential'}))
        packet=case_builder.make_packet(case,{'copy':seed},{seed['call_id']:source},{},[],{})
        # The final clipboard boundary also scrubs older/prepared packets.
        packet['legacy_metadata']={'client_secret': 'legacy-credential'}
        prompt=case_builder.investigation_prompt(packet)
        for value in ['input-credential', 'result-credential', 'legacy-credential']:
            self.assertNotIn(value,prompt)

    def test_audit_excludes_selected_gap_and_is_order_independent(self):
        items=[self.action(str(i),target='/tmp/a',scope='temporary location') for i in range(4)]
        records=[self.record(a) for a in items]
        with tempfile.TemporaryDirectory() as d,patch.dict(case_builder.RULES,audit_sample=2):
            first=case_builder.audit_sample([],records,Path(d),{'call-0'})
            second=case_builder.audit_sample([],list(reversed(records)),Path(d),{'call-0'})
        self.assertEqual(first,second)
        self.assertEqual(len(first),2)
        self.assertNotIn('call-0',{x['id'] for x in first})
        self.assertTrue(all(x['review'] is None for x in first))

    def test_presentation_diversity_preserves_selection(self):
        candidates=[dict(id='a',primary_rule='copy',priority='investigate',start='2026-10-21T00:00:00Z'),
                    dict(id='b',primary_rule='copy',priority='investigate',start='2026-10-21T00:01:00Z'),
                    dict(id='c',primary_rule='database',priority='investigate',start='2026-10-21T00:02:00Z')]
        ordered=case_builder.diverse_order(candidates)
        self.assertEqual(len({c['primary_rule'] for c in ordered[:2]}),2)
        self.assertEqual({c['id'] for c in ordered},{c['id'] for c in candidates})
        self.assertEqual(ordered,case_builder.diverse_order(list(reversed(candidates))))

    def test_jsonl_preserves_unicode_line_separators(self):
        with tempfile.TemporaryDirectory() as d:
            path=Path(d)/'records.jsonl'
            rows=[{'text':'first\u2028second\u2029third'},{'text':'another'}]
            study.jsonl_write(path,rows)
            self.assertEqual(study.jsonl_read(path),rows)


if __name__ == '__main__':
    unittest.main()
