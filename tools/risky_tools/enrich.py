#!/usr/bin/env python3
"""Subject/episode research pass over the v1 candidate cohort, never executes input."""
import argparse
import ast
from collections import Counter, defaultdict
import hashlib
import html
import json
import os
from pathlib import Path
import posixpath as pp
import re
import subprocess
import tempfile
from urllib.parse import urlsplit
import study

REPO = Path(__file__).resolve().parents[2]
SHELL_SOURCE = REPO / 'internal/archive/shell_command.go'
JS_STRING = r'"(?:[^"\\]|\\.)*"'
# Deliberately accept only literal JSON strings and simple object fields. No eval.
JS_COMMAND = re.compile(r'\bcmd"?\s*:\s*(' + JS_STRING + r')')
JS_CWD = re.compile(r'\b(?:workdir|cwd)"?\s*:\s*(' + JS_STRING + r')')
HOME_RE = re.compile(r'^(/Users/[^/]+|/home/[^/]+)(?:/|$)')
CREDENTIAL = re.compile(r'(credentials\.db|access_tokens\.db|application_default_credentials\.json|/\.aws/credentials|/id_(?:rsa|ed25519)(?:$|\.)|/\.netrc|/\.npmrc|(?:^|/)\.env(?:\.|$))', re.I)
GO_WRAPPER = r'''package main
import("bufio";"encoding/json";"fmt";"os")
func main(){s:=bufio.NewScanner(os.Stdin);s.Buffer(make([]byte,4096),8*1024*1024);for s.Scan(){var line string;if json.Unmarshal(s.Bytes(),&line)!=nil{panic("invalid input")};c:=parseShellCommand(line);type seg struct{Command,Operator,Program string};out:=[]seg{};for _,v:=range c.Segments{out=append(out,seg{v.Text,v.Operator,v.Program})};b,_:=json.Marshal(out);fmt.Println(string(b))};if s.Err()!=nil{panic(s.Err())}}
'''


def split_many(commands):
    """Compile the exact current archive splitter outside the project package."""
    with tempfile.TemporaryDirectory(prefix='pharos-risk-split-') as tmp:
        root = Path(tmp)
        (root / 'shell.go').write_text(SHELL_SOURCE.read_text().replace('package archive', 'package main', 1))
        (root / 'main.go').write_text(GO_WRAPPER)
        subprocess.run(['go', 'build', '-o', str(root / 'split'), str(root / 'shell.go'), str(root / 'main.go')], check=True, capture_output=True)
        result = subprocess.run([str(root / 'split')], input=''.join(json.dumps(c)+'\n' for c in commands), text=True, capture_output=True, check=True)
        return [json.loads(line) for line in result.stdout.splitlines()]


def decode(value):
    try:
        return json.loads(value)
    except (ValueError, TypeError):
        return value


def recover(input_value, fallback=''):
    """Recover full commands + *local* literal workdir; never execute Code Mode."""
    value = decode(input_value)
    if isinstance(value, dict) and 'input' in value:
        value = value['input']
    if isinstance(value, dict):
        command = next((value[k] for k in ['cmd','command','CommandLine','command_line'] if isinstance(value.get(k), str)), None)
        if command is not None:
            cwd = next((value[k] for k in ['workdir','cwd','Cwd','working_directory'] if isinstance(value.get(k), str)), '')
            return [dict(command=command, cwd=cwd or fallback, cwd_basis='tool input' if cwd else 'workspace fallback')]
        if isinstance(value.get('command'), list) and len(value['command']) >= 3 and value['command'][1] in ['-c','-lc']:
            return [dict(command=value['command'][2], cwd=value.get('cwd') or fallback, cwd_basis='tool input' if value.get('cwd') else 'workspace fallback')]
        return []
    if isinstance(value, str):
        output = []
        for match in JS_COMMAND.finditer(value):
            # Read only the remainder of this object, stopping before another cmd.
            # A nested/nonliteral cwd is rejected, never borrowed from another call.
            tail = value[match.end():]
            boundary = re.search(r'\b(?:cmd|tools\.[A-Za-z_]+)\b|\}\s*\)', tail)
            tail = tail[:boundary.start()] if boundary else tail
            # Literal workdir can precede cmd in the same exec_command object.
            start=value.rfind('exec_command({',0,match.start())
            prefix=value[start+len('exec_command({'):match.start()] if start>=0 else ''
            if JS_COMMAND.search(prefix):prefix=''
            cm = JS_CWD.search(prefix+tail)
            cwd = json.loads(cm[1]) if cm else ''
            output.append(dict(command=json.loads(match[1]), cwd=cwd or fallback, cwd_basis='tool input' if cwd else 'workspace fallback'))
        return output
    return []


def cache_sources(source_study, out):
    manifest = json.loads((source_study / 'manifest.json').read_text())
    seeds = study.jsonl_read(source_study / 'matches.jsonl')
    sampled = {r['id']: r for r in study.jsonl_read(source_study / 'review.jsonl')}
    evaluation = json.loads((source_study / 'evaluation.json').read_text())
    selected = {r['id']:r for r in seeds}
    for key in evaluation['false_negative_ids']:
        selected.setdefault(key, dict(sampled[key], audit_miss=True))
    db, source, before = study.open_snapshot(manifest['source'])
    if before != manifest['source_fingerprint']:
        raise ValueError('Snapshot differs from the first-pass study fingerprint')
    sources = []
    for key, seed in selected.items():
        row = db.execute('''SELECT t.*,w.location,w.branch,w.repository_id FROM tool_calls t
                            LEFT JOIN workspaces w ON w.id=t.workspace_id WHERE t.id=?''', (key,)).fetchone()
        record = dict(row)
        for kind in ['call','result']:
            msg = db.execute('SELECT text FROM messages WHERE id=?',(record[kind+'_message_id'],)).fetchone()
            record[kind+'_input'] = msg[0] if msg else ''
        record.update(repository=seed['repository'], reader_path=seed['reader_path'], v1_hits=seed['hits'], audit_miss=seed.get('audit_miss',False))
        sources.append(record)
    # Context cache: only retained ledger commands in the selected conversations.
    # It supports locating possible launches/creation, not inferring inherited cwd.
    contexts = []
    for cid in sorted({r['conversation_id'] for r in sources}):
        for row in db.execute('''SELECT id,started_at,command,status,exit_code,backgrounded,call_message_id,workspace_id
                              FROM tool_calls WHERE conversation_id=? AND started_at>=? AND started_at<?
                              AND tool_name IN ('exec','Bash','exec_command','run_command','shell') ORDER BY started_at''', (cid, manifest['start'],manifest['end'])):
            contexts.append(dict(row, conversation_id=cid))
    if before != study.fingerprint(source):
        raise ValueError('Snapshot changed')
    db.close()
    study.jsonl_write(out / 'sources.jsonl', sources)
    study.jsonl_write(out / 'context.jsonl', contexts)
    study.json_write(out / 'cohort.json', dict(source_study=str(source_study), source_fingerprint=before, start=manifest['start'],end=manifest['end'],population=manifest['total_calls'],v1_candidates=len(seeds),audit_misses_added=len(sources)-len(seeds),context_calls=len(contexts), v1_coverage_gaps=manifest['coverage_gaps']))


class State:
    def __init__(self, cwd, workspace, basis='workspace fallback'):
        self.cwd, self.workspace, self.basis = cwd, workspace, basis
        home = HOME_RE.match(workspace or cwd or '')
        self.home = home[1] if home else ''
        self.env = {'PWD':cwd, 'HOME':self.home}
        self.bindings = {}
        self.ambiguous = False

    def expand(self, value):
        if value.startswith('~/') and self.home:
            value = self.home + value[1:]
        def sub(m):
            key = m[1] or m[2]
            return self.env.get(key, m[0])
        return re.sub(r'\$\{([A-Za-z_]\w*)\}|\$([A-Za-z_]\w*)', sub, value)

    def target(self, raw, kind='path'):
        # lex() has already expanded only shell-expandable characters.
        value = raw
        unknown = bool(re.search(r'[$`]|\b(?:unresolved|unknown|inferred by CLI)\b',value)) or not value
        if kind != 'path' or '://' in value:
            return dict(raw=raw,identity=value or '(unknown)',scope='remote' if '://' in value else kind,role=kind,resolution='unresolved' if unknown else 'literal',basis=self.basis)
        if re.search(r'[$`]|[\x00-\x1f]',value) or not value or not self.cwd or not value.startswith('/') and self.ambiguous:
            return dict(raw=raw,identity=value,scope='unresolved',role='unknown',resolution='unresolved',basis=self.basis)
        value = self.env.get('HOME',self.home) + value[1:] if value.startswith('~/') and self.env.get('HOME',self.home) else value
        absolute = pp.normpath(value if value.startswith('/') else pp.join(self.cwd,value))
        inside = lambda root: bool(root and (absolute == root or absolute.startswith(root.rstrip('/')+'/')))
        scope = 'this checkout' if inside(self.workspace) else 'another checkout' if re.search(r'/(?:conductor/workspaces/[^/]+/[^/]+|\.candidate-worktrees/[^/]+|\.claude/worktrees/[^/]+)(?:/|$)',absolute) else 'temporary location' if re.match(r'^/(?:private/)?(?:tmp|var/folders)(?:/|$)',absolute) else 'agent state' if re.search(r'/\.(?:claude|codex|gemini|tl1)(?:/|$)',absolute) else 'system' if absolute=='/' or re.match(r'^/(?:etc|System|Library|usr|bin|sbin)(?:/|$)',absolute) else 'personal / external'
        role = 'credential material' if CREDENTIAL.search(absolute) else 'cloud configuration (may contain credentials)' if re.search(r'/\.config/gcloud(?:/|$)',absolute) else 'agent conversation database' if '/.gemini/' in absolute and absolute.endswith('.db') else 'agent memory' if '/memory/' in absolute and scope=='agent state' else 'research / workspace scratch' if '/.context/' in absolute else 'build output (path hint)' if re.search(r'/(?:\.build|dist|node_modules|target|\.test-build)(?:/|$)',absolute) else 'temporary path' if scope=='temporary location' else 'source / checkout content' if scope in {'this checkout','another checkout'} else 'unknown'
        return dict(raw=raw,identity=absolute,scope=scope,role=role,resolution='pattern' if re.search(r'[*?\[]',value) else 'resolved',basis=self.basis)


def lex(text, state):
    """Quote-aware words and literal variable substitution, no shell evaluation.

    Substitutions, globs, tilde-user expansion and unknown variables stay symbolic.
    Redirection terminates argv; the archive splitter has already skipped heredocs.
    """
    tokens, word, quote, i, active = [], '', '', 0, False
    while i < len(text):
        ch = text[i]
        if quote != "'" and (text.startswith('$(',i) or ch=='`'):
            # Keep a substitution as one symbolic token, including spaces.
            end=i+2 if ch=='$' else i+1
            depth=1; inner_quote=''
            while end<len(text):
                c=text[end]
                if c=='\\':end+=2;continue
                if inner_quote:
                    if c==inner_quote:inner_quote=''
                elif c in "'\"":inner_quote=c
                elif ch=='`' and c=='`':end+=1;depth=0;break
                elif ch=='$':
                    if c=='(':depth+=1
                    if c==')':
                        depth-=1
                        if not depth:end+=1;break
                end+=1
            word+=text[i:end];i=end;active=True;continue
        if ch == '\\' and quote != "'" and i+1 < len(text):
            if text[i+1] != '\n':
                word += text[i+1]
            i += 2; active = True; continue
        if quote:
            if ch == quote:
                quote = ''
            elif quote == '"' and ch == '$':
                m = re.match(r'\$(?:\{[A-Za-z_]\w*\}|[A-Za-z_]\w*)',text[i:])
                if m:
                    word += state.expand(m[0]); i += len(m[0]); continue
                word += ch
            else:
                word += ch
        elif ch in "'\"":
            quote = ch; active = True
        elif ch.isspace():
            if active or word:
                tokens.append(word); word='';active=False
        elif ch in '<>':
            if word and not word.isdigit():tokens.append(word)
            return tokens
        elif ch == '$':
            m = re.match(r'\$(?:\{[A-Za-z_]\w*\}|[A-Za-z_]\w*)',text[i:])
            if m:
                word += state.expand(m[0]); i += len(m[0]); active=True; continue
            word += ch;active=True
        else:
            word += ch; active=True
        i += 1
    if quote:
        return []
    if active or word:tokens.append(word)
    return tokens


def option(args, flags):
    for i, value in enumerate(args):
        if value in flags and i+1 < len(args):return args[i+1]
        for flag in flags:
            if value.startswith(flag+'='):return value[len(flag)+1:]
    return ''


def flags_have(args, flag):
    return study.has_flag(args, flag)


def classify_segment(raw, state):
    tokens = lex(raw,state)
    args, wrappers = study.invocation(tokens)
    if not args:return [], []
    program, rest = pp.basename(args[0]),args[1:]
    a = study.action_args(program,rest)
    sub = a[0] if a else ''
    hits, gaps = [], []
    def add(action, targets, kind='path', **fields):
        hits.append(dict(action=action,targets=[state.target(t,kind) for t in targets],command=study.redact(raw),cwd=state.cwd,cwd_basis=state.basis,**fields))
    # --help is only recognized in argv, never inside quoted payload text.
    help_args=[]
    skip=False
    for token in rest:
        if skip:skip=False;continue
        help_args.append(token)
        if token in {'-d','--data','--data-raw','--data-binary','--data-urlencode','--json','-F','--form','-f','--field','--raw-field','--input','-m','--message','--config'}:skip=True
    if '--help' in help_args or (program in {'gcloud','aws','gsutil'} and '-h' in help_args):
        return [], ['read-only help excluded']
    if program in {'rm','rmdir','unlink','shred','srm','trash'}:
        add('delete', study.operands(rest),recursive=study.short_flag(rest,'r') or study.short_flag(rest,'R') or '--recursive' in rest,force=study.short_flag(rest,'f') or '--force' in rest,recovery='No backup or tracked-file recovery established')
    if program in {'cp','mv','rsync'}:
        operands=study.operands(rest,{'-t','--target-directory','--exclude','--include','-e','--filter','--chmod','--exclude-from'})
        dest=option(rest,{'-t','--target-directory'}) or (operands[-1] if operands else '')
        src=operands if option(rest,{'-t','--target-directory'}) else operands[:-1]
        if dest and any(CREDENTIAL.search(state.target(x)['identity']) or '/.config/gcloud' in state.target(x)['identity'] for x in src):
            add('credential copy' if program!='mv' else 'credential move',src+[dest],source_count=len(src),destination=state.target(dest),recovery='Additional copies can outlive the original session; deletion not established')
        if program=='rsync' and any(x.startswith('--delete') for x in rest):
            add('destination pruning',[dest],dry_run='--dry-run' in rest or study.short_flag(rest,'n'))
    if program in {'chmod','chown','chgrp','setfacl'}:
        operands=study.operands(rest,{'--reference'})
        if operands:add('change permissions',operands[1:],mode=operands[0],recursive=study.short_flag(rest,'R'),recovery='Previous mode/owner not recorded')
    if program in {'kill','pkill','killall'} and not any(x in rest for x in ['-0','--signal=0','-l','--list']):
        operands=study.operands(rest,{'-s','--signal','-u','-f'})
        # pkill -f value is the selection expression, not a flag's decoration.
        if program=='pkill' and '-f' in rest:operands=[rest[rest.index('-f')+1]] if len(rest)>rest.index('-f')+1 else []
        add('signal process',operands,'process',selector=program,signal=option(rest,{'-s','--signal'}) or next((x[1:] for x in rest if re.match(r'^-(?:[A-Z]+|[1-9]\d*)$',x)),'TERM (default)'),ownership='Unknown: no launch/PID association established')
    if program=='git':
        local=State(state.cwd,state.workspace,state.basis)
        local.env=dict(state.env)
        for i,v in enumerate(rest):
            if v=='-C' and i+1<len(rest):
                t=local.target(rest[i+1]); local.cwd=t['identity'] if t['resolution']=='resolved' else '';local.basis='git -C'
        repo=local.target('.')
        before=len(hits)
        extra=dict(repository_target=repo,lease='--force-with-lease' in a,dry_run='--dry-run' in a or study.short_flag(a,'n'))
        operands=study.operands(a[1:],{'--onto','--source','-s','--recurse-submodules'})
        if sub=='push':
            force=any(v in a for v in ['--force','--force-with-lease','--delete']) or study.short_flag(a,'f') or any(v.startswith(('+',':')) for v in operands)
            add('force push / delete ref' if force else 'publish tags' if '--tags' in a or '--follow-tags' in a else 'push branch',operands or ['configured remote / ref (unknown)'],'git ref',**extra)
        elif sub=='rebase' and not any(v in a for v in ['--abort','--quit','--show-current-patch']):
            add('rebase',operands or ['current branch; upstream unknown'],'git ref',**extra)
        elif sub=='reset' and '--hard' in a:
            add('hard reset',operands or ['HEAD'],'git ref',**extra)
        elif sub=='branch' and study.short_flag(a,'D'):
            add('delete local branch',operands,'git ref',**extra)
        elif sub in {'restore','checkout'} and (sub=='restore' or '--' in a or '--force' in a or study.short_flag(a,'f')):
            paths=a[a.index('--')+1:] if '--' in a else operands
            hits.append(dict(action='restore working files',targets=[local.target(p) for p in paths],command=study.redact(raw),cwd=local.cwd,cwd_basis=local.basis,**extra))
        elif sub=='clean' and (study.short_flag(a,'f') or '--force' in a):
            hits.append(dict(action='delete untracked files',targets=[local.target(x) for x in (operands or ['.'])],command=study.redact(raw),cwd=local.cwd,cwd_basis=local.basis,ignored_included=study.short_flag(a,'x'),**extra))
        elif sub=='worktree' and len(a)>1 and a[1]=='remove':
            hits.append(dict(action='remove checkout',targets=[local.target(x) for x in study.operands(a[2:])],command=study.redact(raw),cwd=local.cwd,cwd_basis=local.basis,**extra))
        elif sub=='stash' and len(a)>1 and a[1] in {'drop','clear'}:
            add('discard stash',a[2:] or ['all stashes' if a[1]=='clear' else 'stash@{0}'],'git ref',**extra)
        elif sub in {'filter-branch','filter-repo'} or a[:3]==['lfs','migrate','import']:
            add('rewrite repository history',[repo['identity']],'git repository',**extra)
        for hit in hits[before:]:
            for target in hit['targets']:
                if target['scope']=='git ref':
                    target['identity']=repo['identity']+' :: '+target['identity']
    if program=='gh':
        verb=a[1] if len(a)>1 else ''
        repo=option(rest,{'-R','--repo'}) or state.workspace
        if sub=='api':
            endpoint=next((x for x in a[1:] if re.match(r'/?(?:repos/|graphql$)',x)), '')
            method=option(a,{'-X','--method'})
            fields=[v for i,v in enumerate(a) if i and a[i-1] in {'-f','-F','--field','--raw-field'}]
            if not method:method='POST' if fields or '--input' in a else 'GET'
            if method not in {'GET','HEAD','OPTIONS'}:
                metadata_only=bool(fields) and all(v.split('=',1)[0] in {'title','body'} for v in fields) and '--input' not in a
                label='edit PR metadata' if method=='PATCH' and re.search(r'/pulls/\d+$',endpoint) and metadata_only else 'merge PR' if re.search(r'/pulls/\d+/merge$',endpoint) else 'GitHub API mutation'
                add(label,[endpoint or 'unresolved GitHub endpoint'],'GitHub resource',method=method,field_names=[v.split('=',1)[0] for v in fields])
        elif sub in {'pr','issue','release'} and verb in {'create','edit','comment','close','reopen','merge','review','upload','delete','delete-asset'}:
            target=next((x for x in a[2:] if re.match(r'^(?:\d+|https://github.com/|v\d)',x)), 'current '+sub+' (inferred by CLI)')
            add(('merge PR' if sub=='pr' and verb=='merge' else f'{verb} {sub}'),[str(repo)+' :: '+target],'GitHub resource',method=verb)
        elif sub=='auth' and verb in {'token','login','refresh','setup-git','logout'}:
            add('credential access',['GitHub authentication'],'credential')
    if program=='curl':
        method=option(rest,{'-X','--request'}) or next((v[2:] for v in rest if v.startswith('-X') and len(v)>2),'')
        data=any(x in rest for x in ['-d','--data','--data-raw','--data-binary','--json','-F','--form','-T','--upload-file'])
        if (method or data) and method not in {'GET','HEAD','OPTIONS'} and not any(x in rest for x in ['-G','--get','-I','--head']):
            urls=[x for x in rest if x.startswith(('http://','https://')) or re.match(r'^\$\w+/',x)]
            targets=urls or ['unresolved endpoint']
            add('local API mutation' if urls and all(study.is_local_url(x) for x in urls) else 'HTTP mutation',targets,'HTTP endpoint',method=method or 'POST',payload_sources=[x[1:] for x in rest if x.startswith('@')],recovery='Endpoint semantics and actual response require review')
    if program in {'sqlite3','psql','mysql'}:
        sqls=[x for x in rest if re.search(r'\b(?:DELETE FROM|DROP TABLE|TRUNCATE|UPDATE\s+\w+\s+SET)\b',x,re.I)]
        for sql in sqls:
            if not study.sql_mutates(sql):continue
            database=next((x for x in rest if x.endswith(('.db','.sqlite','.sqlite3'))), '')
            match=re.search(r'\b(DELETE\s+FROM|DROP\s+TABLE|TRUNCATE(?:\s+TABLE)?|UPDATE)\s+([\w."`]+)',sql,re.I)
            add('database mutation',[database or 'database connection unresolved'],'path' if database else 'database connection',table=match[2] if match else 'unknown',operation=match[1] if match else 'mutation',predicate=re.split(r'\bWHERE\b',sql,flags=re.I)[1].strip().rstrip(';') if re.search(r'\bWHERE\b',sql,re.I) else 'NO WHERE predicate',recovery='No backup or transaction rollback established')
    if program in {'gsutil','gcloud','aws'} and (sub=='rm' or a[:2] in [['storage','rm'],['s3','rm']]):
        add('delete cloud objects',[x for x in rest if x.startswith(('gs://','s3://'))],'cloud object',recursive=study.short_flag(rest,'r') or '--recursive' in rest)
    if program in {'diskutil','hdiutil','defaults','launchctl','systemctl','brew','npm','pip','gcloud','shutdown','reboot','security'}:
        existing,_=study.command_hits({'command':raw},state.cwd)
        for h in existing:
            if h['kind'] in {'install_system','spending','credential'}:
                targets=study.operands(a[1:])
                add('system / install change' if h['kind']=='install_system' else 'cloud provisioning' if h['kind']=='spending' else 'credential access',targets[:8] or [sub], 'system resource',operation=sub)
    if program in {'cat','head','tail','base64'}:
        sensitive=[v for v in study.operands(rest) if CREDENTIAL.search(state.target(v)['identity'])]
        if sensitive:add('credential read',sensitive)
    for target in study.redirection_targets(raw):
        dest=state.target(state.expand(target))
        if dest['identity']!='/dev/null' and dest['scope'] in {'agent state','personal / external','system'}:
            add('append external file' if '>>' in raw else 'overwrite external file',[dest['identity']],recovery='Prior contents and backup availability not established')
    if 'sudo' in wrappers:
        gaps.append('privileged execution; arguments require interpretation')
    if program in study.OPAQUE or program.startswith('python3.') or program.startswith('$'):
        gaps.append('interpreter / dynamic command not semantically interpreted')
    return hits,gaps


def invocation_outcome(call, command, count):
    """Only attribute a nested result when its full command matches exactly."""
    envelope=decode(call.get('result_input',''))
    if isinstance(envelope,dict):
        exact=[r for r in envelope.get('details',{}).get('commands',[]) if r.get('command')==command]
        if len(exact)==1:
            result=exact[0]
            if result.get('exit_code') is not None:
                return ('invocation exit 0 reported; target effect unverified' if result['exit_code']==0 else 'invocation error reported; partial effects possible')
    text=call.get('result_input','')
    pending=bool(call.get('backgrounded')) or bool(re.search(r'(?:running in background|moved to the background|Script running with cell ID|Process running with session ID)',text))
    if isinstance(envelope,dict):
        for item in envelope.get('content',[]) if isinstance(envelope.get('content'),list) else []:
            nested=decode(item.get('text',''))
            if isinstance(nested,dict) and nested.get('session_id') is not None and nested.get('exit_code') is None:pending=True
    if pending:return 'accepted / running in outer call; action completion unconfirmed'
    if call.get('status')=='error' or call.get('exit_code') not in (None,0):return 'error reported; partial effects possible'
    if call.get('status')=='no_result':return 'no result'
    return 'outer call completion reported; action effect unverified'


def python_effects(command,state):
    """Small AST experiment: literal Path read/write/chmod, no imports or eval.

    Only a literal sha256(...).hexdigest() chain is called a hash. Other
    transformations remain unspecified. This is static source evidence.
    """
    blocks=[]
    lines=command.splitlines()
    i=0
    while i<len(lines):
        m=re.fullmatch(r"\s*python(?:3(?:\.\d+)?)?\s+-\s+<<\s*['\"]?(\w+)['\"]?\s*",lines[i])
        if m:
            end=i+1
            while end<len(lines) and lines[end]!=m[1]:end+=1
            if end<len(lines):blocks.append('\n'.join(lines[i+1:end]));i=end
            break  # caller supplies the suffix beginning at this Python command
        i+=1
    output=[]
    for block in blocks:
        try:tree=ast.parse(block)
        except (SyntaxError,ValueError):continue
        paths={};sources={}
        def path_of(node):
            if isinstance(node,ast.Name):return paths.get(node.id)
            if isinstance(node,ast.Call) and isinstance(node.func,ast.Name) and node.func.id=='Path' and len(node.args)==1 and isinstance(node.args[0],ast.Constant) and isinstance(node.args[0].value,str):return node.args[0].value
            return None
        def reads(node):
            return [path_of(n.func.value) for n in ast.walk(node) if isinstance(n,ast.Call) and isinstance(n.func,ast.Attribute) and n.func.attr in {'read_text','read_bytes'} and path_of(n.func.value)]
        for node in tree.body:
            if not isinstance(node,(ast.Assign,ast.Expr)):continue
            if isinstance(node,ast.Assign) and len(node.targets)==1 and isinstance(node.targets[0],ast.Name):
                name=node.targets[0].id;path=path_of(node.value)
                if path:paths[name]=path
                else:paths.pop(name,None)
                sources[name]=reads(node.value)
            for call in ast.walk(node):
                if not isinstance(call,ast.Call) or not isinstance(call.func,ast.Attribute):continue
                path=path_of(call.func.value)
                if not path:continue
                if call.func.attr in {'write_text','write_bytes'} and call.args:
                    payload=call.args[0];flow=set(reads(payload))
                    for ref in ast.walk(payload):
                        if isinstance(ref,ast.Name):flow.update(sources.get(ref.id,[]))
                    hash_calls=[n for n in ast.walk(payload) if isinstance(n,ast.Call) and isinstance(n.func,ast.Attribute) and n.func.attr=='hexdigest' and isinstance(n.func.value,ast.Call) and isinstance(n.func.value.func,ast.Attribute) and isinstance(n.func.value.func.value,ast.Name) and n.func.value.func.value.id=='hashlib' and n.func.value.func.attr=='sha256']
                    # Claim a credential hash only for the narrow direct JSON
                    # payload, with no additional references to source variables
                    # outside the hash expression.
                    sensitive_refs=[n for n in ast.walk(payload) if isinstance(n,ast.Subscript) and isinstance(n.slice,ast.Constant) and n.slice.value in {'api_key','token','password','secret'}]
                    hashed_nodes={id(n) for h in hash_calls for n in ast.walk(h)}
                    hashed=bool(sensitive_refs) and all(id(n) in hashed_nodes for n in sensitive_refs)
                    output.append(dict(action='credential hash written' if hashed else 'Python file write',targets=[state.target(path)],sources=[state.target(s) for s in sorted(flow)],transformation='SHA-256 hexdigest in static source; execution unverified' if hashed else 'unspecified; inspect source',command=block,cwd=state.cwd,cwd_basis=state.basis,execution='static Python AST evidence; effect unverified'))
                elif call.func.attr=='chmod' and len(call.args)==1 and isinstance(call.args[0],ast.Constant) and isinstance(call.args[0].value,int):
                    output.append(dict(action='change permissions',targets=[state.target(path)],mode=oct(call.args[0].value),command=block,cwd=state.cwd,cwd_basis=state.basis,execution='static Python AST evidence; effect unverified'))
    return output


def enrich_call(call, commands, parsed):
    actions, gaps, creations = [], [], []
    for n, shell in enumerate(commands):
        state=State(shell['cwd'],call.get('location') or '',shell['cwd_basis'])
        for pos,segment in enumerate(parsed[n]):
            raw,operator=segment['Command'],segment['Operator']
            tokens=lex(raw,state)
            # Assignments and cd are bounded to this invocation. Control flow is
            # lexical evidence only; no cross-call shell state is assumed.
            conditional=operator in {'&&','||'} or state.ambiguous
            if re.search(r'\b(?:if|for|while|until|case)\b', raw.split(' ',1)[0]) or re.match(r'\w+\s*\(\)\s*\{',raw):
                state.ambiguous=True
            assignments=tokens[1:] if tokens[:1]==['export'] else tokens
            if assignments and all(re.match(r'^[A-Za-z_]\w*=',x) for x in assignments):
                for item in assignments:
                    key,value=item.split('=',1)
                    if not re.search(r'[$`()]',value) and not state.ambiguous:
                        state.env[key]=value;state.bindings[key]=raw
                    else:state.env.pop(key,None)
                continue
            if tokens[:1]==['cd']:
                target=state.target(tokens[1]) if len(tokens)==2 else dict(resolution='unresolved')
                state.cwd=target.get('identity','') if target['resolution']=='resolved' else ''
                state.env['PWD']=state.cwd;state.basis='cd in same invocation (success assumed)'
                continue
            if tokens[:1]==['mkdir']:
                creations.extend(dict(target=state.target(t),position=pos,command=raw) for t in study.operands(tokens[1:]))
            found,missing=classify_segment(raw,state)
            if re.match(r'^python(?:3(?:\.\d+)?)?\s+-\s+<<',raw):
                offset=shell['command'].find(raw)
                if offset>=0:
                    found.extend(python_effects(shell['command'][offset:],state))
            gaps.extend(missing)
            for action in found:
                action.update(id=call['id']+':'+str(n)+':'+str(pos)+':'+str(len(actions)),call_id=call['id'],invocation=n,position=pos,operator=operator,execution=action.get('execution') or ('conditional / lexical presence' if conditional or state.ambiguous else 'invoked; effect unverified'),bindings=dict(state.bindings),outcome=invocation_outcome(call,shell['command'],len(commands)))
                for t in action['targets']:
                    prior=[c for c in creations if c['position']<pos and c['target']['identity']==t['identity'] and t['resolution']=='resolved']
                    if prior:t['lifecycle']='mkdir appears earlier in same invocation (creation success unverified)'
                actions.append(action)
    if call.get('tool_category')=='edit' and call.get('path_absolute'):
        state=State(call.get('location') or '',call.get('location') or '')
        actions.append(dict(id=call['id']+':edit',call_id=call['id'],action='file edit',targets=[state.target(call['path_absolute'])],command=call.get('file_path') or '',cwd=state.cwd,cwd_basis='ledger absolute path',execution='tool invocation; effect unverified'))
    if call.get('tool_name')=='mcp__codex_apps__github_update_pull_request':
        value=decode(call['call_input'])
        value=value.get('input') if isinstance(value,dict) else value
        # Exact observed tool identity, literal resource fields only. A Code
        # Mode string is read as syntax, never evaluated.
        if isinstance(value,str):
            repo=re.search(r'\brepository_full_name\s*:\s*('+JS_STRING+')',value)
            pr=re.search(r'\bpr_number\s*:\s*(\d+)',value)
            if repo and pr:
                state=State(call.get('location') or '',call.get('location') or '')
                target=json.loads(repo[1])+' / pull '+pr[1]
                actions.append(dict(id=call['id']+':mcp',call_id=call['id'],action='update PR (MCP)',targets=[state.target(target,'GitHub resource')],command='MCP update_pull_request; resource fields recovered from input; inspect original for changed fields',cwd=state.cwd,cwd_basis='literal MCP resource fields',execution='tool invocation; effect unverified'))
    for action in actions:
        action.update(repository=call['repository'],conversation_id=call['conversation_id'],workspace_id=call['workspace_id'],started_at=call['started_at'],reader_path=call['reader_path'],authorization='unknown',result_status=call['status'],exit_code=call['exit_code'])
        action.setdefault('outcome',invocation_outcome(call,'',len(commands)))
        for target in action['targets']:
            # A PID can be reused and a shell variable can change between
            # invocations. They are selectors, not global resource identities.
            target['group_identity']=target['identity']
            if target['resolution']=='unresolved' or target['scope']=='process':
                target['group_identity']+=' [call '+call['id']+']'
    return actions,sorted(set(gaps))


def family(action):
    name=action['action']
    if name.startswith('credential'):return 'credential movement / access'
    if name in {'rebase','force push / delete ref','hard reset','delete local branch','discard stash','rewrite repository history','push branch','publish tags','restore working files'}:return 'Git history / publication'
    if 'PR' in name or name.endswith((' pr',' issue',' release')) or 'GitHub' in name:return 'GitHub changes'
    if 'process' in name:return 'process signals'
    if name in {'delete','remove checkout','delete untracked files','destination pruning','delete cloud objects'}:return 'deletions'
    if name=='database mutation':return 'database changes'
    if 'permissions' in name:return 'permissions'
    if 'API' in name or 'HTTP' in name:return 'HTTP changes'
    return 'other changes'


def resource_key(action):
    if family(action)=='Git history / publication':return action.get('repository_target',{}).get('identity') or action['cwd']
    if family(action)=='credential movement / access' and action.get('destination'):
        dest=action['destination']
        return dest['identity']+(' [call '+action['call_id']+']' if dest['resolution']=='unresolved' else '')
    return '|'.join(t.get('group_identity',t['identity']) for t in action['targets']) or action['cwd']


def action_order(action):
    return (action['started_at'],action['call_id'],action.get('invocation',0),action.get('position',0),action['id'])


def make_episodes(actions):
    grouped=defaultdict(list)
    for action in actions:
        grouped[(action['conversation_id'],family(action),resource_key(action))].append(action)
    episodes=[]
    for (cid,kind,key),items in grouped.items():
        chunks=[]
        for item in sorted(items,key=action_order):
            # Split disconnected activity; same resource+conversation alone is
            # an association, never proof of intent or causality.
            if not chunks or (study.datetime.fromisoformat(item['started_at'].replace('Z','+00:00'))-study.datetime.fromisoformat(chunks[-1][0]['started_at'].replace('Z','+00:00'))).total_seconds()>6*3600:chunks.append([])
            chunks[-1].append(item)
        for chunk in chunks:
            verbs=Counter(a['action'] for a in chunk)
            relations=[]
            if 'rebase' in verbs and 'force push / delete ref' in verbs:
                first_rebase=next(i for i,a in enumerate(chunk) if a['action']=='rebase')
                if any(a['action']=='force push / delete ref' for a in chunk[first_rebase+1:]):relations.append('Rebase followed by force push in the same repository and conversation')
            episodes.append(dict(id='episode-'+hashlib.sha256((cid+kind+key+chunk[0]['id']).encode()).hexdigest()[:16],family=kind,subject=key,repository=chunk[0]['repository'],conversation_id=cid,actions=[a['id'] for a in chunk],calls=len({a['call_id'] for a in chunk}),verbs=dict(verbs),relations=relations,start=chunk[0]['started_at'],end=chunk[-1]['started_at']))
    # Add explicit cross-family associations around a credential destination.
    # Only exact/descendant resolved paths are joined, never common basenames.
    for episode in list(episodes):
        if episode['family']!='credential movement / access':continue
        base=[a for a in actions if a['id'] in episode['actions']]
        destinations=[a['destination']['identity'] for a in base if a.get('destination') and a['destination']['resolution']=='resolved']
        if not destinations:continue
        neighbors=[a for a in actions if a['conversation_id']==episode['conversation_id'] and a['started_at']>=episode['start'] and a['started_at']<=episode['end'] and a['action'] in {'change permissions','delete'} and any(t['resolution']=='resolved' and any(t['identity']==d or t['identity'].startswith(d+'/') for d in destinations) for t in a['targets'])]
        if neighbors:
            combined=sorted(base+neighbors,key=action_order)
            episode.update(actions=[a['id'] for a in combined],calls=len({a['call_id'] for a in combined}),verbs=dict(Counter(a['action'] for a in combined)),relations=['Credential destination also targeted by '+', '.join(sorted({a['action'] for a in neighbors}))+'; no disclosure or verified cleanup inferred'])
    return sorted(episodes,key=lambda e:(-bool(e['relations']),-e['calls'],e['start']))


def run(args):
    out=study.outside_git(args.out)
    out.mkdir(parents=True,mode=0o700,exist_ok=True)
    if not (out/'sources.jsonl').exists():cache_sources(Path(args.study).expanduser(),out)
    sources=study.jsonl_read(out/'sources.jsonl')
    all_commands=[];recoveries={}
    for call in sources:
        commands=recover(call['call_input'],call.get('location') or '')
        if not commands and call.get('command') and not call['command'].endswith('…') and call['tool_name'].lower() in study.EXECUTION_TOOLS:
            commands=[dict(command=call['command'],cwd=call.get('location') or '',cwd_basis='workspace fallback; ledger command')]
        recoveries[call['id']]=commands
        all_commands.extend(c['command'] for c in commands)
    parsed=iter(split_many(all_commands));actions=[];records=[]
    for call in sources:
        commands=recoveries[call['id']]
        enriched,gaps=enrich_call(call,commands,[next(parsed) for _ in commands])
        actions.extend(enriched)
        records.append(dict(id=call['id'],repository=call['repository'],reader_path=call['reader_path'],started_at=call['started_at'],status=call['status'],exit_code=call['exit_code'],audit_miss=call['audit_miss'],original_hits=call['v1_hits'],recovered_commands=len(commands),gaps=gaps,action_ids=[a['id'] for a in enriched],result_excerpt=call['result_input'][:18000]))
    episodes=make_episodes(actions)
    cohort=json.loads((out/'cohort.json').read_text())
    summary=dict(cohort=cohort,actions=len(actions),calls_with_actions=len({a['call_id'] for a in actions}),episodes=len(episodes),target_mentions=sum(len(a['targets']) for a in actions),targets_by_scope=dict(Counter(t['scope'] for a in actions for t in a['targets'])),targets_by_resolution=dict(Counter(t['resolution'] for a in actions for t in a['targets'])),actions_by_family=dict(Counter(family(a) for a in actions)),calls_without_commands=sum(not r['recovered_commands'] for r in records),splitter_sha256=hashlib.sha256(SHELL_SOURCE.read_bytes()).hexdigest(),enrichment_sha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),support_sha256={name:hashlib.sha256(Path(__file__).with_name(name).read_bytes()).hexdigest() for name in ['study.py','rules.json','briefing.html','annotations.py']})
    study.jsonl_write(out/'actions.jsonl',actions);study.jsonl_write(out/'episodes.jsonl',episodes);study.jsonl_write(out/'evidence.jsonl',records);study.json_write(out/'summary.json',summary)
    report(out,summary,actions,episodes,records)
    print(json.dumps(summary,indent=2))


def report(out,summary,actions,episodes,records):
    data=dict(summary=summary,actions=actions,episodes=episodes,records=records)
    template=(Path(__file__).with_name('briefing.html')).read_text()
    payload=json.dumps(study.scrub(data),ensure_ascii=False).replace('<','\\u003c').replace('\u2028','\\u2028').replace('\u2029','\\u2029')
    document=template.replace('/* DATA */',payload)
    study.private_write(out/'briefing.html',document)
    import annotations
    study.private_write(out/'annotatable.html',annotations.inject(document))


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--study',required=True);parser.add_argument('--out',required=True)
    run(parser.parse_args())
