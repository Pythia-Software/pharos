"""Browser test turns stay exclusive when their previous owner is killed."""
import os
from pathlib import Path
import select
import shutil
import subprocess
import sys
import tempfile
import time
import unittest


HELPER = Path(__file__).resolve().parents[1] / "web/tests/browser-turn.py"
NODE = shutil.which("node")
HOLD = "console.log('entered'); process.stdin.resume(); process.stdin.once('data', () => process.exit(0));"
GUARDED = """
const fs = require('node:fs'), marker = process.argv[1];
fs.closeSync(fs.openSync(marker, 'wx'));
console.log('entered');
process.stdin.resume();
process.stdin.once('data', () => { fs.unlinkSync(marker); process.exit(0); });
"""


@unittest.skipUnless(NODE, "Node is required to check lock inheritance across exec")
class BrowserTurnTests(unittest.TestCase):
    def start(self, lock, script, *args):
        child = subprocess.Popen(
            [sys.executable, str(HELPER), str(lock), "test workspace", NODE, "-e", script, *map(str, args)],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, bufsize=0,
        )

        def cleanup():
            if child.poll() is None:
                child.kill()
            child.wait(timeout=10)
            child.stdin.close()
            child.stdout.close()

        self.addCleanup(cleanup)
        return child

    def line(self, child):
        # Read only one line, without buffering later messages outside select.
        # Pipes acknowledge acquisition/waiting; no timing sleeps decide order.
        line = bytearray()
        deadline = time.monotonic() + 10
        while True:
            ready, _, _ = select.select([child.stdout], [], [], max(0, deadline - time.monotonic()))
            self.assertTrue(ready, f"Timed out waiting for process {child.pid}: {line!r}")
            byte = os.read(child.stdout.fileno(), 1)
            self.assertTrue(byte, f"Process {child.pid} exited early ({child.poll()}): {line.decode()}")
            if byte == b"\n":
                return line.decode()
            line.extend(byte)

    def entered(self, child):
        line = self.line(child)
        while line.startswith("#"):
            line = self.line(child)
        self.assertEqual(line, "entered")

    def release(self, child):
        child.stdin.write(b"release\n")
        child.stdin.flush()
        self.assertEqual(child.wait(timeout=10), 0)

    def test_killed_owner_wakes_contenders_one_at_a_time(self):
        with tempfile.TemporaryDirectory() as directory:
            lock, marker = Path(directory) / "turn", Path(directory) / "occupied"
            owner = self.start(lock, HOLD)
            self.entered(owner)
            waiters = [self.start(lock, GUARDED, marker) for _ in range(2)]
            for waiter in waiters:
                self.assertTrue(self.line(waiter).startswith("# Waiting"))

            # Both contenders are waiting when the owner dies. Each contender
            # must keep the other out until its own executed Node process exits.
            owner.kill()
            owner.wait(timeout=10)
            ready, _, _ = select.select([waiter.stdout for waiter in waiters], [], [], 10)
            self.assertTrue(ready, "No contender acquired the killed owner's turn")
            first = next(waiter for waiter in waiters if waiter.stdout is ready[0])
            second = next(waiter for waiter in waiters if waiter is not first)
            self.entered(first)
            self.release(first)
            self.entered(second)
            self.release(second)
            self.assertFalse(marker.exists())

    def test_failed_test_exit_releases_the_turn_and_preserves_exit_status(self):
        with tempfile.TemporaryDirectory() as directory:
            lock = Path(directory) / "turn"
            failure = self.start(lock, "process.exit(7)")
            self.assertEqual(failure.wait(timeout=10), 7)
            next_run = self.start(lock, HOLD)
            self.entered(next_run)
            self.release(next_run)


if __name__ == "__main__":
    unittest.main()
