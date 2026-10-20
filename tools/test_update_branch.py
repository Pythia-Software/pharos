"""Exercise branch updates against disposable local Git repositories."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("update-branch.py")
BUNDLE = "internal/archive/assets/query-tables.js"
CSS = "internal/archive/assets/query-tables.css"


class BranchUpdateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.upstream, self.repo = self.root / "upstream", self.root / "worktree"
        self.env = {**os.environ, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
                    "GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.invalid",
                    "GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.invalid",
                    "GIT_TERMINAL_PROMPT": "0"}
        # Do not inherit worktree/index overrides from the invoking harness.
        for name in ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"):
            self.env.pop(name, None)
        self.upstream.mkdir()
        self.git("init", "-q", "--initial-branch=master", cwd=self.upstream)
        self.write(self.upstream, "source-a", "base a\n")
        self.write(self.upstream, "source-b", "base b\n")
        self.write(self.upstream, BUNDLE, "base bundle\n")
        self.write(self.upstream, CSS, "base css\n")
        self.write(self.upstream, ".gitignore", f"/{BUNDLE}\n/{CSS}\n/checks.log\n")
        self.git("add", ".", cwd=self.upstream)
        self.git("add", "-f", BUNDLE, CSS, cwd=self.upstream)
        self.git("commit", "-qm", "base", cwd=self.upstream)
        self.git("clone", "-q", str(self.upstream), str(self.repo), cwd=self.root)
        self.git("checkout", "-qb", "feature")
        # Same checkout contract as setup.sh, with cheap deterministic checks.
        setup = self.write(self.repo, "tools/setup.sh", f"""#!{sys.executable}
import os
from pathlib import Path
Path('checks.log').write_text('checked')
if os.environ.get('PHAROS_UPDATE_TEST_FAIL_CHECKS'):
    raise SystemExit(1)
if os.environ.get('PHAROS_UPDATE_TEST_REQUIRE_HELPER'):
    Path('new-helper.py').read_text()
Path('{BUNDLE}').parent.mkdir(parents=True, exist_ok=True)
Path('{BUNDLE}').write_text(Path('source-a').read_text() + Path('source-b').read_text())
Path('{CSS}').write_text('rebuilt css')
if os.environ.get('PHAROS_UPDATE_TEST_DIRTY_CHECKS'):
    Path('source-a').write_text('changed during checks')
if os.environ.get('PHAROS_UPDATE_TEST_UNTRACKED_CHECKS'):
    Path('new-helper.py').write_text('required helper')
""")
        setup.chmod(0o755)
        self.git("add", "tools/setup.sh")
        self.git("rm", BUNDLE, CSS)
        self.git("commit", "-qm", "ignore generated bundles")

    def git(self, *args, cwd=None, check=True):
        return subprocess.run(["git", *args], cwd=cwd or self.repo, env=self.env,
                              text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=check)

    def write(self, root, name, text):
        file = root / name
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)
        return file

    def commit(self, root, name, text):
        self.write(root, name, text)
        self.git("add", name, cwd=root)
        self.git("commit", "-qm", f"change {name}", cwd=root)

    def run_update(self, *args, extra_env=None):
        return subprocess.run([sys.executable, str(SCRIPT), *args], cwd=self.repo,
                              env={**self.env, **(extra_env or {})}, text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    def assert_merge_pending(self, head):
        self.assertEqual(self.git("rev-parse", "HEAD").stdout, head)
        self.assertEqual(self.git("rev-parse", "-q", "--verify", "MERGE_HEAD").returncode, 0)

    def test_merges_disjoint_source_changes_and_rebuilds_ignored_assets(self):
        self.commit(self.repo, "source-a", "feature a\n")
        self.commit(self.upstream, "source-b", "master b\n")
        result = self.run_update()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.repo / BUNDLE).read_text(), "feature a\nmaster b\n")
        self.assertEqual(self.git("status", "--porcelain").stdout, "")
        self.assertEqual(len(self.git("rev-list", "--parents", "-n", "1", "HEAD").stdout.split()), 3)
        self.assertEqual(self.git("ls-files", BUNDLE, CSS).stdout, "")
        # The command never pushes its merge back to the remote.
        self.assertEqual(self.git("rev-parse", "HEAD", cwd=self.upstream).stdout,
                         self.git("rev-parse", "origin/master").stdout)
        self.assertNotEqual(self.git("show-ref", "--verify", "refs/heads/feature",
                                     cwd=self.upstream, check=False).returncode, 0)

    def test_discards_legacy_bundle_conflict_before_rebuilding(self):
        self.commit(self.upstream, BUNDLE, "master's obsolete bundle\n")
        self.commit(self.upstream, CSS, "master's obsolete css\n")
        result = self.run_update()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.git("ls-files", BUNDLE, CSS).stdout, "")
        self.assertEqual((self.repo / BUNDLE).read_text(), "base a\nbase b\n")

    def test_discards_both_tracked_versions_of_a_generated_bundle(self):
        self.write(self.repo, BUNDLE, "feature bundle\n")
        self.git("add", "-f", BUNDLE)
        self.git("commit", "-qm", "legacy branch still tracks bundle")
        self.commit(self.upstream, BUNDLE, "master bundle\n")
        result = self.run_update()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.git("ls-files", BUNDLE).stdout, "")
        self.assertEqual((self.repo / BUNDLE).read_text(), "base a\nbase b\n")

    def test_stops_for_source_conflict_and_continues_staged_resolution(self):
        self.commit(self.repo, "source-a", "feature a\n")
        self.commit(self.upstream, "source-a", "master a\n")
        self.commit(self.upstream, BUNDLE, "legacy bundle\n")
        head = self.git("rev-parse", "HEAD").stdout
        result = self.run_update()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("source-a", result.stderr)
        self.assert_merge_pending(head)
        self.assertFalse((self.repo / "checks.log").exists())
        self.write(self.repo, "source-a", "feature and master\n")
        result = self.run_update("--continue")
        self.assertNotEqual(result.returncode, 0, "unstaged resolutions must not be committed")
        self.git("add", "source-a")
        self.write(self.repo, "new-helper.py", "required helper")
        result = self.run_update("--continue", extra_env={"PHAROS_UPDATE_TEST_REQUIRE_HELPER": "1"})
        self.assertNotEqual(result.returncode, 0, "untracked helpers must not be omitted from the merge")
        self.assertIn("new-helper.py", result.stderr)
        self.assert_merge_pending(head)
        self.assertFalse((self.repo / "checks.log").exists(), "reject untracked inputs before checking")
        self.git("add", "new-helper.py")
        result = self.run_update("--continue", extra_env={"PHAROS_UPDATE_TEST_REQUIRE_HELPER": "1"})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.repo / BUNDLE).read_text(), "feature and master\nbase b\n")
        self.assertEqual(self.git("show", "HEAD:new-helper.py").stdout, "required helper")
        self.assertEqual(self.git("status", "--porcelain").stdout, "")

    def test_failed_checks_leave_the_merge_uncommitted_and_retryable(self):
        self.commit(self.upstream, "source-b", "master b\n")
        head = self.git("rev-parse", "HEAD").stdout
        result = self.run_update(extra_env={"PHAROS_UPDATE_TEST_FAIL_CHECKS": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--continue", result.stderr)
        self.assert_merge_pending(head)
        self.assertEqual(self.run_update("--continue").returncode, 0)

    def test_does_not_commit_changes_made_by_checks(self):
        self.commit(self.upstream, "source-b", "master b\n")
        head = self.git("rev-parse", "HEAD").stdout
        result = self.run_update(extra_env={"PHAROS_UPDATE_TEST_DIRTY_CHECKS": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Checks changed tracked files", result.stderr)
        self.assert_merge_pending(head)

    def test_does_not_omit_untracked_files_created_during_checks(self):
        self.commit(self.upstream, "source-b", "master b\n")
        head = self.git("rev-parse", "HEAD").stdout
        result = self.run_update(extra_env={"PHAROS_UPDATE_TEST_UNTRACKED_CHECKS": "1"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("new-helper.py", result.stderr)
        self.assert_merge_pending(head)
        self.assertTrue((self.repo / "checks.log").exists(), "checks ran before the helper was created")
        self.git("add", "new-helper.py")
        result = self.run_update("--continue", extra_env={"PHAROS_UPDATE_TEST_REQUIRE_HELPER": "1"})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.git("show", "HEAD:new-helper.py").stdout, "required helper")

    def test_refuses_dirty_worktree_without_starting_a_merge(self):
        self.commit(self.upstream, "source-b", "master b\n")
        self.write(self.repo, "untracked-note", "user's work")
        head = self.git("rev-parse", "HEAD").stdout
        result = self.run_update()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Commit or stash", result.stderr)
        self.assertEqual(self.git("rev-parse", "HEAD").stdout, head)
        self.assertNotEqual(self.git("rev-parse", "-q", "--verify", "MERGE_HEAD", check=False).returncode, 0)

    def test_up_to_date_and_continue_without_a_merge_do_not_commit(self):
        head = self.git("rev-parse", "HEAD").stdout
        self.assertEqual(self.run_update().returncode, 0)
        self.assertFalse((self.repo / "checks.log").exists())
        self.assertNotEqual(self.run_update("--continue").returncode, 0)
        self.assertEqual(self.git("rev-parse", "HEAD").stdout, head)


if __name__ == "__main__":
    unittest.main()
