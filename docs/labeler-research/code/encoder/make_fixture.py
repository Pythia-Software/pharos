# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/KESTREL/scripts/make_fixture.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Parity fixture over INVENTED strings only (no corpus text): every segment's framing x Unicode and length edge cases.
For each case: the item, token ids, attention mask (batch of one, right-padded to a multiple of 32), fp32 and int8 logits
[5, 54] and probs, and closed labels. usage: python make_fixture.py EXPORT_DIR  -> EXPORT_DIR/parity-fixture.json"""
import hashlib, json, os, sys
import numpy as np
d = os.path.abspath(sys.argv[1]); sys.path.insert(0, d)
import kframe
from reference import Labeler

SEGS = [("H-typed", "H-typed", "human", "user"), ("H-typed+ctx", "H-typed+ctx", "human", "user"), ("H-template", "H-template", "human", "user"),
        ("H-resent", "H-resent", "human", "user"), ("H-feedback", "H-feedback", "human", "user"), ("AIUR", "AIUR", "agent_in_user_role", "user"),
        ("AR", "AR", "agent_reply", "assistant"), ("AR-agent", "AR", "agent_reply", "agent"), ("DEL-result", "DEL", "delegation", "tool"),
        ("DEL-prompt", "DEL", "delegation", "assistant"), ("TH", "TH", "thinking", "assistant")]  # sender = units.sender_role
CTX = "Previous user request: can you make the importer skip blank rows?\nPrevious assistant message: Done — the importer now skips blank rows and logs a count."
para = ("The widget cache keeps stale entries after a schema change, so the loader returns old column names. "
        "We should invalidate it on migration and add a regression test that renames a column. ")
CASES = {
    "ascii_short": ("", "fix the flaky login test please"),
    "single_char": ("", "?"),
    "with_context": (CTX, "great, now do the same for the exporter"),
    "combining_marks": (CTX, "rename café to café and keep the ñ in mañana; Z̵̡a̷l̶g̴o text"),
    "emoji_zwj": ("", "ship it \U0001F469‍\U0001F4BB and tell the team \U0001F468‍\U0001F469‍\U0001F467‍\U0001F466 \U0001F44D\U0001F3FD ☺️"),
    "flags": ("", "localize for \U0001F1FA\U0001F1F8 \U0001F1EF\U0001F1F5 \U0001F1E7\U0001F1F7 and the \U0001F3F4\U000E0067\U000E0062\U000E0073\U000E0063\U000E0074\U000E007F flag"),
    "cjk": (CTX, "请修复这个测试，然后运行全部检查。テストを直してください。테스트를 고쳐 주세요."),
    "rtl": ("", "من فضلك أصلح الاختبار ثم شغّل الفحوصات ‏ שלום, תקן את הבדיקה"),
    "crlf_tabs": ("Previous assistant message: ok\r\n", "line one\r\nline two\r\n\tindented\twith\ttabs\r\n"),
    "nbsp_zw": ("", "keep the spacing here​ and﻿there \U0001D518\U0001D52B\U0001D526"),
    "markers": ("", "here is the log ⟦likely pasted text, context only, 1840 chars: \"Traceback (most recent call last): … KeyError: 'id'\"⟧ why does it fail? "
                    "⟦excluded: question form, 40 chars⟧ ⟦quoted agent text, context only, 312 chars: \"I changed the parser…\"⟧"),
    "long_text_4096": ("", (para * 40)[:4096]),
    "long_text_4097": ("", (para * 40)[:4097]),
    "very_long_text": (CTX, para * 120),
    "long_context": ((CTX + " ") * 40, "and the other one too"),
    "long_both": ((CTX + " ") * 40, para * 60),
    "account_sender": ("", "use the staging key"),
}


def main():
    a, b = Labeler(d, "fp32"), Labeler(d, "int8")
    out = []
    for lab, seg, coh, snd in SEGS:
        for name, (ctx, txt) in CASES.items():
            it = {"segment": seg, "cohort": coh, "sender": ("account:invented-42" if name == "account_sender" else snd), "context": ctx, "text": txt}
            ids = a.encode(it)
            assert ids == b.encode(it)
            pid, att = kframe.pad([ids])
            ra, rb = a.label([it])[0], b.label([it])[0]
            out.append({"case": f"{lab}/{name}", "item": it, "input_ids": pid[0], "attention_mask": att[0], "n_tokens": len(ids),
                        "fp32": {"logits": np.round(ra["logits"], 5).tolist(), "probs": np.round(ra["probs"], 6).tolist(), "labels": ra["labels"]},
                        "int8": {"logits": np.round(rb["logits"], 5).tolist(), "probs": np.round(rb["probs"], 6).tolist(), "labels": rb["labels"]}})
    pa = np.array([c["fp32"]["probs"] for c in out]); pb = np.array([c["int8"]["probs"] for c in out])
    same = np.mean([c["fp32"]["labels"] == c["int8"]["labels"] for c in out])
    fx = {"framing_contract": kframe.CONTRACT, "model": a.man["name"],
          "tokenizer_sha256": hashlib.sha256(open(os.path.join(d, "tokenizer.json"), "rb").read()).hexdigest(),
          "onnx_sha256": a.man["sha256"], "padding": "batch of one, right-padded with PAD 50283 to a multiple of 32 (padding does not change outputs)",
          "tolerance": {"logits_abs": 1e-3, "probs_abs": 1e-4, "note": "same runtime + same weights; ids and labels must match exactly"},
          "summary": {"cases": len(out), "max_abs_prob_diff_int8_vs_fp32": float(np.abs(pa - pb).max()), "label_sets_equal_int8_vs_fp32": float(same)},
          "cases": out}
    json.dump(fx, open(os.path.join(d, "parity-fixture.json"), "w"), ensure_ascii=False)
    print(json.dumps(fx["summary"]))


if __name__ == "__main__":
    main()
