"""Backfill Codex model and effort changes from turn_context events without reindexing text.

Usage: python3 tools/backfill_codex_models.py PATH_TO_CATALOG
"""

from __future__ import annotations

import argparse
import bisect
import json
import sqlite3
from pathlib import Path


def configuration_changes(path: Path) -> tuple[list[int], list[tuple[str | None, str | None]]]:
    lines: list[int] = []
    configurations: list[tuple[str | None, str | None]] = []
    model = effort = None
    with path.open("rb") as source:
        for number, raw in enumerate(source, 1):
            if b"turn_context" not in raw and b"session_meta" not in raw:
                continue
            try:
                event = json.loads(raw)
            except json.JSONDecodeError:
                continue
            if event.get("type") not in {"turn_context", "session_meta"}:
                continue
            payload = event.get("payload", {})
            reported_model = payload.get("model")
            if isinstance(reported_model, str) and reported_model:
                model = reported_model
            if event.get("type") == "turn_context" or any(key in payload for key in ("effort", "reasoning_effort", "model_reasoning_effort")):
                reported_effort = payload.get("effort", payload.get("reasoning_effort", payload.get("model_reasoning_effort")))
                effort = reported_effort.strip().lower() or None if isinstance(reported_effort, str) else None
            configuration = (model, effort)
            if not configurations or configuration != configurations[-1]:
                lines.append(number)
                configurations.append(configuration)
    return lines, configurations


def backfill(db: sqlite3.Connection) -> tuple[int, int, int]:
    db.execute("PRAGMA busy_timeout=10000")
    db.execute("PRAGMA foreign_keys=ON")
    columns = {row[1] for row in db.execute("PRAGMA table_info(messages)")}
    if "model" not in columns:
        db.execute("ALTER TABLE messages ADD COLUMN model TEXT")
    for table in ("messages", "conversations"):
        columns = {row[1] for row in db.execute(f"PRAGMA table_info({table})")}
        if "effort" not in columns:
            db.execute(f"ALTER TABLE {table} ADD COLUMN effort TEXT")
    tables = {row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    conversations = db.execute(
        "SELECT c.id,c.origin FROM conversations c JOIN workspaces w ON w.id=c.workspace_id "
        "WHERE w.source_kind='codex' AND c.provider='codex' AND c.origin IS NOT NULL"
    ).fetchall()
    updated_conversations = updated_messages = missing = 0
    for conversation_id, origin in conversations:
        path = Path(origin)
        if not path.is_file():
            missing += 1
            continue
        lines, configurations = configuration_changes(path)
        if not configurations:
            continue
        model, effort = configurations[-1]
        cursor = db.execute(
            "UPDATE conversations SET model=COALESCE(?,model),effort=? WHERE id=? "
            "AND (model IS NOT COALESCE(?,model) OR effort IS NOT ?)",
            (model, effort, conversation_id, model, effort),
        )
        changed = cursor.rowcount > 0
        updated_conversations += 1
        rows = db.execute(
            "SELECT source_order,evidence_locator FROM messages WHERE conversation_id=? ORDER BY source_order",
            (conversation_id,),
        ).fetchall()
        ranges: list[tuple[tuple[str | None, str | None], int, int]] = []
        current_configuration = None
        start = end = 0
        prefix = origin + ":"
        for source_order, locator in rows:
            if source_order is None or not locator or not locator.startswith(prefix):
                continue
            try:
                line = int(locator[len(prefix):])
            except ValueError:
                continue
            index = bisect.bisect_right(lines, line) - 1
            configuration = configurations[index] if index >= 0 else None
            if configuration is None:
                continue
            if current_configuration != configuration or source_order != end + 1:
                if current_configuration is not None:
                    ranges.append((current_configuration, start, end))
                current_configuration, start = configuration, source_order
            end = source_order
        if current_configuration is not None:
            ranges.append((current_configuration, start, end))
        for (model, effort), first, last in ranges:
            cursor = db.execute(
                "UPDATE messages SET model=COALESCE(?,model),effort=? WHERE conversation_id=? AND source_order BETWEEN ? AND ? "
                "AND (model IS NOT COALESCE(?,model) OR effort IS NOT ?)",
                (model, effort, conversation_id, first, last, model, effort),
            )
            updated_messages += cursor.rowcount
            changed = changed or cursor.rowcount > 0
        # These derived rows are rebuilt from corrected messages on the next app
        # startup/build-tools; stale fingerprints must not hide the correction.
        if changed:
            for table in ("agent_sessions", "tool_ledger_state", "conversation_derivation_inputs"):
                if table in tables:
                    db.execute(f"DELETE FROM {table} WHERE conversation_id=?", (conversation_id,))
        if updated_conversations % 100 == 0:
            db.commit()
            print(f"{updated_conversations} conversations, {updated_messages} messages", flush=True)
    db.commit()
    return updated_conversations, updated_messages, missing


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("catalog", type=Path)
    args = parser.parse_args()
    with sqlite3.connect(args.catalog) as connection:
        conversations, messages, missing = backfill(connection)
    print(f"Backfilled {conversations} Codex conversations and {messages} messages; {missing} source files missing.")
