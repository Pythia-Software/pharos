"""Exec a browser/e2e test process while it holds the machine's test turn."""
import fcntl
import os
import sys
import time


def main():
    path, repo, *command = sys.argv[1:]
    # Keep this file in place: replacing or unlinking it would let waiters
    # hold different inodes. The kernel releases the flock on exit or a kill.
    with open(path, "a+") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            lock.seek(0)
            owner = lock.read().strip() or "another workspace"
            print(f"# Waiting for the browser tests in {owner}; PHAROS_BROWSER_LOCK=0 skips the queue.", flush=True)
            started = time.monotonic()
            fcntl.flock(lock, fcntl.LOCK_EX)
            print(f"# Waited {round(time.monotonic() - started)} s for the browser tests.", flush=True)
        lock.seek(0)
        lock.truncate()
        lock.write(repo)
        lock.flush()
        os.set_inheritable(lock.fileno(), True)
        os.execv(command[0], command)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(130)
