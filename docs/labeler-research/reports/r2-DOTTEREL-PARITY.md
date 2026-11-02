> Sanitized copy of ~/pharos-labeling/studies/DOTTEREL/PARITY.md (archived in full on euclid at "Pharos Labeling/2026-10-29-bird-studies"). Verbatim conversation text was removed.

# DOTTEREL parity-fixture spec: featurizer + linear scorer (Python reference ⇄ Go)

The canonical implementation is Python: `scripts/dfeat.py` (tokenizer, n-grams, word-order, interaction and cue
features), `scripts/refscore.py` (per-item reference scorer over an exported model: meta2, rules, TF-IDF, dot
product) and track A's `rules.py` (the 34 regexes, copied to `scripts/rules.py`). The Go module in `go/` must give
the same logits for every item. This file is the contract; where the two differ, Python wins.

## 1. Inputs per item

| field | type | notes |
|---|---|---|
| `text` | string | model-visible CURRENT_TEXT (markers included) |
| `context` | string | the ≤600-char PREVIOUS_CONTEXT the teachers saw ("" if none) |
| `cohort` | string | human, agent_reply, agent_in_user_role, delegation |
| `provider` | string | claude, codex, antigravity |
| `kind` | string | message, delegation, delegation_result |
| `depth` | int | clipped to [0, 4] |
| `chars` | int | `len(text)` in **code points** (not bytes) |
| `chunk_index`, `chunk_count` | int | |
| `context_excerpt` | bool | the 600-char cut removed something |
| `segment` | string | one of H-typed, H-typed+ctx, H-resent, H-template, H-feedback, H-other, AIUR, AR, DEL |

All string slicing below is by **code point** (Python `s[:500]`, `s[-200:]`): in Go, slice `[]rune` or walk runes,
never bytes.

## 2. Unicode: frozen at Python 3.12 / Unicode 15.0

Go 1.27 ships Unicode **17.0** (`unicode.Version`); Python 3.12.8 uses **15.0**. Code points assigned or re-cased
between 15.0 and 17.0 would classify differently, so the Go side must **not** use package `unicode` for any decision
below. `scripts/gen_unitab.py` generates `go/labeler/unitab_gen.go` from Python itself:

| table | Python definition | size |
|---|---|---|
| `pyWord` | `re.match(r"\w", c)` (Unicode `str` pattern) = L* ∪ N* ∪ `_` in 15.0 | 748 ranges |
| `pyDigit` | `re.match(r"\d", c)` = Nd | 64 ranges |
| `pySpace` | `c.isspace()` = `re` `\s` (29 code points; includes `\x0b`, `\x1c`–`\x1f`, `\x85`, `\xa0`, U+1680, U+2000–200A, U+2028/9, U+202F, U+205F, U+3000) | 29 |
| `pyLower` | `c.lower()` where it differs from `c` | 1,433 entries; one is multi-rune: U+0130 İ → `i` + U+0307 |
| `pyCased`, `pyCaseIgnorable` | the Final_Sigma properties `str.lower` uses, derived by probing `str.lower` | 150 / 437 ranges |

### 2.1 Lowercasing (`norm`) = Python `str.lower()`

For each code point: if it is U+03A3 (Σ), emit ς (U+03C2) when it is in the Final_Sigma context, else σ (U+03C3);
otherwise emit `pyLower[c]` if present, else `c`. Final_Sigma (CPython `handle_capital_sigma`): scanning backwards
from Σ, skip `pyCaseIgnorable` code points; the first other code point must exist and be `pyCased`. Then scanning
forwards, skip `pyCaseIgnorable`; Σ is final if the string ends or the first other code point is not `pyCased`.
The scans look at the **original** (not yet lowered) string. Corpus: Σ occurs in 130 items, İ in 10, so both
matter for parity. Go's `strings.ToLower` and `unicode.ToLower` are wrong here (no final sigma, İ → `i` only,
Unicode 17 mappings).

### 2.2 Tokenizer

Python: `re.findall(r"(?u)\b\w+\b|[?!]|```|⟦", norm(s))`. Equivalent scan over the lowered string, left to right:
a maximal run of `pyWord` code points is one token; else `?` or `!` is a one-character token; else three
backticks are one token ("```"; one or two backticks are skipped); else `⟦` (U+27E6) is a token; else skip one code
point. Go's RE2 `\w` and `\b` are ASCII-only, so do not use `regexp` for this.

### 2.3 Feature strings

- `word12(s)`: tokens `t`, then unigrams `t[i]` followed by bigrams `t[i] + " " + t[i+1]` (sklearn order; the order
  does not matter for counts).
- Hashed sources:

| block | source | strings |
|---|---|---|
| `w` | text | `word12(text)` |
| `x` | context | `word12(context)` |
| `h` | `text[:500]` | `word12` |
| `xu`, `xa` | `split_context(context)` → (user part, assistant part) | `word12` of each |
| `xt` | `context[-200:]` | `word12` |
| `pos` | `tt = toks(text)` | `pos_feats(tt)`: `f{i}={tok}` (first 6), `l{i}={tok}` (last 4, i=0 is last), `fb={t0} {t1}`, `lb={t-2} {t-1}`, `nb={bin}`, `f0nb={t0}|{bin}`; bin of len(tt): 0→0, 1→1, 2→2, ≤5→3, ≤10→4, ≤20→5, ≤50→6, ≤150→7, else 8 |
| `xi` | `tt`, `ct = toks(context)` | if `len(tt) ≤ 40` and `ct` non-empty: `{a}|{b}` for a in `tt[:3]`, b in `ct[-12:]` (a-major order) |

| `pos2` | `text`, `tt`, `ct` | `pos2_feats`: `f{i}={tok}` for i = 6..9; `ft={t0} {t1} {t2}`; for each of the first 30 pieces of `norm(text)` split by the regex `[.!?\n]+` (Python `re.split`, empty pieces kept but yield nothing): tokens of the piece minus `?`/`!` tokens, `s0={p0}` and `s01={p0} {p1}`; then `c{i}={tok}` for the last 8 context tokens, i=0 is the last |
| `chH` | `text`, **only when cohort == human** (else no strings) | sklearn `char_wb` 2–5: `norm(text).split()` (Python `str.split()`: runs of `pySpace`), each word padded `" "+w+" "`, then for n = 2..5 every n-gram of code points (`w[off:off+n]`, off = 0.. while off+n < len); if the padded word is shorter than n, emit it once and stop increasing n |

- `split_context(c)`: `a = c.rfind("Previous assistant message:\n")`. If found: user part = text between the first
  `"Previous user request:\n"` (if it occurs before `a`) and `a`, else ""; assistant part = after the header. Else if
  `"Previous user request:\n"` occurs: user part = after it, assistant = "". Else user = "", assistant = whole c.

### 2.4 Hashing (sklearn `FeatureHasher`/`HashingVectorizer`, `alternate_sign=False`)

`h = murmur3_x86_32(utf8(feature_string), seed=0)` read as **signed int32**; index `= abs(h) % n_features`, except
`h == -2^31` → `(2^31 - 1 - (n_features - 1)) % n_features`. Count repeats (a string occurring k times gives count k).

### 2.5 TF-IDF per block

For each hashed index with an entry in the block table (all indices with train document frequency ≥ 3):
`v = float32(log1p(float32(count))) * idf` (float32). Block norm `‖v‖₂` over **all table entries present**
(including entries whose `col` is −1: the norm was computed before the 200k/300k column cap); `x[col] = v / norm`
(norm 0 → 1). Entries with `col = −1` contribute to the norm only. A copy column `copycol[g] ≥ 0` gets the same
value when the item's segment is in group g (`model.json` → `groups`).

### 2.6 Dense blocks

- `meta2` (33, order in `model.json` → `meta2_cols`): cohort/provider/kind one-hots; `depth/4`;
  `float32(log1p(float32(chars)))/9`; `chars<10,30,100,300,1000,3000`; chunk flags (`chunked`: count>1; first:
  index 0; last: index count−1; mid: otherwise, all only when chunked); `ctx_empty`; `ctx_excerpt`; `ctx_has_user`
  (`"Previous user request"` substring); `ctx_ends_q` (`context.rstrip()` ends with `?`, **rstrip strips
  `pySpace`**, not Go's `unicode.IsSpace`, which lacks `\x1c`–`\x1f`); `n_q = log1p(count "?")/3`; `ends_q`;
  `n_marker = log1p(count "⟦")/3`; `n_fence = log1p(non-overlapping count "```")/3`; `n_lines = log1p(count "\n")/6`;
  `n_bullets = log1p(#non-overlapping matches of (?m)^\s*(?:[-*]|\d+\.)\s)/4` (Unicode `\s`, `\d`: use the shadow
  string of §2.7); `json_start` (`text.lstrip()` starts with `{` or `[`). log1p in float64, cast to float32, divide in float32.
- `rules` (34): rule j fires (1.0) when the item's cohort is in its cohort set (or the set is None) and
  `re.search(pattern, text[:3000], re.I | re.M)`.
- `cue` (13): `dfeat.cue_feats(tt, ct, text)`; word lists in `dfeat.py`.
- `seg` (9): one-hot of `segment` in `seg_order`.

### 2.7 Regex rules in Go: the ASCII shadow string

Go `regexp` is RE2: `\w`, `\d`, `\s`, `\b` are ASCII-only, and `\s` lacks `\v`. Python's are Unicode. Run the
same pattern text (with `(?im)` prefix) on a shadow of `text[:3000]` built per code point:

| code point | shadow |
|---|---|
| ASCII except `\v` | itself |
| `\v` and every other `pySpace` code point | `' '` (but `\n` stays `\n`, so `^`/`$` with `(?m)` match the same lines) |
| non-ASCII `pyDigit` | `'0'` |
| İ (U+0130), ı (U+0131) | `'i'`; ſ (U+017F) → `'s'`; K (U+212A, Kelvin) → `'k'` (Python `re.I` matches these with the ASCII letters) |
| other non-ASCII `pyWord` | `'_'` (an ASCII word char no rule pattern contains) |
| anything else (symbols such as ✓ ✅ ’ …) | itself |

This makes `\b`, `\w`, `\d`, `\s` and case-insensitive ASCII literals decide identically. Leftmost-first RE2 match
existence equals Python's for these patterns (no backreferences or lookarounds). The same shadow is used for
`n_bullets`.

## 3. Scorer

`logit = b + Σ_col x[col] · W[col, :]` (accumulate in float64 or float32; see tolerance), `p = 1/(1+e^−logit)`.
Thresholds: `model.json` → `thresholds` (default vector, optionally per segment group); binary = `p ≥ θ`, then
close upwards (`parent` indices: a predicted child adds all ancestors).

## 4. Tolerance

Python computes TF-IDF in float32 with scipy/numpy summation order; Go sums in another order. Expected logit
difference ≤ ~1e-5 (float32 rounding of ≤ a few hundred terms of magnitude ≤ 1, with |W| ≲ 10). Gate:
**max |Δlogit| ≤ 1e-4 and max |Δp| ≤ 2e-5 on every eval item**, and report the number of thresholded label flips
(expected 0; any flip must come from a probability within 2e-5 of its threshold).

## 5. Test strings (fixtures)

`go/testdata/fixtures.jsonl` holds hand-written edge cases with Python's tokens, lowered string, hashed indices and
logits (generated by `scripts/fixtures.py`): empty text; ASCII; `İstanbul`; `ΟΔΟΣ ΣΑΣ` (final sigma); `Σ` alone;
`aΣ.` / `aΣb`; fullwidth `ＡＢＣ`; `²` (No, a word char); Kelvin `K`; `ſ`; combining marks `é` (e + U+0301);
emoji; `‘curly’ quotes`; `\x1c`/`\x0b`/U+00A0/U+3000 whitespace (rstrip, bullets); `٣.` bullet (Arabic-Indic digit);
two/three/four backticks; `⟦marker⟧`; CRLF; a Unicode 16/17-only letter (U+1C89, assigned in 16.0: a letter in Go's
tables, not `\w` in Python 3.12); a 600-char context with both headers; a context with only the user header.
