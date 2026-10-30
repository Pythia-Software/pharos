# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/DOTTEREL/scripts/dfeat.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""DOTTEREL canonical featurizer (the Go parity contract lives here; see PARITY.md).

Per item inputs: text, context (the 600-char PREVIOUS_CONTEXT), cohort, provider, kind, depth, chars,
chunk_index, chunk_count, context_excerpt, segment.

Every sparse block is a list of feature strings -> murmur3_32(utf8, seed 0) as int32 -> abs % n_features
(sklearn FeatureHasher contract) -> counts -> keep-map (train df >= mindf) -> log1p(tf) * idf -> L2 normalise.
"""
import re
import numpy as np

TOK = re.compile(r"(?u)\b\w+\b|[?!]|```|⟦")


def norm(s):
    return s.lower()


def toks(s):
    return TOK.findall(norm(s))


def ngrams12(t):
    return t + [t[i] + " " + t[i + 1] for i in range(len(t) - 1)]


def word12(s):
    return ngrams12(toks(s))


UH, AH = "Previous user request:\n", "Previous assistant message:\n"


def split_context(c):
    """(user_part, assistant_part). The 600-char cut keeps the END of the context, whose last section is the
    assistant message when both headers exist; an unheadered context is treated as an assistant-message tail."""
    a = c.rfind(AH)
    if a >= 0:
        u = c.find(UH)
        user = c[u + len(UH):a] if 0 <= u < a else ""
        return user, c[a + len(AH):]
    u = c.find(UH)
    if u >= 0:
        return c[u + len(UH):], ""
    return "", c


NEG = {"not", "no", "don", "doesn", "didn", "isn", "aren", "wasn", "won", "can", "never", "nothing", "none",
       "without", "nor", "shouldn", "couldn", "wouldn", "stop", "avoid", "t"}
IMP = set("""add fix make run use go check try do update remove delete create write implement build let keep show
change move rename look find read review test verify continue proceed please stop revert commit push merge open
explain tell give put set start rewrite refactor clean investigate figure debug ship deploy install send"""
          .split())
WH = {"what", "why", "how", "where", "when", "which", "who", "whose", "whom", "is", "are", "does", "do", "did",
      "can", "could", "would", "should", "will", "was", "were", "has", "have", "any"}


def pos_feats(t, k_first=6, k_last=4):
    """word-order features over the token list t of the text."""
    f = ["f%d=%s" % (i, x) for i, x in enumerate(t[:k_first])]
    f += ["l%d=%s" % (i, x) for i, x in enumerate(reversed(t[-k_last:]))]
    if len(t) >= 2:
        f.append("fb=%s %s" % (t[0], t[1]))
        f.append("lb=%s %s" % (t[-2], t[-1]))
    n = len(t)
    nb = 0 if n == 0 else 1 if n == 1 else 2 if n == 2 else 3 if n <= 5 else 4 if n <= 10 else 5 if n <= 20 else 6 if n <= 50 else 7 if n <= 150 else 8
    f.append("nb=%d" % nb)
    if t:
        f.append("f0nb=%s|%d" % (t[0], nb))
    return f


def inter_feats(t, ctx_t, k_text=3, k_ctx=12, max_text=40):
    """first k_text text tokens x last k_ctx context tokens; only for texts of <= max_text tokens."""
    if len(t) > max_text or not ctx_t:
        return []
    a = t[:k_text]
    b = ctx_t[-k_ctx:]
    return ["%s|%s" % (x, y) for x in a for y in b]


def cue_feats(t, ctx_t, text):
    """dense cue features (floats)."""
    s = set(t)
    n = len(t)
    neg = sum(1 for x in t if x in NEG)
    ctx_tail = ctx_t[-25:]
    cs = set(ctx_tail)
    return [
        min(neg, 3) / 3.0,
        1.0 if t and t[0] in IMP else 0.0,
        1.0 if (t and t[0] == "please") or (len(t) > 1 and t[1] in IMP and t[0] in {"please", "ok", "okay", "yes", "now", "then", "so", "pls"}) else 0.0,
        1.0 if t and t[0] in WH else 0.0,
        1.0 if "?" in s else 0.0,
        1.0 if t and t[-1] == "?" else 0.0,
        1.0 if t and t[0] in WH and "?" in s else 0.0,
        1.0 if ctx_t and ctx_t[-1] == "?" else 0.0,
        1.0 if "?" in cs else 0.0,
        1.0 if ({"should", "shall", "want", "like"} & cs) and "i" in cs or ("me" in cs and "want" in cs) else 0.0,
        1.0 if ({"option", "options", "or"} & cs) else 0.0,
        np.log1p(n) / 7.0,
        1.0 if n <= 3 else 0.0,
    ]


N_CUE = 13


SENT_END = {"?", "!"}


def pos2_feats(text, t, ct, k_first=10, k_ctx=8):
    """extended word order: positions 6..9, first trigram, sentence/line-initial unigrams and bigrams of the text,
    and the last k_ctx context tokens with their position from the end."""
    f = ["f%d=%s" % (i, x) for i, x in enumerate(t[6:k_first], start=6)]
    if len(t) >= 3:
        f.append("ft=%s %s %s" % (t[0], t[1], t[2]))
    # sentence starts: split the lowered text at . ! ? and newlines, tokenize each piece
    for piece in SPLIT.split(norm(text))[:30]:
        pt = TOK.findall(piece)
        pt = [x for x in pt if x not in SENT_END]
        if pt:
            f.append("s0=%s" % pt[0])
            if len(pt) > 1:
                f.append("s01=%s %s" % (pt[0], pt[1]))
    f += ["c%d=%s" % (i, x) for i, x in enumerate(reversed(ct[-k_ctx:]))]
    return f


SPLIT = re.compile(r"[.!?\n]+")


def char_wb(s, lo=2, hi=5):
    """sklearn analyzer='char_wb', ngram_range=(lo, hi), lowercase=True (used for human-cohort rows only)."""
    out = []
    for w in norm(s).split():  # str.split(): runs of str.isspace() code points (= the \\s\\s+ collapse + split)
        w = " " + w + " "
        L = len(w)
        for n in range(lo, hi + 1):
            off = 0
            out.append(w[off:off + n])
            while off + n < L:
                off += 1
                out.append(w[off:off + n])
            if off == 0:
                break
    return out
