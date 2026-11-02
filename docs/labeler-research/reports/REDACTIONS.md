# Redactions

Each report in this folder is a sanitized copy of a research report from `~/pharos-labeling`. The full originals are
archived on euclid at "Pharos Labeling/2026-10-28-student-research" (round 1, `r1-*`) and
"Pharos Labeling/2026-10-29-bird-studies" (round 2, `r2-*`); rounds 3 and 4 (`r3-*`, `r4-*`) are under
"Pharos Labeling/2026-10-29-bird-studies/studies/". Each redaction is replaced in place by a bracketed neutral
description. Numbers, tables, methods, model names, label IDs, paths, script names and taxonomy wording are unchanged.

| file | redactions | kinds of content removed |
|---|---|---|
| r1-A-classic.md | 0 | none |
| r1-B-embeddings.md | 0 | none |
| r1-C-finetune.md | 0 | none |
| r1-D-shift.md | 5 | automation-prompt template openings and headings quoted as shift markers; codex image-attachment markup |
| r1-E-ensemble.md | 0 | none |
| r1-F-audit.md | 34 | verbatim excerpts of human messages, delegations, agent replies and automation prompts in the error-pattern examples (§2, §3); template openings and headings; relayed reviewer-feedback text; template names derived from prompt content; another project's repository name; a prompt-referenced file path; corpus phrases reused in the taxonomy recommendations |
| r2-AVOCET.md | 0 | none |
| r2-BOBOLINK.md | 0 | none |
| r2-BUFFLEHEAD.md | 0 | none |
| r2-CHICKADEE.md | 0 | none |
| r2-DOTTEREL.md | 0 | none |
| r2-DOTTEREL-PARITY.md | 0 | none (its Unicode test strings are invented fixtures, not corpus text) |
| r2-EGRET.md | 0 | none |
| r2-GANNET.md | 0 | none |
| r3-HERON.md | 9 | private repository names in the fold table, replaced by repo cluster A / B / C with counts kept; the two largest repositories' names; a spreadsheet-automation project's automation name; a JSON task-payload snippet and its key name |
| r3-IBIS.md | 0 | none (public Hugging Face dataset names, uploaders and licenses kept as provenance; no dataset conversation text was quoted) |
| r4-KESTREL.md | 0 | none (public dataset names and licenses kept as provenance) |
| r4-KESTREL-FRAMING.md | 0 | none (its tags and context headers are Pharos's own input format, not corpus text) |
| **total** | **48** | |

The frozen code snapshot in [../code/](../code/) needed no redactions; see its README.
