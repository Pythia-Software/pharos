#!/usr/bin/env python3
"""Opt-in, synthetic Claude export lifecycle measurement; no live library inputs.

Run from any directory: python3 tools/claude-export-probe.py
Requires workspace setup. Results go to .context/claude-export-probe.json.
Instrumentation exists only in a temporary Go overlay, never production sources.
"""

import json
import pathlib
import subprocess
import tempfile


ROOT = pathlib.Path(__file__).resolve().parents[1]


def main():
    with tempfile.TemporaryDirectory(prefix="pharos-claude-probe-") as temporary:
        directory = pathlib.Path(temporary)
        replacements = {}

        def overlay(relative, changes, suffix=""):
            original = ROOT / relative
            source = original.read_text()
            for old, new in changes:
                if source.count(old) != 1:
                    raise RuntimeError(f"probe instrumentation needs review: {relative}: {old!r}")
                source = source.replace(old, new)
            target = directory / original.name
            target.write_text(source + suffix)
            replacements[str(original)] = str(target)

        overlay("internal/archive/adapter_claude_ai.go", [
            ("func decodeClaudeConversations(content io.Reader, emit func(claudeAIConversation) error) error {",
             'func decodeClaudeConversations(content io.Reader, emit func(claudeAIConversation) error) error {\n content = io.TeeReader(content, claudeProbeCounter("decoded_bytes"))'),
            ('\t\tif conversation.UUID == "" || seen[conversation.UUID] {',
             '\t\tclaudeProbeCounts["decoded_conversations"]++\n claudeProbeCounts["decoded_messages"] += int64(len(conversation.Messages))\n\t\tif conversation.UUID == "" || seen[conversation.UUID] {'),
        ], '''
// Temporary measurement overlay, compiled only by tools/claude-export-probe.py.
var claudeProbeCounts = map[string]int64{}
type claudeProbeCounter string
func (c claudeProbeCounter) Write(p []byte) (int, error) {
    claudeProbeCounts[string(c)] += int64(len(p)); return len(p), nil
}
type claudeProbeReaderAt struct { io.ReaderAt }
func (r claudeProbeReaderAt) ReadAt(p []byte, off int64) (int, error) {
    n, err := r.ReaderAt.ReadAt(p, off); claudeProbeCounts["zip_read_at_bytes"] += int64(n); return n, err
}
''')
        overlay("internal/archive/claude_export.go", [
            ('io.Copy(hash, claudeContextReader{files.context(), file})',
             'io.Copy(io.MultiWriter(hash, claudeProbeCounter("verified_archive_bytes")), claudeContextReader{files.context(), file})'),
            ('\t\tcloseErr := reader.Close()\n\t\tif readErr != nil || closeErr != nil || n != int64(entry.UncompressedSize64) {',
             '\t\tclaudeProbeCounts["verified_expanded_bytes"] += n\n\t\tcloseErr := reader.Close()\n\t\tif readErr != nil || closeErr != nil || n != int64(entry.UncompressedSize64) {'),
        ])
        overlay("internal/archive/adapter_exports.go", [
            ('return zip.NewReader(at, size)', 'return zip.NewReader(claudeProbeReaderAt{at}, size)'),
        ])
        overlay("internal/archive/capture_fs.go", [
            ('func (s sourceFiles) Open(path string) (*sourceFile, error) {',
             'func (s sourceFiles) Open(path string) (*sourceFile, error) {\n claudeProbeCounts["source_opens"]++'),
            ('func (s sourceFiles) Stat(path string) (fs.FileInfo, error) {',
             'func (s sourceFiles) Stat(path string) (fs.FileInfo, error) {\n claudeProbeCounts["source_stats"]++'),
        ])
        fixture = ROOT / "tools/fixtures/claude-export-probe.go.txt"
        virtual = ROOT / "internal/archive/claude_lifecycle_probe_test.go"
        target = directory / virtual.name
        target.write_text(fixture.read_text())
        replacements[str(virtual)] = str(target)
        manifest = directory / "overlay.json"
        manifest.write_text(json.dumps({"Replace": replacements}))
        command = ["go", "test", "-overlay", str(manifest), "-tags", "probe", "-vet=off",
                   "-run", "^TestClaudeExportLifecycleProbe$", "-count=1", "-v",
                   "-timeout", "15m", "./internal/archive"]
        result = subprocess.run(command, cwd=ROOT, text=True, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, check=False)
        output = ROOT / ".context/claude-export-probe.log"
        output.parent.mkdir(exist_ok=True)
        output.write_text(result.stdout)
        print("\n".join(line for line in result.stdout.splitlines()
                        if "CLAUDE_PROBE_RESULT " not in line))
        if result.returncode:
            raise SystemExit(result.returncode)
        marker = "CLAUDE_PROBE_RESULT "
        records = [json.loads(line.split(marker, 1)[1]) for line in result.stdout.splitlines()
                   if marker in line]
        if len(records) != 4:
            raise RuntimeError(f"expected four scenarios, got {len(records)}")
        destination = ROOT / ".context/claude-export-probe.json"
        destination.write_text(json.dumps(records, indent=2) + "\n")
        print(f"Results: {destination}")


if __name__ == "__main__":
    main()
