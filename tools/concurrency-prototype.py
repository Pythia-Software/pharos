#!/usr/bin/env python3
"""Read an existing dev catalog into a bounded, real-data concurrency preview.

No catalog migrations or writes. Model spans are input-to-response estimates,
not inference telemetry. Long unexplained waits are omitted, not capped or
invented. User threads are based on observed prompt-to-last-response turns.
"""

import argparse
import datetime as dt
import json
import pathlib
import re
import sqlite3
import time
from zoneinfo import ZoneInfo


def timestamp(value):
    if not value:
        return None
    try:
        return int(dt.datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp() * 1000)
    except (ValueError, TypeError):
        return None


def merge(spans, gap=0):
    result = []
    for start, end in sorted(spans):
        if end <= start:
            continue
        if result and start - result[-1][1] <= gap:
            result[-1][1] = max(result[-1][1], end)
        else:
            result.append([start, end])
    return result


def activity(messages):
    """A model response follows a user/tool input; tools themselves aren't model time."""
    model, turns = [], []
    pending = response = turn_start = turn_end = previous_at = None
    missing = 0

    def close_request():
        nonlocal pending, response, missing
        if pending is not None and response is not None and response > pending:
            # Queueing, an approval, or a suspended machine cannot be called
            # model execution. Keep this conservative and report exclusions.
            if response - pending <= 10 * 60 * 1000:
                model.append([pending, response])
            else:
                missing += 1
        pending = response = None

    def close_turn():
        nonlocal turn_start, turn_end
        if turn_start is not None and turn_end is not None and turn_end > turn_start:
            turns.append([turn_start, turn_end])
        turn_start = turn_end = None

    for message in messages:
        at = timestamp(message["created_at"])
        if at is None:
            continue
        role, kind = message["role"], message["kind"]
        if previous_at is not None and at - previous_at > 60 * 60 * 1000:
            # Without explicit turn-end telemetry, an hour of silence is not
            # evidence that a single agent turn kept running throughout.
            close_request()
            close_turn()
        previous_at = at
        if role == "user" and kind == "message":
            close_request()
            close_turn()
            pending = turn_start = at
        elif kind in ("tool_result", "delegation_result"):
            close_request()
            pending = at
            if turn_start is not None:
                turn_end = at
        elif role == "assistant" and kind in ("message", "thinking", "tool_call", "delegation"):
            if pending is not None and at >= pending:
                response = at
            if turn_start is None:
                # A capture can begin midway through a turn. Do not extend it
                # backward to the conversation's creation date.
                turn_start = at
            turn_end = at
    close_request()
    close_turn()
    return merge(model), merge(turns), missing


def title(text, fallback):
    # Exclude injected environment instructions from a conversation label.
    text = re.sub(r"<(system_instruction|environment_context|INSTRUCTIONS|instructions|context|user_instructions)\b[^>]*>.*?</\1>", "", text or "", flags=re.S)
    text = re.sub(r"\s+", " ", text).strip()
    return text[:110] if text else fallback


def extract(catalog, days, timezone):
    started = time.monotonic()
    zone = ZoneInfo(timezone)
    db = sqlite3.connect(catalog.resolve().as_uri() + "?mode=ro", uri=True)
    db.row_factory = sqlite3.Row
    db.execute("PRAGMA query_only=ON")
    # The indexed hourly ledger is small; it establishes the snapshot's last
    # observed activity rather than selecting an empty 'today' in an old copy.
    latest = db.execute("SELECT usage_hour FROM agent_session_usage WHERE usage_hour IS NOT NULL ORDER BY usage_hour DESC LIMIT 1").fetchone()
    if latest is None:
        raise RuntimeError("No timestamped usage in this catalog")
    last = dt.datetime.fromtimestamp(timestamp(latest[0]) / 1000, zone).date()
    first = last - dt.timedelta(days=days - 1)
    start = dt.datetime.combine(first, dt.time(), zone)
    end = dt.datetime.combine(last + dt.timedelta(days=1), dt.time(), zone)
    lower, upper = start.timestamp() * 1000, end.timestamp() * 1000
    iso = lambda value: value.astimezone(dt.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")
    rows = db.execute("""
        SELECT c.id,c.workspace_id,c.provider,c.model,c.native_id,c.aliases_json,c.parent_id,c.agent_depth,
               w.title,w.location,COALESCE(r.display_name,'Unknown repository') repository
        FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
        LEFT JOIN repositories r ON r.id=w.repository_id
        WHERE c.ended_at>=? AND c.started_at<?
          AND c.provider IN ('codex','claude','antigravity')
          AND NOT EXISTS(SELECT 1 FROM conversation_group_membership gm WHERE gm.conversation_id=c.id AND gm.active=0)
        ORDER BY c.agent_depth,c.started_at,c.id
    """, (iso(start), iso(end))).fetchall()

    # Native/Conductor aliases are the same conversation. Pick the richer
    # retained transcript and carry the human workspace title from its mirror.
    families, by_alias = [], {}
    for row in rows:
        aliases = json.loads(row["aliases_json"] or "null") or []
        keys = [row["native_id"], *aliases]
        found = {by_alias[key] for key in keys if key in by_alias}
        index = min(found) if found else len(families)
        if not found:
            families.append([])
        for other in found - {index}:
            families[index].extend(families[other])
            families[other] = []
            by_alias = {key: index if value == other else value for key, value in by_alias.items()}
        families[index].append(row)
        for key in keys:
            by_alias[key] = index

    threads, excluded, mirror_count, representative = [], 0, 0, {}
    for family in families:
        if not family:
            continue
        candidates = []
        for row in family:
            count = db.execute("SELECT count(*) FROM messages WHERE conversation_id=?", (row["id"],)).fetchone()[0]
            candidates.append((count, row))
        row = max(candidates, key=lambda candidate: candidate[0])[1]
        representative.update({item["id"]: row["id"] for item in family})
        mirror_count += len(family) - 1
        messages = db.execute("""
            SELECT m.role,m.kind,m.created_at,
                   CASE WHEN m.role='user' AND m.kind='message' THEN substr(m.text,1,12000) ELSE '' END text
            FROM messages m WHERE m.conversation_id=?
              AND m.created_at>=? AND m.created_at<?
              AND (m.role IN ('user','assistant') OR m.kind IN ('tool_result','delegation_result'))
              AND NOT EXISTS(SELECT 1 FROM agent_session_messages am JOIN agent_sessions a ON a.id=am.agent_session_id
                             WHERE am.message_id=m.id AND a.native_id<>'main')
            ORDER BY m.source_order IS NULL,m.source_order,m.created_at,m.id
        """, (row["id"], iso(start - dt.timedelta(hours=2)), iso(end + dt.timedelta(hours=2)))).fetchall()
        model_spans, turns, missing = activity(messages)
        excluded += missing
        clip = lambda spans: [[max(a, int(lower)), min(b, int(upper))] for a, b in spans if a < upper and b > lower]
        model_spans, turns = clip(model_spans), clip(turns)
        if not turns:
            continue
        label_row = next((item for item in family if not item["title"].startswith(("Codex ", "Claude "))), row)
        initial = db.execute("SELECT substr(text,1,12000) FROM messages WHERE conversation_id=? AND +role='user' AND kind='message' ORDER BY source_order IS NULL,source_order,created_at LIMIT 1", (row["id"],)).fetchone()
        prompt = initial[0] if initial else ""
        location = row["location"] or label_row["location"]
        # Source adapters can assign different workspace IDs to the same
        # checkout. Location supplies the shared user-workspace identity.
        workspace = str(pathlib.PurePath(location)) if location else row["workspace_id"]
        threads.append({
            "id": row["id"], "conversation_id": row["id"], "work_id": row["workspace_id"],
            "workspace_id": workspace, "workspace_name": pathlib.PurePath(location).name if location else label_row["title"],
            "repository": row["repository"], "title": label_row["title"] if not label_row["title"].startswith(("Codex ", "Claude ", "Antigravity ")) else title(prompt, label_row["title"]),
            "provider": row["provider"], "model": row["model"] or "Unknown model", "agent_depth": row["agent_depth"],
            "turns": turns, "model_spans": model_spans,
        })
    # A delegated conversation runs inside its root workspace; it contributes
    # compute, not another user-managed workspace or another user thread.
    by_id = {item["id"]: item for item in threads}
    for thread in threads:
        if not thread["agent_depth"]:
            continue
        current, visited = thread["id"], set()
        while current not in visited:
            visited.add(current)
            parent = db.execute("SELECT parent_id FROM conversations WHERE id=?", (current,)).fetchone()
            if not parent or not parent[0]:
                break
            current = representative.get(parent[0], parent[0])
            root = by_id.get(current)
            if root and not root["agent_depth"]:
                for key in ("workspace_id", "workspace_name", "repository", "work_id"):
                    thread[key] = root[key]
                thread["root_conversation_id"] = root["id"]
                break
    db.close()
    result = {
        "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(), "timezone": timezone,
        "period_start": first.isoformat(), "period_end": last.isoformat(),
        "days": [(first + dt.timedelta(days=i)).isoformat() for i in range(days)],
        "threads": threads,
        "coverage": {"conversations": len(threads), "mirrors_removed": mirror_count,
                     "unexplained_waits_excluded": excluded, "timing": "estimated", "catalog": "pharos-dev"},
    }
    print(f"Read {len(threads)} conversations; {mirror_count} mirrors removed; {excluded} unexplained waits excluded in {time.monotonic()-started:.1f}s", flush=True)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--days", type=int, default=7, choices=range(1,15))
    parser.add_argument("--timezone", default="America/Denver")
    args = parser.parse_args()
    result = extract(args.catalog, args.days, args.timezone)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    temporary = args.output.with_suffix(".tmp")
    temporary.write_text(json.dumps(result, separators=(",", ":")))
    temporary.chmod(0o600)
    temporary.replace(args.output)


if __name__ == "__main__":
    main()
