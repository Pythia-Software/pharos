"""Helpers for Pharos importer scripts (contract pharos-run/1).

Standard library only: copy this file beside an importer and import it. See
docs/plugins.md and contracts/plugins/ for the contract.
"""

from datetime import datetime
import json
import os
from pathlib import Path
import sys
import time
import traceback

CONTRACT = "pharos-run/1"
# Room left for result.json under the byte budget.
RESULT_RESERVE = 1 << 20


class Run:
    """One importer run: the request, its limits, and the result being built."""

    def __init__(self, request_path=None, time_margin=30):
        request_path = request_path or (sys.argv[1] if len(sys.argv) > 1 else os.environ.get("PHAROS_REQUEST"))
        if not request_path:
            raise SystemExit("usage: importer REQUEST.json (or set PHAROS_REQUEST)")
        self.request = json.loads(Path(request_path).read_text())
        if self.request.get("contract") != CONTRACT:
            raise SystemExit(f"unsupported contract {self.request.get('contract')!r}; expected {CONTRACT}")
        self.output_dir = Path(self.request["output_dir"])
        self.params = self.request.get("params") or {}
        self.cursor = self.request.get("cursor")
        self.max_bytes = int(self.request["limits"]["max_bytes"])
        self.deadline = datetime.fromisoformat(self.request["deadline"].replace("Z", "+00:00")).timestamp()
        self.time_margin = time_margin
        self.dropped_bytes = 0
        self.transferred_bytes = 0
        self.machines = []
        self.drops = []
        self.warnings = []

    def remaining_seconds(self):
        return self.deadline - time.time()

    def out_of_time(self):
        """True once the run should stop and report what it has."""
        return self.remaining_seconds() <= self.time_margin

    def fits(self, size):
        """Whether size more bytes stay within the budget."""
        return self.dropped_bytes + size <= self.max_bytes - RESULT_RESERVE

    def machine(self, key, label, os_name, machine_id=None):
        entry = {"key": key, "label": label, "os": os_name}
        if machine_id:
            entry["machine_id"] = machine_id
        self.machines.append(entry)
        return key

    def drop(self, directory, machine, kind, root, source=None):
        """Declare a drop and return its folder under output_dir."""
        self.drops.append({"dir": directory, "machine": machine, "kind": kind,
                           "source": source or kind, "root": root})
        path = self.output_dir / directory
        path.mkdir(parents=True, exist_ok=True)
        return path

    def write_file(self, drop_dir, relative, chunks, mtime=None):
        """Write a file from an iterable of byte chunks, counting its bytes.

        The file appears under its final name only once complete.
        """
        target = drop_dir / relative
        if drop_dir.resolve() not in target.resolve().parents:
            raise ValueError(f"{relative} escapes its drop")
        target.parent.mkdir(parents=True, exist_ok=True)
        partial = target.with_name(target.name + ".partial")
        size = 0
        try:
            with open(partial, "wb") as out:
                for chunk in chunks:
                    out.write(chunk)
                    size += len(chunk)
            if mtime is not None:
                os.utime(partial, (mtime, mtime))
            partial.replace(target)
        finally:
            partial.unlink(missing_ok=True)
        self.dropped_bytes += size
        return size

    def warn(self, message):
        print(f"warning: {message}", file=sys.stderr)
        self.warnings.append(message)

    def finish(self, status, message, cursor):
        """Write result.json; the script should exit 0 afterwards."""
        result = {"contract": CONTRACT, "status": status, "message": message, "cursor": cursor,
                  "transferred_bytes": self.transferred_bytes, "machines": self.machines,
                  "drops": self.drops, "warnings": self.warnings}
        partial = self.output_dir / "result.json.partial"
        partial.write_text(json.dumps(result, indent=2) + "\n")
        partial.replace(self.output_dir / "result.json")
        print(f"{status}: {message}", file=sys.stderr)


def main(body, **options):
    """Run body(run); an uncaught error becomes a failed result."""
    run = Run(**options)
    try:
        body(run)
    except Exception as error:  # noqa: BLE001 - every failure must reach result.json
        traceback.print_exc()
        run.drops = []
        run.finish("failed", f"{type(error).__name__}: {error}", run.cursor)
        sys.exit(1)
