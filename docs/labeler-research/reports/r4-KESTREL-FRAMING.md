> Sanitized copy of ~/pharos-labeling/studies/KESTREL/scripts/FRAMING.md (archived in full on euclid at "Pharos Labeling/2026-10-29-bird-studies/studies/KESTREL"; the same file ships as `export/kestrel-68m/FRAMING.md`). Verbatim conversation text was removed.

# Input framing contract `kestrel-framing-v1`

This is the exact input construction the KESTREL encoders were trained with. A runtime must reproduce it token for
token; `parity-fixture.json` checks that. `kframe.py` is the executable statement of this contract (pure Python plus the
`tokenizers` library), and `reference.py` runs it end to end with onnxruntime.

## 1. Inputs per item

Every item comes from `pharos labeling prepare` (`labeling-units-v1`, `human-pieces-v2`, `context-v3`, `chunking-v1`):

| field | source | notes |
|---|---|---|
| `text` | the item's visible text | target pieces only, with `⟦…⟧` markers in place of context/dropped material, exactly as the teacher saw it; chunks of long units are separate items (≤ 6,000 characters) |
| `context` | the unit's `context_prompt` | `Previous user request: …` (last 1,400 characters) then `Previous assistant message: …` (last 800 characters); may be empty |
| `cohort` | the unit's cohort | `human`, `agent_in_user_role`, `agent_reply`, `delegation`, `thinking` |
| `sender` | the unit's **`sender_role`** column (`units.sender_role`), **not** `units.sender` | `user` (human, AIUR), `assistant` (AR, delegation prompts, thinking), `agent` (some AR), `tool` (delegation results); empty only if the role is missing. `units.sender` is an identity field (NULL, `automation:*`, `account:*`) and must not be used: framing with it costs up to 0.09 J on DEL/TH. A value starting with `account:` would become `account` |
| `segment` | AVOCET's segment rule, known before labeling | `H-typed`, `H-typed+ctx`, `H-template`, `H-resent`, `H-feedback` (the human segments), `AIUR`, `AR`, `DEL`, `TH` |

Segment rule (AVOCET): cohort `agent_in_user_role` → AIUR, `agent_reply` → AR, `delegation` → DEL, `thinking` → TH.
Human items use piece statuses inside the item's visible range: any `estimated_typed` target → `H-typed` if that is the
only target status and there is no context piece (`likely_pasted`, `quoted`, `attachment`, `reused_unverified`), else
`H-typed+ctx`; otherwise `feedback_comment` → `H-feedback`, `reused_template` → `H-template`, `reused_own_message` →
`H-resent`.

All string lengths and slices below count **Unicode code points** (Python `str` indexing), not bytes or UTF-16 units.
No Unicode normalization is applied anywhere: text goes to the tokenizer as stored (CRLF, tabs, NBSP, zero-width
characters, combining marks, emoji sequences unchanged).

## 2. Token sequence

Tokenizer: `tokenizer.json` (ModernBERT/ettin BPE; SHA-256 in `manifest.json`). Every string is tokenized on its own
**without special tokens** (`add_special_tokens=False`). Special ids: `[CLS]` 50281, `[SEP]` 50282, `[PAD]` 50283.

```
[CLS]  tag  context  [SEP]  text  [SEP]          (at most 512 tokens in total)
```

1. **Tag** = tokens of `f"{cohort} {sender}".strip() + " :"`, e.g. `human user :`, `agent_reply assistant :`,
   `delegation tool :`, `thinking assistant :`.
2. **Text pre-cut (all segments).** If `text` has more than 4,096 code points, it becomes
   `text[:2048] + " … " + text[-2048:]` (space, U+2026, space) before tokenizing.
3. **Context pre-cut.**
   - Non-human segments (AIUR, AR, DEL, TH): only the **last 512 code points** of `context` are tokenized.
   - Human segments: the whole `context` is tokenized, then only its **last 512 tokens** are kept.
   - An empty context gives no tokens.
4. **Budget.** `budget = 512 − 3 − len(tag)`. The context gets `cb = min(len(ctx), max(64, budget − len(text)))` tokens:
   at least 64 when it has them, more when the text is short.
5. **Text truncation.** `tb = budget − cb`. If the text has more than `tb` tokens, keep its first `h = int(0.7 · tb)`
   tokens and its last `tb − h` tokens (head + tail, nothing inserted).
6. **Context truncation side.** Keep the **last** `cb` context tokens (nearest the item).

## 3. Model inputs and outputs

- `input_ids` int64 [batch, seq], `attention_mask` int64 [batch, seq]: 1 for the real tokens, 0 for padding.
  Pad on the right with `[PAD]` 50283. Padding length does not change the outputs (the reference pads to a multiple of 32).
- The encoder's last hidden states are **mean-pooled over positions with attention_mask = 1**, then one linear layer
  gives `logits` [batch, 5, 54]: heads in the order of `manifest.heads` (`y_hf, haiku, flash, argon, y3`), labels in
  the order of `manifest.labels.order`. Both happen inside the ONNX graph.

## 4. From logits to labels

1. **Probabilities (scorer).** `p = 0.5 · (sigmoid(logits[:, y3]) + sigmoid(logits[:, y_hf]))`, i.e. heads 4 and 0.
   The ONNX output `probs` is exactly this.
2. **Thresholds.** Label `j` is on when `p[j] ≥ thresholds[segment][j]`; a segment without its own row uses
   `thresholds["default"]` (`manifest.thresholds`).
3. **Closure.** Every label that is on adds all of its ancestors (`manifest.labels.ancestors`; parents in
   `manifest.labels.parent`). The closed set is the output.

## 5. Variants

`model.fp32.onnx` and `model.int8.onnx` take the same inputs and give the same outputs. int8 is weight-only
(MatMulNBits, 8 bits, block 32, symmetric, accuracy_level 4); the embedding table stays fp32. Parity tolerances in
`parity-fixture.json`: token ids and attention masks must match exactly; logits within 1e-3 and probabilities within
1e-4 for the same variant; closed labels must match exactly except for a probability within 1e-4 of its threshold.
