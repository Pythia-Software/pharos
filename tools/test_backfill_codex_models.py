import json
import sqlite3
import tempfile
import unittest
from pathlib import Path

from backfill_codex_models import backfill, configuration_changes


class BackfillEffortTests(unittest.TestCase):
    def test_effort_changes_without_model_changes_and_explicit_reset(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "rollout.jsonl"
            events = [
                {"type": "session_meta", "payload": {"id": "session", "model": "gpt-6"}},
                {"type": "turn_context", "payload": {"model": "gpt-6", "effort": "low"}},
                {"type": "response_item", "payload": {}},
                {"type": "turn_context", "payload": {"model": "gpt-6", "reasoning_effort": "HIGH"}},
                {"type": "response_item", "payload": {}},
                {"type": "turn_context", "payload": {"model": "gpt-6", "effort": None}},
                {"type": "response_item", "payload": {}},
            ]
            path.write_text("\n".join(map(json.dumps, events)))
            lines, values = configuration_changes(path)
            self.assertEqual(lines, [1, 2, 4, 6])
            self.assertEqual(values, [("gpt-6", None), ("gpt-6", "low"), ("gpt-6", "high"), ("gpt-6", None)])
            with sqlite3.connect(":memory:") as db:
                db.executescript("""
                    CREATE TABLE workspaces(id TEXT, source_kind TEXT);
                    CREATE TABLE conversations(id TEXT, workspace_id TEXT, provider TEXT, origin TEXT, model TEXT);
                    CREATE TABLE messages(conversation_id TEXT, source_order INTEGER, evidence_locator TEXT, model TEXT);
                    CREATE TABLE agent_sessions(conversation_id TEXT);
                    CREATE TABLE tool_ledger_state(conversation_id TEXT);
                    CREATE TABLE conversation_derivation_inputs(conversation_id TEXT);
                    INSERT INTO workspaces VALUES('workspace','codex');
                """)
                db.execute("INSERT INTO conversations VALUES('session','workspace','codex',?,'gpt-6')", (str(path),))
                for order, line in enumerate([3, 5, 7]):
                    db.execute("INSERT INTO messages VALUES('session',?,?,NULL)", (order, f"{path}:{line}"))
                derived = ["agent_sessions", "tool_ledger_state", "conversation_derivation_inputs"]
                for table in derived:
                    db.execute(f"INSERT INTO {table} VALUES('session')")
                db.commit()
                self.assertEqual(backfill(db), (1, 3, 0))
                self.assertEqual(db.execute("SELECT model,effort FROM messages ORDER BY source_order").fetchall(),
                                 [("gpt-6", "low"), ("gpt-6", "high"), ("gpt-6", None)])
                for table in derived:
                    self.assertEqual(db.execute(f"SELECT count(*) FROM {table}").fetchone()[0], 0)
                    db.execute(f"INSERT INTO {table} VALUES('session')")
                db.commit()
                self.assertEqual(backfill(db), (1, 0, 0))
                for table in derived:
                    self.assertEqual(db.execute(f"SELECT count(*) FROM {table}").fetchone()[0], 1)


if __name__ == "__main__":
    unittest.main()
