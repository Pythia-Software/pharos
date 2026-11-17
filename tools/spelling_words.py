#!/usr/bin/env python3
"""Build internal/archive/spelling/words.txt.gz from an extracted SCOWL release.

Usage: python3 -I tools/spelling_words.py <extracted scowl-YYYY.MM.DD dir> [output]

Keeps SCOWL's english and american word and contraction lists up to size 60,
lowercased, letters and apostrophes only, one word per line, sorted. The gzip
header carries no name or time, so the same release always gives the same bytes.
See internal/archive/spelling/README.md.
"""
import gzip, io, re, sys
from pathlib import Path

LIST = re.compile(r"(english|american)-(words|contractions)\.(\d+)$")
WORD = re.compile(r"[A-Za-z][a-z']*")


def build(root: Path, max_size: int = 60) -> list[str]:
    words = set()
    for path in sorted((root / "final").iterdir()):
        match = LIST.match(path.name)
        if not match or int(match.group(3)) > max_size:
            continue
        for line in path.read_text(encoding="latin-1").splitlines():
            word = line.strip()
            if WORD.fullmatch(word):
                words.add(word.lower())
    return sorted(words)


def main() -> None:
    root = Path(sys.argv[1])
    out = Path(sys.argv[2]) if len(sys.argv) > 2 else Path(__file__).resolve().parent.parent / "internal/archive/spelling/words.txt.gz"
    data = ("\n".join(build(root)) + "\n").encode()
    buffer = io.BytesIO()
    with gzip.GzipFile(filename="", mode="wb", fileobj=buffer, compresslevel=9, mtime=0) as gz:
        gz.write(data)
    out.write_bytes(buffer.getvalue())
    print(f"{out}: {data.count(b'\n')} words, {len(buffer.getvalue())} bytes")


if __name__ == "__main__":
    main()
