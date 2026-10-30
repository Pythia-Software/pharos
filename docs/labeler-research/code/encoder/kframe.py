# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/KESTREL/scripts/kframe.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""KESTREL input framing, contract `kestrel-framing-v1`: a standalone re-statement of the training framing
(BOBOLINK framing.Framer with ctx=r1, fmt=tag, frame_segments=human; round-1 common.Encoder for every other segment).
Only `tokenizers` is needed. Verified token-for-token against framing.Framer (scripts/check_frame.py).

encode(tok, segment, cohort, sender, context, text) -> list[int] token ids, [CLS] tag : context [SEP] text [SEP]."""
CONTRACT = "kestrel-framing-v1"
MAX_LEN = 512          # total tokens including [CLS] and two [SEP]
CTX_MIN = 64           # context keeps at least this many tokens (if it has them)
TEXT_CHAR_CAP = 4096   # texts longer than this (code points) are cut to head 2048 + " … " + tail 2048 before tokenizing
CTX_CHAR_CAP = 512     # non-human segments: only the last 512 code points of the context are tokenized
CTX_TOK_CAP = 512      # human segments: the whole context is tokenized, then only its last 512 tokens are kept
HEAD_FRAC = 0.7        # token truncation of the text keeps head int(0.7 * budget) and the tail rest
HUMAN = ("H-typed", "H-typed+ctx", "H-template", "H-resent", "H-feedback")
CLS, SEP, PAD = 50281, 50282, 50283


def sender_tag(cohort, sender):
    s = sender or ""
    if s.startswith("account:"):
        s = "account"
    return f"{cohort} {s}".strip() + " :"


def _ids(tok, s):
    return tok.encode(s, add_special_tokens=False).ids if hasattr(tok, "encode") and not hasattr(tok, "convert_tokens_to_ids") \
        else tok(s, add_special_tokens=False)["input_ids"]


def encode(tok, segment, cohort, sender, context, text):
    context = context or ""
    if len(text) > TEXT_CHAR_CAP:
        text = text[: TEXT_CHAR_CAP // 2] + " … " + text[-(TEXT_CHAR_CAP // 2):]
    human = segment in HUMAN
    if not human:
        context = context[-CTX_CHAR_CAP:] if context else ""
    g = _ids(tok, sender_tag(cohort, sender))
    t = _ids(tok, text)
    c = _ids(tok, context) if context else []
    if human:
        c = c[-CTX_TOK_CAP:]
    budget = MAX_LEN - 3 - len(g)
    cb = min(len(c), max(CTX_MIN, budget - len(t)))
    tb = budget - cb
    if len(t) > tb:
        h = int(tb * HEAD_FRAC)
        t = t[:h] + t[len(t) - (tb - h):] if tb - h > 0 else t[:tb]
    c = c[len(c) - cb:] if cb > 0 else []
    return [CLS] + g + c + [SEP] + t + [SEP]


def pad(seqs, mult=32):
    """Right-pad with PAD to a multiple of 32; attention mask 1 for real tokens."""
    L = max(len(s) for s in seqs); L = ((L + mult - 1) // mult) * mult
    ids = [s + [PAD] * (L - len(s)) for s in seqs]; att = [[1] * len(s) + [0] * (L - len(s)) for s in seqs]
    return ids, att
