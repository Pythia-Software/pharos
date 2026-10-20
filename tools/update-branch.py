#!/usr/bin/env python3
"""Merge origin/master, rebuild the frontend, check, and commit; never push.

Resolve and stage source conflicts, then rerun with --continue. Legacy tracked
bundles are discarded only after every source conflict has been resolved.
"""
import argparse
import os
from pathlib import Path
import subprocess
import sys


BUNDLES = (
    "internal/archive/assets/query-tables.js",
    "internal/archive/assets/query-tables.css",
)


def git(*args, capture=False, check=True):
    return subprocess.run(["git", *args], check=check, text=True,
                          stdout=subprocess.PIPE if capture else None)


def merging():
    return git("rev-parse", "-q", "--verify", "MERGE_HEAD", capture=True, check=False).returncode == 0


def require_tracked_inputs():
    # diff --quiet covers tracked edits only. A new helper can make checks pass
    # while remaining absent from the merge commit. Ignore build outputs using
    # Git's ignore rules, just as the initial clean-worktree check does.
    untracked = git("ls-files", "--others", "--exclude-standard", "-z", capture=True).stdout
    if untracked:
        files = "\n  ".join(path for path in untracked.split("\0") if path)
        raise RuntimeError("Stage or remove these untracked files, then run tools/update-branch.py --continue:\n  " + files)


def update(continuing):
    root = git("rev-parse", "--show-toplevel", capture=True).stdout.strip()
    os.chdir(root)
    if continuing:
        if not merging():
            raise RuntimeError("No merge is in progress. Run tools/update-branch.py first.")
    else:
        if merging():
            raise RuntimeError("A merge is already in progress. Resolve and stage source conflicts, then use --continue.")
        # Untracked files matter too: a merge may overwrite them. Never stash or
        # stage the user's work automatically.
        if git("status", "--porcelain", "--untracked-files=normal", capture=True).stdout:
            raise RuntimeError("Commit or stash your changes before updating the branch.")
        if not git("symbolic-ref", "-q", "HEAD", capture=True, check=False).stdout:
            raise RuntimeError("Check out your feature branch before updating it.")
        for state in ("rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD", "REVERT_HEAD"):
            path = git("rev-parse", "--git-path", state, capture=True).stdout.strip()
            if Path(path).exists():
                raise RuntimeError("Finish the current Git operation before updating the branch.")
        git("fetch", "origin", "master")
        result = git("merge", "--no-commit", "--no-ff", "origin/master", check=False)
        if not merging():
            if result.returncode:
                raise RuntimeError("Git could not start the merge; see its output above.")
            print("Already up to date.")
            return

    conflicts = set(git("diff", "--name-only", "--diff-filter=U", "-z", capture=True).stdout.split("\0")) - {""}
    sources = conflicts - set(BUNDLES)
    if sources:
        raise RuntimeError("Resolve and stage these source conflicts, then run tools/update-branch.py --continue:\n  "
                           + "\n  ".join(sorted(sources)))

    # During migration, master or an older branch may still modify the retired
    # bundles. Keep their deletion, then rebuild from the merged source. Never
    # choose one branch's compiled code as the final asset.
    tracked = git("ls-files", "-z", "--", *BUNDLES, capture=True).stdout
    if tracked:
        git("rm", "-f", "--ignore-unmatch", "--", *BUNDLES)
    if git("diff", "--quiet", check=False).returncode:
        raise RuntimeError("Stage your source resolutions before running --continue.")
    require_tracked_inputs()

    # setup selects Node 24 even from a non-interactive shell, installs pinned
    # packages, rebuilds assets, and runs the frontend checks and Go fast tier.
    try:
        subprocess.run([str(Path(root) / "tools/setup.sh"), "--check"], check=True)
    except subprocess.CalledProcessError:
        raise RuntimeError("Checks failed. Fix the problem and stage any source changes, then run tools/update-branch.py --continue.") from None
    require_tracked_inputs()
    if git("diff", "--quiet", check=False).returncode:
        raise RuntimeError("Checks changed tracked files. Review and stage them, then rerun with --continue.")
    git("commit", "--no-edit")
    print("Merged and checked origin/master. Review the merge, then push when ready.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--continue", dest="continuing", action="store_true",
                        help="finish an existing merge after staging source resolutions")
    args = parser.parse_args()
    try:
        update(args.continuing)
    except (RuntimeError, subprocess.CalledProcessError, OSError) as error:
        print(f"Branch update stopped: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
