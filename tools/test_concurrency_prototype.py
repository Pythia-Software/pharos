import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("concurrency", Path(__file__).with_name("concurrency-prototype.py"))
concurrency = importlib.util.module_from_spec(spec)
spec.loader.exec_module(concurrency)


def message(role, kind, at):
    return {"role": role, "kind": kind, "created_at": at}


class TimingTests(unittest.TestCase):
    def test_tool_waits_are_not_model_time_but_are_part_of_a_user_turn(self):
        messages = [message("user", "message", "2026-10-08T10:00:00Z"),
                    message("assistant", "tool_call", "2026-10-08T10:00:10Z"),
                    message("tool", "tool_result", "2026-10-08T10:01:00Z"),
                    message("assistant", "message", "2026-10-08T10:01:15Z")]
        model, turns, missing = concurrency.activity(messages)
        self.assertEqual(sum(b - a for a, b in model), 25_000)
        self.assertEqual(sum(b - a for a, b in turns), 75_000)
        self.assertEqual(missing, 0)

    def test_unanswered_prompts_do_not_invent_runtime(self):
        self.assertEqual(concurrency.activity([message("user", "message", "2026-10-08T10:00:00Z")]), ([], [], 0))

    def test_long_silence_is_neither_inference_nor_continuous_work(self):
        messages = [message("user", "message", "2026-10-08T10:00:00Z"),
                    message("assistant", "message", "2026-10-08T10:00:10Z"),
                    message("assistant", "message", "2026-10-08T14:00:00Z"),
                    message("assistant", "tool_call", "2026-10-08T14:00:10Z")]
        model, turns, _ = concurrency.activity(messages)
        self.assertEqual(sum(b - a for a, b in model), 10_000)
        self.assertEqual(sum(b - a for a, b in turns), 20_000)

    def test_queueing_longer_than_ten_minutes_is_reported_and_omitted(self):
        model, turns, missing = concurrency.activity([message("user", "message", "2026-10-08T10:00:00Z"), message("assistant", "message", "2026-10-08T10:15:00Z")])
        self.assertEqual(model, [])
        self.assertEqual(len(turns), 1)
        self.assertEqual(missing, 1)


if __name__ == "__main__":
    unittest.main()
