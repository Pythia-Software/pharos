# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/BOBOLINK/scripts/framing.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Framing encoder as in bob_train.py (same logic, as a class), for CPU benchmarks and tokenizer-parity work."""
import os, sys
import numpy as np
import pandas as pd
sys.path.insert(0, os.path.dirname(__file__))
from common import sender_tag, Encoder


class Framer:
    def __init__(self, tok, cohort, sender, segment, ctx_all, ctx_user, ctx_asst, text, frame_segments,
                 ctx="r1", ctx_part="both", order="ctx_first", fmt="tag", max_len=512, base_max_len=512):
        self.tok, self.cohort, self.sender, self.segment = tok, cohort, sender, segment
        self.ctx_all, self.ctx_user, self.ctx_asst, self.text = ctx_all, ctx_user, ctx_asst, text
        self.FS = set(frame_segments)
        self.ctx, self.ctx_part, self.order, self.fmt, self.max_len = ctx, ctx_part, order, fmt, max_len
        self.CLS = tok.cls_token_id if tok.cls_token_id is not None else tok.bos_token_id
        self.SEP = tok.sep_token_id if tok.sep_token_id is not None else tok.eos_token_id
        self.r1enc = Encoder(tok, max_len=base_max_len, ctx_max=64)
        self._tc = {}

    def tids(self, s):
        v = self._tc.get(s)
        if v is None:
            v = self.tok(s, add_special_tokens=False)["input_ids"]; self._tc[s] = v
        return v

    def framed(self, idx):
        if self.ctx_part == "both":
            cs = [self.ctx_all[i] for i in idx]
        elif self.ctx_part == "asst":
            cs = [self.ctx_asst[i] for i in idx]
        else:
            cs = [self.ctx_user[i] for i in idx]
        if self.ctx == "none":
            cs = ["" for _ in idx]
        cap = self.max_len * 8
        txt = [self.text[i] for i in idx]
        txt = [t if len(t) <= cap else t[: cap // 2] + " … " + t[-cap // 2:] for t in txt]
        T = self.tok(txt, add_special_tokens=False)["input_ids"]
        C = self.tok(cs, add_special_tokens=False)["input_ids"]
        out = []
        for k, i in enumerate(idx):
            if self.fmt == "tag":
                g1, g2 = self.tids(sender_tag(self.cohort[i], self.sender[i]) + " :"), []
            elif self.fmt == "segtag":
                g1, g2 = self.tids(f"{self.segment[i]} {self.sender[i] or ''} :"), []
            elif self.fmt == "notag":
                g1, g2 = [], []
            else:
                g1, g2 = self.tids("Earlier conversation:"), self.tids(f"Current {self.cohort[i]} message:")
            c, t = C[k], T[k]
            if self.order == "text_first":
                g1, g2 = (g2, g1) if self.fmt == "words" else (g1, [])
            budget = self.max_len - 3 - len(g1) - len(g2)
            if self.ctx == "r1":
                c = c[-(64 * 8):]
                cb = min(len(c), max(64, budget - len(t)))
            else:
                capn = 10 ** 6 if self.ctx in ("all", "none") else int(self.ctx)
                want = min(len(c), capn)
                cb = min(want, max(min(want, 64), budget - len(t)))
            tb = budget - cb
            if len(t) > tb:
                h = int(tb * 0.7)
                t = t[:h] + t[len(t) - (tb - h):] if tb - h > 0 else t[:tb]
            c = c[len(c) - cb:] if cb > 0 else []
            if self.order == "text_first":
                out.append([self.CLS] + g1 + t + [self.SEP] + g2 + c + [self.SEP])
            else:
                out.append([self.CLS] + g1 + c + [self.SEP] + g2 + t + [self.SEP])
        return out

    def encode_rows(self, idx):
        idx = np.asarray(idx)
        out = [None] * len(idx)
        fm = np.isin(self.segment[idx], list(self.FS))
        pos = np.flatnonzero(fm)
        for i in range(0, len(pos), 4096):
            p = pos[i:i + 4096]
            for k, v in zip(p, self.framed(idx[p])):
                out[k] = v
        pos = np.flatnonzero(~fm)
        for i in range(0, len(pos), 4096):
            p = pos[i:i + 4096]
            r = self.r1enc.encode_batch([sender_tag(self.cohort[idx[k]], self.sender[idx[k]]) for k in p],
                                        [self.ctx_all[idx[k]] for k in p], [self.text[idx[k]] for k in p])
            for k, v in zip(p, r):
                out[k] = v
        return out
