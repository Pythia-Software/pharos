#!/usr/bin/env python3
"""Project the TL1 missing-root-timing cohort into a new disposable SQLite file.

Reads the source with mode=ro, in one consistent snapshot. This is a validation
fixture, not a recoverable library backup: message bodies and unrelated tables
are omitted. See docs/session-time-bounds.md.
"""

import argparse
import json
from pathlib import Path
import sqlite3


def copy_cohort(source, destination):
    destination = Path(destination).resolve()
    with destination.open("xb"):
        pass  # Never overwrite an existing database.
    with sqlite3.connect(destination, uri=True) as db:
        db.execute("ATTACH DATABASE ? AS original", (Path(source).resolve().as_uri() + "?mode=ro",))
        db.execute("BEGIN")
        db.execute("CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL)")
        db.execute("""CREATE TABLE conversations AS SELECT c.* FROM original.conversations c
            JOIN original.workspaces w ON w.id=c.workspace_id
            WHERE w.source_kind='tl1' AND c.started_at IS NOT NULL AND c.ended_at IS NOT NULL
            AND EXISTS(SELECT 1 FROM original.agent_sessions s WHERE s.conversation_id=c.id
                AND s.native_id='main' AND COALESCE(s.started_at,'')='')""")
        db.execute("CREATE UNIQUE INDEX conversations_id ON conversations(id)")
        db.execute("CREATE TABLE agent_sessions AS SELECT * FROM original.agent_sessions WHERE conversation_id IN (SELECT id FROM conversations)")
        db.execute("CREATE UNIQUE INDEX sessions_id ON agent_sessions(id)")
        db.execute("CREATE TABLE messages AS SELECT id,conversation_id,created_at FROM original.messages WHERE conversation_id IN (SELECT id FROM conversations)")
        db.execute("CREATE INDEX messages_conversation_idx ON messages(conversation_id)")
        db.execute("CREATE TABLE tl1_attempts AS SELECT * FROM original.tl1_attempts WHERE conversation_native_id IN (SELECT native_id FROM conversations)")
        db.execute("CREATE INDEX attempts_conversation_idx ON tl1_attempts(conversation_native_id)")
        for table, where in {
            "agent_session_usage": "agent_session_id IN (SELECT id FROM agent_sessions)",
            "model_requests": "conversation_id IN (SELECT id FROM conversations)",
            "tool_calls": "conversation_id IN (SELECT id FROM conversations)",
            "usage_attribution_state": "workspace_id IN (SELECT workspace_id FROM conversations)",
        }.items():
            db.execute(f"CREATE TABLE {table} AS SELECT * FROM original.{table} WHERE {where}")
        for table, column, definition in [
            ("conversations", "session_bounds_json", "TEXT"),
            ("agent_sessions", "time_bounds_source", "TEXT NOT NULL DEFAULT 'unavailable'"),
        ]:
            columns = {row[1] for row in db.execute(f"PRAGMA table_info({table})")}
            if column not in columns:
                db.execute(f"ALTER TABLE {table} ADD COLUMN {column} {definition}")
        db.execute("CREATE TABLE session_bounds_disposable_copy(purpose TEXT)")
        db.execute("INSERT INTO session_bounds_disposable_copy VALUES('session-bounds-repair-validation')")
        counts = {table: db.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
                  for table in ["conversations", "agent_sessions", "messages", "tl1_attempts", "agent_session_usage", "model_requests", "tool_calls"]}
    return counts


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source")
    parser.add_argument("destination")
    args = parser.parse_args()
    print(json.dumps(copy_cohort(args.source, args.destination), sort_keys=True))
