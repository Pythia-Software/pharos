#!/usr/bin/env python3
"""Export a private, read-only label snapshot with recorded agent nesting."""

import argparse
import datetime as dt
import json
import math
import os
import pathlib
import sqlite3
import struct
import tempfile


def decode_labels(mask, raw, order):
    if len(raw) != len(order) * 4 or len(order) > 64:
        raise ValueError("Label probabilities do not match the recorded model order")
    values = struct.unpack("<" + "f" * len(order), raw)
    if any(not math.isfinite(p) or not 0 <= p <= 1 for p in values):
        raise ValueError("Invalid label probability")
    return [[name, round(values[i], 4)] for i, name in enumerate(order) if mask & (1 << i)]


def excerpt(text, segment, spans, limit=800):
    provenance = "Retained message excerpt"
    if segment.startswith("H-typed") and spans:
        raw = text.encode("utf-8")
        pieces = [raw[s["start"]:s["end"]].decode("utf-8")
                  for s in json.loads(spans) if s.get("category") == "typed"]
        if pieces:
            text, provenance = "\n\n".join(pieces), "Typed text excerpt"
    text = text.strip()
    return text[:limit], provenance, len(text) > limit


def read_workflow(db, root, orders):
    """Keep native session order and explicit links; never merge branch timelines."""
    records, session_rows, pending = {}, {}, [root["id"]]
    while pending:
        cid = pending.pop(0)
        if cid in records:
            continue
        row = db.execute("SELECT * FROM conversations WHERE id=?", (cid,)).fetchone()
        if row is None:
            continue
        records[cid] = dict(row)
        for s in db.execute("SELECT * FROM agent_sessions WHERE conversation_id=? ORDER BY depth,started_at,id", (cid,)):
            session_rows[s["id"]] = dict(s)
            if s["child_conversation_id"]:
                pending.append(s["child_conversation_id"])
        pending.extend(r[0] for r in db.execute("""SELECT c.id FROM conversations c WHERE c.parent_id=?
            AND NOT EXISTS(SELECT 1 FROM conversation_group_membership g WHERE g.conversation_id=c.id AND g.active=0)
            ORDER BY c.started_at,c.id""", (cid,)))
    primary = {}
    for cid in records:
        own = [s for s in session_rows.values() if s["conversation_id"] == cid]
        if own:
            own.sort(key=lambda s: (s["parent_id"] is not None, s["native_id"] != "main", s["depth"], s["id"]))
            primary[cid] = own[0]["id"]
        else:
            sid = "conversation:" + cid
            primary[cid] = sid
            session_rows[sid] = {"id": sid, "conversation_id": cid, "parent_id": None,
                "child_conversation_id": None, "delegation_message_id": None, "native_id": "",
                "kind": "root" if cid == root["id"] else "subagent", "depth": 0}
    counts = {r[0]: r[1] for r in db.execute("""SELECT agent_session_id,count(*) FROM agent_session_messages
        WHERE agent_session_id IN (SELECT value FROM json_each(?)) GROUP BY agent_session_id""", (json.dumps(list(session_rows)),))}
    # Empty handles point at a separately retained child transcript. Use that
    # transcript once, carrying the handle's exact parent/call evidence.
    aliases, incoming = {}, {}
    for sid, s in session_rows.items():
        child = primary.get(s["child_conversation_id"])
        if child and child != sid and not counts.get(sid):
            aliases[sid] = child
            incoming.setdefault(child, []).append(s)
    def canonical(sid):
        seen = set()
        while sid in aliases and sid not in seen:
            seen.add(sid)
            sid = aliases[sid]
        return sid
    sessions = {}
    for sid, s in session_rows.items():
        if sid in aliases:
            continue
        cv = records[s["conversation_id"]]
        links = incoming.get(sid, [])
        # Conflicting owners stay unresolved, rather than picking one caller.
        owners = {canonical(link["parent_id"]) for link in links}
        exact = links[0] if links and len(owners) == 1 else None
        parent = canonical(exact["parent_id"] if exact else s["parent_id"])
        evidence = "delegation handle" if exact else "native session parent" if parent else ""
        if not parent and cv["parent_id"] in primary:
            parent, evidence = canonical(primary[cv["parent_id"]]), "conversation lineage"
        if sid == primary[root["id"]]:
            parent, evidence = None, "sample root"
        if parent == sid:
            parent, evidence = None, "unresolved parent"
        native = (cv.get("native_id") if sid == primary[cv["id"]] else s["native_id"]) or s["native_id"] or sid
        prefix = "Unresolved call " if sid != primary[cv["id"]] and not counts.get(sid) else "Subagent "
        nickname = cv.get("agent_nickname") if sid == primary[cv["id"]] else None
        name = "Main agent" if sid == primary[root["id"]] else nickname or prefix + native.split(":")[-1][-8:]
        dirty = db.execute("SELECT EXISTS(SELECT 1 FROM message_labels_dirty WHERE conversation_id=?)", (cv["id"],)).fetchone()[0]
        sessions[sid] = {"id": sid, "parent_id": parent, "conversation_id": cv["id"], "work_id": cv["workspace_id"],
            "name": name, "kind": s["kind"], "depth": 0, "link_evidence": evidence,
            "call_message_id": (exact or s)["delegation_message_id"], "result_message_id": None,
            "labels_pending": bool(dirty), "model": s.get("model") or (cv.get("model") if sid == primary[cv["id"]] else "") or "", "provider": s.get("provider") or cv.get("provider") or ""}
    for sid, s in sessions.items():
        visited, current = {sid}, s["parent_id"]
        while current in sessions and current not in visited:
            visited.add(current)
            s["depth"] += 1
            current = sessions[current]["parent_id"]
        if current in visited:
            s["parent_id"], s["depth"], s["link_evidence"] = None, 0, "unresolved cycle"
        elif s["parent_id"] not in sessions:
            s["parent_id"] = None
    messages, models = [], set()
    message_has_model = any(r[1] == "model" for r in db.execute("PRAGMA table_info(messages)"))
    for cid, cv in records.items():
        rows = db.execute(f"""
            SELECT {"m.model" if message_has_model else "NULL"} AS generation_model,m.id,m.role,m.kind,m.created_at,m.source_order,m.text,m.call_id,
                   am.agent_session_id,am.source_order session_order,
                   l.segment,l.labels,l.probs,l.model,l.chunks,a.spans_json
            FROM messages m
            LEFT JOIN agent_session_messages am ON am.message_id=m.id
            LEFT JOIN message_labels l ON l.message_id=m.id
            LEFT JOIN message_authorship a ON a.message_id=m.id
            WHERE m.conversation_id=? AND m.selected=1
              AND m.kind IN ('message','reasoning','thinking','delegation','delegation_result')
              AND m.role IN ('user','assistant','agent','tool')
            ORDER BY m.source_order IS NULL,m.source_order,m.created_at,m.id
        """, (cid,)).fetchall()
        for r in rows:
            sid = canonical(r["agent_session_id"] or primary[cid])
            if sid not in sessions:
                sid = canonical(primary[cid])
            segment, labels, chunks = r["segment"] or "", [], 0
            if r["model"] and not sessions[sid]["labels_pending"]:
                if r["model"] not in orders:
                    raise ValueError("A label row references an unknown model")
                labels = decode_labels(r["labels"], r["probs"], orders[r["model"]])
                models.add(r["model"])
                chunks = r["chunks"]
            text, provenance, truncated = excerpt(r["text"], segment, r["spans_json"])
            messages.append({"id": r["id"], "role": r["role"], "kind": r["kind"], "at": r["created_at"],
                "segment": segment, "labels": labels, "text": text, "text_source": provenance,
                "truncated": truncated, "chunks": chunks, "session_id": sid, "conversation_id": cid,
                "work_id": cv["workspace_id"], "order": r["session_order"] if r["session_order"] is not None else r["source_order"],
                "call_id": r["call_id"], "generation_model": r["generation_model"] or ""})
    by_id = {m["id"]: m for m in messages}
    for s in sessions.values():
        call = by_id.get(s["call_message_id"])
        if call:
            # A tool result can be an asynchronous launch acknowledgement.
            # Keep its identity, without claiming the child's work completed.
            results = [m for m in messages if m["session_id"] == s["parent_id"] and m["kind"] == "delegation_result"
                       and call["call_id"] and m["call_id"] == call["call_id"]
                       and (m["order"] is None or call["order"] is None or m["order"] >= call["order"])]
            if results:
                s["result_message_id"] = results[0]["id"]
    messages.sort(key=lambda m: (m["session_id"], m["order"] is None, m["order"] or 0, m["at"] or "", m["id"]))
    activity = None
    if db.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name='tool_calls'").fetchone():
        activity = []
        for cid in records:
            for t in db.execute("""SELECT id,agent_session_id,call_message_id,tool_name,tool_category,
                    command_category,status,started_at,ended_at,duration_ms
                    FROM tool_calls WHERE conversation_id=? ORDER BY started_at,id""", (cid,)):
                sid = canonical(t["agent_session_id"] or primary[cid])
                if sid not in sessions:
                    sid = canonical(primary[cid])
                activity.append({"id": t["id"], "session_id": sid, "message_id": t["call_message_id"],
                    "name": t["tool_name"], "category": t["tool_category"], "command_category": t["command_category"],
                    "status": t["status"], "start": t["started_at"], "end": t["ended_at"], "duration_ms": t["duration_ms"]})
    return messages, sorted(sessions.values(), key=lambda s: (s["depth"], s["id"])), primary[root["id"]], models, activity


def extract(catalog, limit=200, minimum=8, roots=None):
    catalog = pathlib.Path(catalog).resolve(strict=True)
    db = sqlite3.connect(catalog.as_uri() + "?mode=ro", uri=True, timeout=5)
    db.row_factory = sqlite3.Row
    db.execute("PRAGMA query_only=ON")
    db.execute("BEGIN")
    try:
        orders = {r["model"]: json.loads(r["labels_json"]) for r in db.execute("SELECT model,labels_json FROM message_labels_models")}
        ledger = dict(db.execute("""SELECT count(*) messages,count(DISTINCT conversation_id) conversations,
            min(labeled_at) first_labeled,max(labeled_at) last_labeled FROM message_labels""").fetchone())
        candidates = db.execute("""
            SELECT c.*,w.title,coalesce(r.display_name,'Unknown repository') repository
            FROM (SELECT conversation_id FROM message_labels WHERE segment NOT IN ('TH','DEL')
                  GROUP BY conversation_id HAVING count(*)>=?) n
            JOIN conversations c ON c.id=n.conversation_id JOIN workspaces w ON w.id=c.workspace_id
            LEFT JOIN repositories r ON r.id=w.repository_id
            WHERE c.agent_depth=0
              AND NOT EXISTS(SELECT 1 FROM message_labels_dirty d WHERE d.conversation_id=c.id)
              AND EXISTS(SELECT 1 FROM message_labels_conversations b WHERE b.conversation_id=c.id)
              AND NOT EXISTS(SELECT 1 FROM conversation_group_membership g WHERE g.conversation_id=c.id AND g.active=0)
            ORDER BY c.started_at DESC,c.id
        """, (minimum,)).fetchall()
        conversations, seen, models = [], set(), set()
        for c in candidates:
            if roots is not None and c["id"] not in roots:
                continue
            aliases = {c["native_id"], *(json.loads(c["aliases_json"] or "[]") or [])} - {"", None}
            duplicate = bool(aliases & seen)
            seen.update(aliases)
            if duplicate:
                continue
            messages, sessions, main, used, activity = read_workflow(db, c, orders)
            if sum(m["chunks"] > 0 and m["kind"] == "message" and m["session_id"] == main for m in messages) < minimum:
                continue
            models.update(used)
            conversations.append({"id": c["id"], "work_id": c["workspace_id"], "title": c["title"],
                "repository": c["repository"], "provider": c["provider"], "started_at": c["started_at"],
                "messages": messages, "sessions": sessions, "main_session": main, "activity": activity})
            if len(conversations) >= limit:
                break
        if not conversations:
            raise ValueError("No clean labeled main conversations meet the sample criteria")
        taxonomy = json.loads((pathlib.Path(__file__).resolve().parents[1] / "internal/archive/labeling/taxonomy-candidate-0.3.json").read_text())
        return {"version": 2, "source": {"kind": "production-derived catalog copy", "catalog": str(catalog),
            "exported_at": dt.datetime.now(dt.timezone.utc).isoformat(), "models": sorted(models),
            "taxonomy": taxonomy["version"], "ledger": ledger, "candidate_conversations": len(candidates),
            "limit": limit, "minimum_labeled_prose": minimum, "retained_roots": roots is not None,
            "selection": "Clean labeled roots, inactive root mirrors excluded, native aliases deduplicated. Includes recorded nested sessions and linked descendant conversations. Empty delegation handles resolve to their retained child transcript; explicit calls/tool results are retained. Dirty child labels are withheld. Each branch retains its own order; no interleaved thread is inferred. Restricted sample, not a library estimate."},
            "taxonomy": [{k: row[k] for k in ("id", "parent", "name", "definition")} for row in taxonomy["rows"]], "conversations": conversations}
    finally:
        db.rollback()
        db.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", required=True, type=pathlib.Path)
    parser.add_argument("--output", required=True, type=pathlib.Path)
    parser.add_argument("--limit", type=int, default=200)
    parser.add_argument("--minimum", type=int, default=8)
    parser.add_argument("--roots-from", type=pathlib.Path, help="Keep the root identities of an earlier private snapshot")
    args = parser.parse_args()
    if not 1 <= args.limit <= 1000 or args.minimum < 2:
        parser.error("Choose --limit 1–1000 and --minimum at least 2")
    roots = {c["id"] for c in json.loads(args.roots_from.read_text())["conversations"]} if args.roots_from else None
    payload = extract(args.catalog, args.limit, args.minimum, roots)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(dir=args.output.parent, prefix=".label-trajectories-")
    try:
        with os.fdopen(fd, "w") as stream:
            json.dump(payload, stream, ensure_ascii=False, separators=(",", ":"))
        os.replace(name, args.output)
    finally:
        if os.path.exists(name): os.unlink(name)
    messages = [m for c in payload["conversations"] for m in c["messages"]]
    print(json.dumps({"conversations": len(payload["conversations"]), "messages": len(messages),
        "sessions": sum(len(c["sessions"]) for c in payload["conversations"]),
        "labeled": sum(bool(m["chunks"]) for m in messages), "models": payload["source"]["models"], "output": str(args.output)}, indent=2))


if __name__ == "__main__": main()
