#!/usr/bin/env python3
"""Offline, deterministic tool-call research. No Pharos service hooks or model calls.

scan --catalog BACKUP --snapshot-copy --out STUDY --start DATE --end DATE
report --study STUDY [--labels labels.jsonl]
Only the existing tool_commands ledger splits shell syntax. Never execute input.
"""
import argparse
from collections import Counter
from datetime import datetime, timezone
import hashlib
import heapq
import html
import ipaddress
import json
import os
from pathlib import Path
import re
import shlex
import sqlite3
import sys
from urllib.parse import quote, urlsplit
from zoneinfo import ZoneInfo

RULES_PATH = Path(__file__).with_name('rules.json')
RULES = json.loads(RULES_PATH.read_text())
KINDS = {v[0] for v in RULES['rules'].values()}
LEVELS = {'low': 0, 'routine': 1, 'caution': 2, 'high': 3}
WRAPPERS = {'sudo': {'-u', '-g', '-h', '-p'}, 'env': {'-u', '--unset', '-C', '--chdir'},
            'timeout': set(), 'gtimeout': set(), 'nice': {'-n'}, 'time': {'-o', '-f'},
            'command': set(), 'exec': set(), 'nohup': set(), 'caffeinate': set(), 'xcrun': {'--sdk', '--toolchain'}}
GLOBAL_VALUES = {'git': {'-C', '-c', '--git-dir', '--work-tree'}, 'gh': {'-R', '--repo'},
                 'npm': {'--prefix', '-w', '--workspace'}, 'pnpm': {'-C', '--dir', '--filter'},
                 'yarn': {'--cwd'}, 'docker': {'--context', '-H'}}
SENSITIVE_PATH = re.compile(r'(^|/)(?:\.env(?:\.[^/]*)?|credentials(?:\.json)?|application_default_credentials\.json|id_(?:rsa|ed25519)|\.npmrc|\.netrc|\.pypirc)$|/\.aws/credentials|/\.config/gcloud/', re.I)
PROFILE = re.compile(r'(^|/)(?:\.(?:zshrc|zprofile|bashrc|bash_profile|profile)|authorized_keys|sudoers)$|^/(?:etc|Library/LaunchDaemons)/')
# Match whole quoted values (including whitespace and escapes), not just the
# first token. JSON keys have a closing quote before the separator.
SECRET_KEY = r'(?:authorization|password|passwd|api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|session[_-]?token|client[_-]?secret|private[_-]?key|aws[_-]?secret[_-]?access[_-]?key|token|secret)'
SECRET = re.compile(r"(?i)(\b" + SECRET_KEY + r"[\"']?\s*[:=]\s*(?:(?:bearer|basic)\s+)?)(\"(?:\\.|[^\"\\])*\"|'(?:\\.|[^'\\])*'|[^\s\"'&,;}]+)")
SENSITIVE_KEY = re.compile(r'(?i)^' + SECRET_KEY + r'$')
TOKEN = re.compile(r'\b(?:sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{16,}|AKIA[A-Z0-9]{16})\b')
OPAQUE = {'python', 'python3', 'node', 'ruby', 'perl', 'bash', 'sh', 'zsh', 'osascript', 'eval', 'xargs', 'find', 'ssh'}
EXECUTION_TOOLS = {'bash', 'shell', 'exec_command', 'exec', 'local_shell', 'run', 'run_command',
                   'execute_command', 'container.exec'}


def redact(text):
    text = text or ''
    # Structured inputs can contain escaped nested JSON, multiline private
    # keys, or non-string credential values. Scrub them before serialization.
    try:
        value = json.loads(text)
    except (ValueError, TypeError):
        value = None
    if isinstance(value, (dict, list)):
        return json.dumps(scrub(value), ensure_ascii=False)
    if isinstance(value, str):
        return json.dumps(redact(value), ensure_ascii=False)
    def replace(match):
        value = match[2]
        quote = value[0] if value[0] in '\"\'' else ''
        return match[1] + quote + '[REDACTED]' + quote
    text = SECRET.sub(replace, text)
    text = TOKEN.sub('[REDACTED]', text)
    return re.sub(r'(https?://)[^/\s:@]+:[^/\s@]+@', r'\1[REDACTED]@', text)


def words(text):
    try:
        return shlex.split(text, comments=True)
    except ValueError:
        return []


def invocation(tokens):
    """Return executable arguments and wrappers; quoted operands stay operands."""
    args, wrappers = list(tokens), []
    while args:
        if re.match(r'^[A-Za-z_][A-Za-z_0-9]*=', args[0]) or args[0] in {'then', 'do', 'else', '!', '{', '('}:
            args.pop(0)
            continue
        name = os.path.basename(args[0])
        if name not in WRAPPERS:
            break
        wrappers.append(name)
        args.pop(0)
        while args and args[0].startswith('-'):
            flag = args.pop(0)
            if flag in WRAPPERS[name] and args:
                args.pop(0)
        if name in {'timeout', 'gtimeout'} and args:
            args.pop(0)
    return args, wrappers


def action_args(program, args):
    args = list(args)
    while args and args[0].startswith('-') and args[0] != '--':
        flag = args.pop(0)
        if flag in GLOBAL_VALUES.get(program, set()) and args:
            args.pop(0)
    return args


def has_flag(args, *flags):
    return any(a.split('=', 1)[0] in flags for a in args)


def short_flag(args, flag):
    return any(a.startswith('-') and not a.startswith('--') and flag in a[1:] for a in args)


def operands(args, value_flags=()):
    out, skip, literal = [], False, False
    for arg in args:
        if skip:
            skip = False
        elif literal:
            out.append(arg)
        elif arg == '--':
            literal = True
        elif arg in value_flags:
            skip = True
        elif not arg.startswith('-'):
            out.append(arg)
    return out


def path_context(targets, cwd=''):
    """Only all-literal known scratch targets lower severity; never a $variable."""
    scopes = []
    for target in targets:
        if not target or re.search(r'[$`*?{}]', target) or '://' in target:
            scopes.append('unknown')
            continue
        absolute = os.path.normpath(target if target.startswith(('/', '~')) else os.path.join(cwd, target))
        if absolute.startswith(('/tmp/', '/private/tmp/', '/var/folders/', '/private/var/folders/')) or '/.context/' in absolute or absolute == '.context' or absolute.startswith('.context/'):
            scopes.append('scratch')
        else:
            scopes.append('other')
    return 'scratch' if scopes and all(s == 'scratch' for s in scopes) else 'unknown_or_persistent'


def sql_mutates(sql):
    # Ignore SQL comments and literal values (a SELECT of 'DROP TABLE' is data).
    clean = re.sub(r"'(?:''|[^'])*'|\"(?:\"\"|[^\"])*\"|--[^\n]*|/\*.*?\*/", ' ', sql, flags=re.S)
    for statement in clean.split(';'):
        if re.match(r'^\s*(?:EXPLAIN|SELECT)\b', statement, re.I):
            continue
        if re.search(r'\b(?:DROP\s+(?:TABLE|DATABASE|SCHEMA|INDEX)|TRUNCATE\b|DELETE\s+FROM|UPDATE\s+\w+\s+SET|ALTER\s+TABLE)\b', statement, re.I):
            return True
    return False


def command_hits(segment, cwd=''):
    raw = segment.get('command') or ''
    if raw.endswith('…'):
        return [], ['ledger_segment_truncated']
    tokens = words(raw)
    args, wrappers = invocation(tokens)
    if not args:
        return [], ['unparsed_segment']
    p, rest = os.path.basename(args[0]), args[1:]
    a = action_args(p, rest)
    sub = a[0] if a else ''
    hits, gaps = [], []

    def add(rule, evidence, targets=(), dry=False):
        kind, level, note = RULES['rules'][rule]
        context = path_context(targets, cwd)
        hits.append(dict(rule=rule, kind=kind, severity=level,
                         effective_severity='low' if dry or (context == 'scratch' and rule in {'delete', 'outside_write', 'permission'}) else level,
                         evidence=evidence, targets=list(targets), context=context, dry_run=dry, reversibility=note))

    if 'sudo' in wrappers or p == 'sudo':
        add('system', 'Privileged command')
    if p in {'rm', 'rmdir', 'unlink', 'shred', 'srm', 'trash'}:
        targets = operands(rest)
        if targets:
            add('delete', p + ' removes filesystem entries', targets)
    if p == 'find' and '-delete' in rest:
        add('delete', 'find -delete', operands(rest[:rest.index('-delete')]))
    if p == 'rsync' and any(x.startswith('--delete') for x in rest):
        targets = operands(rest, {'--exclude', '--include', '-e', '--filter'})
        add('delete', 'rsync destination pruning', targets[-1:], has_flag(rest, '--dry-run') or short_flag(rest, 'n'))
    if p == 'git':
        flags = a[1:]
        if sub == 'push':
            dry = has_flag(flags, '--dry-run') or short_flag(flags, 'n')
            if has_flag(flags, '--force', '--force-with-lease', '--force-if-includes', '--delete') or short_flag(flags, 'f') or any(x.startswith(('+', ':')) for x in flags):
                add('history', 'Force push or remote ref deletion; lease protection recorded in evidence', dry=dry)
            elif has_flag(flags, '--tags', '--follow-tags') or any('refs/tags/' in x for x in flags):
                add('publish', 'Tag push may trigger publication', dry=dry)
            else:
                add('routine', 'Ordinary git push (a short tag ref cannot be distinguished from a branch)', dry=dry)
        if (sub == 'reset' and '--hard' in flags) or (sub == 'branch' and short_flag(flags, 'D')) or sub in {'filter-branch', 'filter-repo'} or (sub == 'lfs' and flags[:2] == ['migrate', 'import']):
            add('history', 'Destructive local history/ref rewrite')
        if sub == 'rebase' and not has_flag(flags, '--abort', '--quit', '--show-current-patch'):
            add('history', 'Rebase; whether the branch is shared is unknown')
        if sub == 'clean' and (short_flag(flags, 'f') or '--force' in flags):
            add('delete', 'git clean deletes untracked files', dry=short_flag(flags, 'n') or '--dry-run' in flags)
        if sub == 'worktree' and flags and flags[0] == 'remove':
            add('delete', 'git worktree remove deletes a checkout', operands(flags[1:]))
        if sub == 'stash' and flags and flags[0] in {'drop', 'clear'}:
            add('history', 'Discard saved uncommitted work')
        if sub in {'checkout', 'restore'} and (has_flag(flags, '--force') or short_flag(flags, 'f') or '--' in flags or sub == 'restore'):
            add('history', 'Working tree restore may discard uncommitted content')
    if p == 'gh':
        second = a[1] if len(a) > 1 else ''
        if sub == 'release' and second in {'create', 'upload', 'edit', 'delete', 'delete-asset'}:
            add('publish', 'GitHub release mutation')
        elif sub in {'pr', 'issue'} and second in {'create', 'edit', 'comment', 'close', 'reopen', 'merge', 'review'}:
            add('routine' if second != 'merge' else 'publish', 'GitHub ' + sub + ' ' + second)
        elif sub == 'api':
            method = next((a[i + 1].upper() for i, x in enumerate(a[:-1]) if x in {'-X', '--method'}), '')
            method = next((x.split('=', 1)[1].upper() for x in a if x.startswith('--method=')), method)
            fields = has_flag(a, '-f', '-F', '--field', '--raw-field', '--input') or any(x.startswith(('-f', '-F')) and len(x) > 2 for x in a)
            if method not in {'GET', 'HEAD', 'OPTIONS'} and (method or fields):
                add('publish', 'GitHub API write (field arguments imply POST)')
        elif sub == 'auth' and second in {'token', 'login', 'setup-git', 'refresh', 'logout'}:
            add('credential', 'GitHub authentication or token access')
    if p in {'brew', 'apt', 'apt-get', 'dnf', 'yum', 'pacman', 'port'} and sub in {'install', 'upgrade', 'remove', 'uninstall', 'reinstall', 'tap', 'update'}:
        add('install', 'System package manager ' + sub)
    if p in {'npm', 'pnpm', 'yarn', 'bun', 'pip', 'pip3', 'uv'}:
        global_install = has_flag(rest, '--global', '--system') or short_flag(rest, 'g') or (p == 'uv' and a[:2] == ['tool', 'install'])
        if global_install and sub in {'install', 'i', 'add', 'uninstall', 'remove', 'tool'}:
            add('install', 'Global/system package change', dry=has_flag(rest, '--dry-run'))
        if sub == 'publish':
            add('publish', 'Package publication', dry=has_flag(rest, '--dry-run'))
    if p in {'kill', 'pkill', 'killall'} and not has_flag(rest, '-l', '--list', '--help') and not has_flag(rest, '-0') and '--signal=0' not in rest and not (len(rest) > 1 and rest[:2] in [['-s', '0'], ['--signal', '0']]):
        add('process', 'Process signal; targets may include unfinished work')
    if p in {'chmod', 'chown', 'chgrp', 'setfacl'}:
        add('permission', 'Filesystem permissions/ownership change', operands(rest)[1:])
    if p in {'defaults', 'launchctl', 'systemctl', 'diskutil', 'hdiutil', 'shutdown', 'reboot'}:
        read_only = {'read', 'read-type', 'help', 'list', 'info', 'status', 'show', 'print', 'print-disabled', 'is-active', 'is-enabled', 'list-units', 'list-unit-files', 'imageinfo', 'verify'}
        if p == 'diskutil' and sub == 'apfs' and len(a) > 1 and a[1] in {'list', 'listSnapshots', 'listVolumeGroups'}:
            read_only.add('apfs')
        if p not in {'shutdown', 'reboot'} and sub not in read_only and sub and not has_flag(rest, '--help', '-h'):
            add('system', p + ' ' + sub)
        if p in {'shutdown', 'reboot'} and not has_flag(rest, '--help', '-h'):
            add('system', 'Machine shutdown/restart')
    if p == 'security' and sub in {'find-generic-password', 'find-internet-password', 'delete-generic-password', 'delete-internet-password', 'import', 'export', 'add-generic-password', 'add-internet-password', 'unlock-keychain', 'create-keychain'}:
        add('credential', 'Keychain credential operation (not merely listing identities)')
    if p in {'cat', 'head', 'tail', 'cp', 'rsync', 'printenv'}:
        if any(SENSITIVE_PATH.search(x) for x in operands(rest)) or (p == 'printenv' and any(re.search(r'TOKEN|SECRET|PASSWORD|API_KEY', x) for x in rest)):
            add('credential', 'Sensitive credential file or environment access')
    if p in {'sqlite3', 'psql', 'mysql', 'mariadb', 'duckdb'}:
        if any(sql_mutates(x) for x in rest):
            add('data', 'Database mutation in an explicit SQL argument')
    if (p in {'prisma', 'npx'} and 'migrate' in a and any(x in a for x in ['deploy', 'reset', 'dev'])) or (p == 'alembic' and sub in {'upgrade', 'downgrade'}) or (p == 'terraform' and sub in {'apply', 'destroy'}):
        add('data' if p != 'terraform' else 'spend', 'Migration/infrastructure mutation; production vs fixture unresolved')
    if p in {'gsutil', 'gcloud', 'aws'}:
        if (p == 'gsutil' and sub == 'rm') or (p == 'gcloud' and a[:2] == ['storage', 'rm']) or (p == 'aws' and a[:2] == ['s3', 'rm']):
            add('delete', 'Cloud object deletion', dry=has_flag(rest, '--dryrun', '--dry-run'))
        elif (p == 'gcloud' and a[:2] in [['run', 'deploy'], ['builds', 'submit']]) or (p == 'aws' and 'create' in sub):
            add('spend', 'Cloud deployment/build can incur charges')
    if p == 'curl':
        method = next((rest[i + 1].upper() for i, x in enumerate(rest[:-1]) if x in {'-X', '--request'}), '')
        method = next((x[2:].upper() for x in rest if x.startswith('-X') and len(x) > 2), method)
        method = next((x.split('=', 1)[1].upper() for x in rest if x.startswith('--request=')), method)
        data = has_flag(rest, '-d', '--data', '--data-raw', '--data-binary', '--data-urlencode', '--json', '-F', '--form', '-T', '--upload-file') or any(x.startswith(('-d', '-F', '-T')) and len(x) > 2 for x in rest)
        if method not in {'GET', 'HEAD', 'OPTIONS'} and not has_flag(rest, '--get', '--head') and not short_flag(rest, 'G') and not short_flag(rest, 'I') and (method or data):
            targets = [x for x in rest if x.startswith(('http://', 'https://'))]
            local = targets and all(is_local_url(x) for x in targets)
            add('publish', 'HTTP mutation; loopback target' if local else 'HTTP write; effect depends on endpoint', targets)
            if local:
                hits[-1]['effective_severity'] = 'caution'
    # A quoted '>' is data; only unquoted top-level operators redirect output.
    for target in redirection_targets(raw):
        if PROFILE.search(target):
            add('system', 'Shell profile/system configuration write', [target])
        elif re.search(r'[$`*?{}]', target):
            gaps.append('dynamic_redirection')
        elif target.startswith(('/', '~/')) and target != '/dev/null' and path_context([target], cwd) != 'scratch':
            normalized = os.path.normpath(target)
            in_repo = cwd and normalized.startswith(cwd.rstrip('/') + '/')
            if not in_repo:
                add('outside_write', 'Output redirection outside known working directory', [target])
    if p in OPAQUE or p.startswith('python3.') or p.startswith('$'):
        gaps.append('opaque_or_dynamic_command')
    return hits, gaps


def redirection_targets(text):
    quote_char, depth, escaped = '', 0, False
    i = 0
    while i < len(text):
        ch = text[i]
        if escaped:
            escaped = False
        elif ch == "\\" and quote_char != "'":
            escaped = True
        elif quote_char:
            if ch == quote_char:
                quote_char = ''
        elif ch in "'\"`":
            quote_char = ch
        elif ch == '(':
            depth += 1
        elif ch == ')' and depth:
            depth -= 1
        elif ch == '>' and not depth:
            i += 1
            while i < len(text) and text[i] in '>|':
                i += 1
            # FD duplication has no path target.
            if i < len(text) and text[i] == '&':
                continue
            target = words(text[i:])
            if target:
                yield target[0]
            continue
        i += 1


def is_local_url(url):
    try:
        host = urlsplit(url).hostname
        if host == 'localhost':
            return True
        return ipaddress.ip_address(host).is_loopback
    except (ValueError, TypeError):
        return False


def file_hits(call):
    if call.get('tool_category') != 'edit':
        return [], []
    target, scope = call.get('path_absolute') or call.get('file_path'), call.get('path_scope')
    if not target:
        return [], ['edit_path_unavailable']
    rules = []
    if SENSITIVE_PATH.search(target):
        rules.append('credential')
    if PROFILE.search(target):
        rules.append('system')
    if scope in {'external', 'agent_home'}:
        rules.append('outside_write')
    hits = []
    for rule in rules:
        kind, severity, note = RULES['rules'][rule]
        hits.append(dict(rule=rule, kind=kind, severity=severity, effective_severity=severity,
                         evidence='File edit/write: ' + target, targets=[target], context=scope or 'unknown', dry_run=False, reversibility=note))
    return hits, []


# Exact observed/API tool identities: no broad "write" substring on arbitrary MCP names.
MCP_RULES = {'mcp__codex_apps__github_update_pull_request': 'routine',
             'mcp__codex_apps__github_create_pull_request': 'routine',
             'mcp__codex_apps__github_merge_pull_request': 'publish',
             'mcp__codex_apps__github_create_issue': 'routine',
             'mcp__codex_apps__github_add_comment_to_issue': 'routine',
             'mcp__codex_apps__sites_deploy_site_version': 'publish',
             'mcp__codex_apps__sites_deploy_private_site_version': 'publish'}


def classify(call, segments):
    hits, gaps = file_hits(call)
    if call.get('tool_name') and call['tool_name'].lower() not in EXECUTION_TOOLS and segments:
        # Results of polling/wait calls can repeat command executions. They are
        # not evidence that this call launched those commands a second time.
        gaps.append('non_execution_call_with_commands')
        segments = []
    if call.get('tool_name') in MCP_RULES:
        rule = MCP_RULES[call['tool_name']]
        kind, severity, note = RULES['rules'][rule]
        hits.append(dict(rule=rule, kind=kind, severity=severity, effective_severity=severity,
                         evidence='MCP: ' + call['tool_name'], targets=[], context='unknown', dry_run=False, reversibility=note))
    elif call.get('mcp_server'):
        gaps.append('mcp_semantics_not_classified')
    cwd = call.get('location') or ''
    for segment in segments:
        found, missing = command_hits(segment, cwd)
        for hit in found:
            hit.update(position=segment['position'], operator=segment.get('operator') or '', command=redact(segment.get('command')),
                       segment_exit_code=segment.get('exit_code'))
        hits.extend(found)
        gaps.extend(missing)
        tokens, _ = invocation(words(segment.get('command') or ''))
        if tokens and tokens[0] == 'cd' and len(tokens) == 2:
            cwd = os.path.normpath(os.path.join(cwd, tokens[1])) if not re.search(r'[$`]', tokens[1]) else ''
    if call.get('command') and not segments and 'non_execution_call_with_commands' not in gaps:
        gaps.append('shell_segments_unavailable')
    # Never infer per-segment success or execution from the final compound-call exit.
    outcome = 'no_result' if call.get('status') == 'no_result' else 'error_possible_partial' if call.get('status') == 'error' or call.get('exit_code') not in (None, 0) else 'success_reported'
    for hit in hits:
        hit['outcome'] = outcome
    return hits, sorted(set(gaps))


def private_write(path, text):
    path = Path(path)
    with open(path, 'w', encoding='utf-8', opener=lambda p, f: os.open(p, f, 0o600)) as stream:
        stream.write(text)


def json_write(path, data):
    private_write(path, json.dumps(scrub(data), ensure_ascii=False, indent=2) + '\n')


def scrub(value):
    if isinstance(value, str):
        return redact(value)
    if isinstance(value, dict):
        def sensitive(key, item):
            if not SENSITIVE_KEY.fullmatch(str(key)):
                return False
            # This research format also has an authorization *judgment*.
            # Preserve only its explicit enum/schema, never arbitrary headers.
            if str(key).lower() == 'authorization':
                if isinstance(item, str) and item in {'unknown', 'explicitly_requested'}:
                    return False
                if isinstance(item, dict) and set(item) <= {'status', 'evidence_ids'} and item.get('status') in {'unknown', 'explicitly_requested'}:
                    return False
            return True
        return {k: '[REDACTED]' if sensitive(k, v) else scrub(v) for k, v in value.items()}
    if isinstance(value, list):
        return [scrub(v) for v in value]
    return value


def jsonl_write(path, rows):
    private_write(path, ''.join(json.dumps(scrub(row), ensure_ascii=False) + '\n' for row in rows))


def jsonl_read(path):
    # JSONL records are delimited by LF. Unicode line/paragraph separators are
    # valid characters inside JSON strings and must not split a record.
    return [json.loads(line) for line in Path(path).read_text().split('\n') if line.strip()]


def outside_git(path):
    path = Path(path).expanduser().resolve()
    if any((p / '.git').exists() for p in (path, *path.parents)):
        raise ValueError('Study data/results must be outside git: ' + str(path))
    return path


def fingerprint(path):
    return [list((p.stat().st_size, p.stat().st_mtime_ns)) if p.exists() else None
            for p in [path, Path(str(path) + '-wal'), Path(str(path) + '-shm')]]


def open_snapshot(path):
    path = Path(path).expanduser().resolve(strict=True)
    if any((path.parent / lock).exists() for lock in ['.pharos-service.lock', '.pharos-use.lock']):
        raise ValueError('Refusing a library catalog; use a backup copy')
    before = fingerprint(path)
    if any(x and x[0] for x in before[1:]):
        raise ValueError('Snapshot has a nonempty WAL/SHM; use a checkpointed backup')
    db = sqlite3.connect(path.as_uri() + '?mode=ro&immutable=1', uri=True)
    db.row_factory = sqlite3.Row
    db.execute('PRAGMA query_only=ON')
    return db, path, before


def rank_sample(heap, key, value, n, seed):
    rank = int.from_bytes(hashlib.sha256((seed + key).encode()).digest(), 'big')
    item = (-rank, key, value)
    if len(heap) < n:
        heapq.heappush(heap, item)
    elif n and rank < -heap[0][0]:
        heapq.heapreplace(heap, item)


def scan(args):
    out = outside_git(args.out)
    if out.exists() and any(out.iterdir()):
        raise ValueError('Output must be empty; use another study directory')
    db, source, before = open_snapshot(args.catalog)
    zone = ZoneInfo(args.timezone)
    start, end = [datetime.fromisoformat(x).replace(tzinfo=zone).astimezone(timezone.utc).isoformat().replace('+00:00', 'Z') for x in [args.start, args.end]]
    if end <= start or args.sample < 1:
        raise ValueError('Require end > start and a positive sample size')
    required = {'tool_calls', 'tool_commands', 'workspaces', 'repositories', 'messages', 'tool_mirror_workspaces'}
    present = {r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    if not required <= present:
        raise ValueError('Snapshot missing required tables: ' + ', '.join(sorted(required - present)))
    out.mkdir(parents=True, mode=0o700, exist_ok=True)
    os.chmod(out, 0o700)
    survey, segment_survey, mcp, coverage, kinds, severity, outcomes, repo_counts = (Counter() for _ in range(8))
    matches, match_sample, random_sample = [], [], []
    sql = '''SELECT t.*,w.location,coalesce(r.display_name,'(unknown)') repository
             FROM tool_calls t LEFT JOIN workspaces w ON w.id=t.workspace_id
             LEFT JOIN repositories r ON r.id=w.repository_id
             WHERE t.started_at>=? AND t.started_at<?
               AND NOT EXISTS(SELECT 1 FROM tool_mirror_workspaces m WHERE m.workspace_id=t.workspace_id)
             ORDER BY t.started_at,t.id'''
    total, command_count = 0, 0
    for row in db.execute(sql, (start, end)):
        call = dict(row)
        segments = [dict(s) for s in db.execute('SELECT * FROM tool_commands WHERE tool_call_id=? ORDER BY position', (call['id'],))]
        total += 1
        command_count += len(segments)
        segment_survey.update((s.get('program') or '', s.get('subcommand') or '') for s in segments)
        survey[(call.get('program') or '', call.get('subcommand') or '', call['tool_name'])] += 1
        if call.get('mcp_server'):
            mcp[call['tool_name']] += 1
        hits, gaps = classify(call, segments)
        coverage.update(gaps)
        record = {k: call.get(k) for k in ['id', 'workspace_id', 'conversation_id', 'call_message_id', 'repository', 'started_at', 'tool_name', 'tool_category', 'mcp_server', 'status', 'exit_code', 'file_path', 'path_absolute', 'path_scope']}
        record.update(command=redact(call.get('command')), hits=hits, gaps=gaps, authorization='unknown',
                      reader_path='/work/' + quote(call['workspace_id'], safe='') + '?conversation=' + quote(call['conversation_id'], safe='') + '&message=' + quote(call['call_message_id'], safe=''))
        rank_sample(random_sample, call['id'], record, args.sample, 'random-v1:')
        if hits:
            matches.append(record)
            rank_sample(match_sample, call['id'], record, args.sample, 'precision-v1:')
            kinds.update({h['kind'] for h in hits})
            severity.update([max((h['effective_severity'] for h in hits), key=LEVELS.get)])
            outcomes.update({h['outcome'] for h in hits})
            repo_counts.update({(record['repository'], h['kind']) for h in hits})
        if total % 10000 == 0:
            print(f'{total:,} calls; {len(matches):,} matched', file=sys.stderr, flush=True)
    samples = {}
    for cohort, heap in [('precision', match_sample), ('random', random_sample)]:
        for _, key, record in sorted(heap, reverse=True):
            samples.setdefault(key, dict(record, cohorts=[]))['cohorts'].append(cohort)
    # Request context is evidence for human review, never automatic authorization.
    for record in samples.values():
        original = db.execute('SELECT text FROM messages WHERE id=?', (record['call_message_id'],)).fetchone()
        if original:
            try:
                record['retained_input'] = scrub(json.loads(original['text']))
            except ValueError:
                record['retained_input'] = redact(original['text'])
        row = db.execute('''SELECT m.id,m.text FROM messages m
             WHERE m.conversation_id=? AND m.role='user' AND m.kind='message' AND m.selected=1
               AND m.source_order < (SELECT source_order FROM messages WHERE id=?)
             ORDER BY m.source_order DESC LIMIT 1''', (record['conversation_id'], record['call_message_id'])).fetchone()
        if row:
            record['previous_request'] = dict(message_id=row['id'], text=redact(row['text']), attribution='user role; may be delegated or harness text; verify explicit intent')
        record['review'] = dict(risk_kinds=None, authorization='unknown', authorization_evidence=None, notes='')
    if fingerprint(source) != before:
        raise ValueError('Snapshot changed while reading; discard this run')
    manifest = dict(version=RULES['version'], rules_sha256=hashlib.sha256(RULES_PATH.read_bytes()).hexdigest(),
                    code_sha256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), source=str(source), source_fingerprint=before,
                    source_user_version=db.execute('PRAGMA user_version').fetchone()[0], start=start, end=end, timezone=args.timezone,
                    total_calls=total, command_segments=command_count, matched_calls=len(matches),
                    counts_by_kind=dict(kinds), counts_by_effective_severity=dict(severity), counts_by_outcome=dict(outcomes),
                    counts_by_repository=[dict(repository=r, kind=k, calls=n) for (r, k), n in repo_counts.most_common()],
                    coverage_gaps=dict(coverage), mirror_policy='exclude tool_mirror_workspaces from this snapshot; freshness not independently recomputed',
                    samples=dict(seed='sha256 precision-v1/random-v1 + call ID', precision=len(match_sample), random=len(random_sample)),
                    mcp_calls_note='mcp_calls logs requests to Pharos itself, not agent MCP executions; surveyed agent MCP via tool_calls.mcp_server',
                    limitations=['Successful calls report success, not verified side effects. Compound segments may not all have run.',
                                 'Failed calls can have partial effects; no_result does not imply no action.',
                                 'Commands repeated on polling/wait calls are excluded; such calls are not new executions.',
                                 'Aliases, variables, scripts, shell -c and arbitrary code-mode calls are not evaluated.',
                                 'Ledger commands are clipped at 2,000 characters and segments at 500; clipped segments are excluded and counted as gaps. Sample retained_input holds the full source.',
                                 'Missing edit paths, nested tool calls, SQL from stdin and short tag refs are coverage gaps.',
                                 'Authorization is unknown until an evidence-backed manual annotation; lack of a match does not imply safety.'])
    json_write(out / 'manifest.json', manifest)
    json_write(out / 'survey.json', dict(programs=[dict(program=p, subcommand=s, tool=t, calls=n) for (p, s, t), n in survey.most_common()], segments=[dict(program=p, subcommand=s, commands=n) for (p, s), n in segment_survey.most_common()], mcp=dict(mcp.most_common())))
    jsonl_write(out / 'matches.jsonl', matches)
    jsonl_write(out / 'review.jsonl', samples.values())
    db.close()
    report(argparse.Namespace(study=str(out), labels=None))
    print(json.dumps(dict(out=str(out), calls=total, matches=len(matches))))


def wilson(yes, n):
    if not n:
        return None
    z = 1.96
    p = yes / n
    mid = (p + z*z/(2*n)) / (1 + z*z/n)
    width = z * ((p*(1-p)/n + z*z/(4*n*n)) ** .5) / (1 + z*z/n)
    return dict(n=n, yes=yes, estimate=p, ci95=[mid-width, mid+width])


def evaluation(samples, labels):
    by_id = {s['id']: s for s in samples}
    seen, reviewed = set(), []
    for label in labels:
        key = label['id']
        if key in seen or key not in by_id:
            raise ValueError('Duplicate/unknown review ID: ' + key)
        seen.add(key)
        review = label['review']
        kinds = review['risk_kinds']
        if kinds is None:
            continue
        if not isinstance(kinds, list) or not set(kinds) <= KINDS:
            raise ValueError('Invalid risk kinds: ' + key)
        if review.get('authorization', 'unknown') not in {'unknown', 'explicitly_requested'}:
            raise ValueError('Invalid authorization: ' + key)
        if review.get('authorization') == 'explicitly_requested' and not review.get('authorization_evidence'):
            raise ValueError('Explicit authorization requires evidence: ' + key)
        reviewed.append((by_id[key], review))
    precision = [(s, r) for s, r in reviewed if 'precision' in s['cohorts']]
    random = [(s, r) for s, r in reviewed if 'random' in s['cohorts']]
    # Call-level detection: did it contain ANY action in scope? Kind-level correctness is separate.
    positive_random = [(s, r) for s, r in random if r['risk_kinds']]
    return dict(reviewed=len(reviewed), precision=wilson(sum(bool(r['risk_kinds']) for s, r in precision), len(precision)),
                recall=wilson(sum(bool(s['hits']) for s, r in positive_random), len(positive_random)),
                random_reviewed=len(random), random_positives=len(positive_random),
                exact_kind_agreement=wilson(sum({h['kind'] for h in s['hits']} == set(r['risk_kinds']) for s, r in precision), len(precision)),
                complete_precision_sample=len(precision) == sum('precision' in s['cohorts'] for s in samples),
                complete_random_sample=len(random) == sum('random' in s['cohorts'] for s in samples),
                false_negative_ids=[s['id'] for s, r in positive_random if not s['hits']])


def report(args):
    root = outside_git(args.study)
    manifest = json.loads((root / 'manifest.json').read_text())
    matches = jsonl_read(root / 'matches.jsonl')
    samples = jsonl_read(root / 'review.jsonl')
    labels = jsonl_read(args.labels) if args.labels else samples
    metrics = evaluation(samples, labels)
    json_write(root / 'evaluation.json', metrics)
    esc = lambda x: html.escape(str(x or ''))
    def table(rows):
        return '<table>' + ''.join('<tr>' + ''.join('<td>' + esc(v) + '</td>' for v in row) + '</tr>' for row in rows) + '</table>'
    def rate(metric):
        if metric is None:
            return 'not measured'
        return f"{metric['yes']}/{metric['n']} = {metric['estimate']:.1%} (95% Wilson interval {metric['ci95'][0]:.1%}–{metric['ci95'][1]:.1%})"
    summary = f'''<h1>Risky tool calls — research prototype</h1><p>{esc(manifest['start'])} ≤ call time &lt; {esc(manifest['end'])} · {esc(manifest['timezone'])} · {esc(manifest['version'])}</p>
    <p><b>{manifest['matched_calls']:,} matched calls / {manifest['total_calls']:,} calls.</b> These are potentially risky attempted actions. A command's presence or successful exit does not verify its effect. Kinds can overlap; each call counts once per kind.</p>
    <h2>Counts by risk kind</h2>{table(manifest['counts_by_kind'].items())}
    <h2>Effective severity</h2>{table(manifest['counts_by_effective_severity'].items())}<p>Each call appears at its highest effective severity. Routine remote writes remain visible. Recognized dry runs and literal scratch-only deletions are low severity. Unknown variables are never assumed temporary.</p>
    <h2>Reported outcomes</h2>{table(manifest['counts_by_outcome'].items())}<p>Failures may have partial effects; missing results remain unknown.</p>
    <h2>Manual sample review</h2><p>Call precision: {rate(metrics['precision'])}<br>Call recall in random sample: {rate(metrics['recall'])}<br>Exact risk-kind agreement: {rate(metrics['exact_kind_agreement'])}<br>{metrics['random_reviewed']} random calls reviewed; {metrics['random_positives']} positives. Recall includes routine remote writes and scratch deletions. Partial samples are exploratory.</p>
    <p><b>Review provenance:</b> {esc(', '.join(sorted({str(x.get('reviewer', 'unspecified')) for x in labels if x.get('review', {}).get('risk_kinds') is not None})) or 'not reviewed')}. These are observational labels, not proof of effects. If these calls informed rule development, precision is calibration; the intervals exclude tuning and reviewer bias. Recall covers identifiable actions in retained inputs, not hidden script behavior.</p>
    <h2>Workspace repositories</h2>{table((x['repository'], x['kind'], x['calls']) for x in manifest['counts_by_repository'])}
    <h2>Coverage gaps (overlap)</h2>{table(manifest['coverage_gaps'].items())}<ul>{''.join('<li>'+esc(x)+'</li>' for x in manifest['limitations'])}</ul>
    <p>{esc(manifest['mirror_policy'])}. {esc(manifest['mcp_calls_note'])}. See survey.json, manifest.json, matches.jsonl and review.jsonl for reproducibility.</p>'''
    annotations = {r['id']: r['review'] for r in labels if r.get('review', {}).get('risk_kinds') is not None}
    missed = []
    for call in samples:
        if call['id'] in metrics['false_negative_ids']:
            review = annotations[call['id']]
            missed.append('<details data-search="audit miss"><summary>Audit miss · ' + esc(call['repository']) + ' · ' + esc(', '.join(review['risk_kinds'])) + '</summary><p>' + esc(review.get('notes')) + ' · <a class="reader" target="_blank" rel="noopener" href="' + esc(call['reader_path']) + '">Open original call</a></p><pre>' + esc(call['command'] or call['file_path'] or json.dumps(call.get('retained_input'))) + '</pre></details>')
    summary += '<h2>Missed actions in the random audit</h2>' + (''.join(missed) or '<p>No reviewed misses (an unreviewed sample does not establish recall).</p>')
    details = []
    for call in sorted(matches, key=lambda c: (-max(LEVELS[h['effective_severity']] for h in c['hits']), c['started_at'], c['id'])):
        kinds = sorted({h['kind'] for h in call['hits']})
        levels = sorted({h['effective_severity'] for h in call['hits']})
        annotation = annotations.get(call['id'], {})
        auth = annotation.get('authorization', 'unknown')
        title = f"{call['repository']} · {', '.join(kinds)} · {', '.join(levels)} · {call['status']} · requested: {auth}"
        text = call['command'] or call['file_path'] or call['tool_name']
        reviewed = '<p>Manual review: ' + esc(', '.join(annotation['risk_kinds']) or 'no action in scope') + ' · ' + esc(annotation.get('notes')) + '</p>' if annotation else ''
        details.append('<details data-search="' + esc(title.lower()) + '"><summary>' + esc(title) + '</summary><p>' + esc(call['started_at']) + ' · ' + esc(annotation.get('authorization_evidence') or '') + ' · <a target="_blank" rel="noopener" class="reader" href="' + esc(call['reader_path']) + '">Open original tool call</a></p><pre>' + esc(text) + '</pre>' + table((h['evidence'], h['reversibility'], h['outcome']) for h in call['hits']) + reviewed + '</details>')
    document = '''<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Risky tool calls</title><style>
    body{font:16px/1.5 system-ui;max-width:1100px;margin:2rem auto;padding:0 1rem;color:#202630;background:#fafafa}table{border-collapse:collapse;margin:1rem 0;width:100%;table-layout:fixed}td{border:1px solid #ddd;padding:.35rem .7rem;overflow-wrap:anywhere}pre{white-space:pre-wrap;overflow-wrap:anywhere;max-height:30rem;overflow:auto;background:#fff;padding:1rem}details{border:1px solid #ddd;margin:.5rem 0;padding:.7rem}summary{cursor:pointer;overflow-wrap:anywhere}input{font:inherit;padding:.4rem;max-width:90%}h2{margin-top:2rem}</style>''' + summary + '''<h2>Per-call drill-down</h2><p><label>Pharos origin <input id="base" value="http://127.0.0.1:50612"></label> <label>Filter repository, kind, severity, outcome <input id="search" type="search"></label></p>''' + ''.join(details) + '''<script>
    const links=[...document.querySelectorAll('.reader')].map(a=>[a,a.getAttribute('href')]);
    function setBase(value){try{const u=new URL(value);if(!['http:','https:'].includes(u.protocol))return;for(const[a,p]of links)a.href=u.origin+p}catch{}}
    document.querySelector('#base').addEventListener('input',e=>setBase(e.target.value));
    setBase(document.querySelector('#base').value);
    document.querySelector('#search').addEventListener('input',e=>{const q=e.target.value.toLowerCase();for(const d of document.querySelectorAll('details'))d.hidden=!d.dataset.search.includes(q)});
    </script></html>'''
    private_write(root / 'report.html', document)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    commands = ap.add_subparsers(dest='operation', required=True)
    s = commands.add_parser('scan')
    s.add_argument('--catalog', required=True)
    s.add_argument('--snapshot-copy', required=True, action='store_true')
    s.add_argument('--out', required=True)
    s.add_argument('--start', required=True, help='Inclusive local date, YYYY-MM-DD')
    s.add_argument('--end', required=True, help='Exclusive local date, YYYY-MM-DD')
    s.add_argument('--timezone', default='America/Denver')
    s.add_argument('--sample', type=int, default=300)
    r = commands.add_parser('report')
    r.add_argument('--study', required=True)
    r.add_argument('--labels')
    args = ap.parse_args()
    try:
        (scan if args.operation == 'scan' else report)(args)
    except (ValueError, OSError, sqlite3.Error) as error:
        ap.exit(1, str(error) + '\n')


if __name__ == '__main__':
    main()
