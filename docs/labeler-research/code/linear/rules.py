# Frozen research snapshot, copied unchanged below this line from ~/pharos-labeling/studies/DOTTEREL/scripts/rules.py (2026-10-30).
# Not maintained and not run by tests; ~/pharos-labeling paths refer to the archive layout (see ../README.md).
"""Hand-written lexical cues.  Each rule is (name, label it votes for, cohorts
it applies to or None, regex).  rule_matrix(df) -> (n x R) float32 of 0/1;
used alone (rule fires => label, ancestors implied) and as a feature block.
"""
import re
import numpy as np

H = {"human"}
A = {"agent_reply"}
U = {"agent_in_user_role", "delegation"}
P_SIDE = {"human", "agent_in_user_role", "delegation"}

I = re.I | re.M
RULES = [
    # --- Catcher reports (agent replies) ---
    ("intent", "C.report.intent", A | {"delegation"}, r"^\s*(?:i'll|i will|i'm going to|i am going to|let me|now let me|next,? i'll|first,? i'll|i'll now)\b"),
    ("plan", "C.deliberation.plan", A | {"delegation"}, r"\b(?:let me|now let me|i need to|i should|next,? i|now i'll|i'll (?:check|look|run|read|inspect|search|update|start))\b"),
    ("product", "C.report.product", A | {"delegation"}, r"\b(?:i(?:'ve| have)? (?:added|updated|created|removed|changed|implemented|fixed|rewrote|wrote|renamed|moved|deleted|replaced|refactored)|^\s*(?:added|updated|created|removed|changed|implemented|fixed|wrote)\b|here(?:'s| is) the)"),
    ("verif", "C.report.verification", A | {"delegation"}, r"\b(?:tests? (?:pass|passed|fail|failed|passing|failing)|all \d+ tests?|build (?:succeeds|succeeded|passes|failed)|\bpass(?:es|ed)\b|\bfail(?:s|ed|ing)\b|verified|✓|✅|exit code|go test|npm test|pytest)"),
    ("complete", "C.report.completion", A | {"delegation"}, r"\b(?:done\b|complete[d]?\b|finished|all set|is now (?:fixed|working|in place)|that's it|implemented and)"),
    ("finding", "C.report.finding", A | {"delegation"}, r"\b(?:i found|found (?:that|the|it)|the (?:issue|problem|bug|cause|root cause) (?:is|was)|turns out|this means|because\b|is caused by|the reason)"),
    ("hypo", "C.deliberation.hypothesis", A, r"\b(?:maybe|might be|could be|perhaps|possibly|i suspect|likely|probably|my guess|hypothesis)\b"),
    ("evid", "C.deliberation.evidence", A, r"\b(?:so the|which means|this (?:confirms|suggests|shows|indicates)|that (?:confirms|explains|suggests)|consistent with)\b"),
    ("recon", "C.deliberation.reconsider", A, r"\b(?:actually,|wait,|i was wrong|my (?:earlier|previous) (?:\w+ )?(?:was|is) wrong|on second thought|correction|i misread|hmm)\b"),
    ("limit", "C.report.limitation", A | {"delegation"}, r"\b(?:not (?:yet )?(?:verified|tested)|haven't (?:verified|tested|run|checked)|did not (?:verify|test|run)|i assumed|assuming|note that|caveat|limitation|unverified|untested)\b"),
    ("block", "C.difficulty.blocker", A | {"delegation"}, r"\b(?:cannot|can't|unable to|blocked|permission denied|not permitted|no access|is not available|failed to (?:start|connect|run))\b"),
    ("ambig", "C.difficulty.ambiguity", None, r"\b(?:unclear|ambiguous|not sure (?:what|which|whether)|could mean|it's not clear)\b"),
    ("ask_perm", "P.request.input.permission", A, r"\b(?:should i|shall i|want me to|would you like me to|do you want me to|may i|can i proceed|ok to proceed|okay to)\b"),
    ("ask_choice", "P.request.input.choice", None, r"\b(?:which (?:one|option|approach|do you prefer)|option [a-c1-3]\b|\bor\b[^.?]*\?|prefer\b[^.]*\?)"),
    ("question", "P.request.input.information", P_SIDE, r"(?:^\s*(?:why|what|how|where|when|is|are|does|do|did|can|could|which|who)\b[^\n]*\?|\?\s*$)"),
    ("ask_info_a", "P.request.input", A, r"\?\s*$"),
    # --- Pitcher work requests ---
    ("implement", "P.request.work.implement", P_SIDE, r"\b(?:implement|add (?:a|an|the|support)|create (?:a|an|the)|build (?:a|an|the)|make (?:a|it|the)|write (?:a|an|the))\b"),
    ("investigate", "P.request.work.investigate", P_SIDE, r"\b(?:investigate|look into|figure out|find out|dig into|debug|diagnose|explore|understand why|why (?:is|does|did))\b"),
    ("review", "P.request.work.review", P_SIDE, r"\b(?:review|audit|critique|assess|evaluate)\b"),
    ("revise", "P.request.work.revise", P_SIDE, r"\b(?:fix|change|update|address|revise|rename|refactor|clean up|remove)\b"),
    ("verify", "P.request.work.verify", P_SIDE, r"\b(?:test|verify|reproduce|confirm|check (?:that|whether|if)|validate|run the)\b"),
    ("continue", "P.control.continue", H | {"agent_in_user_role"}, r"^\s*(?:continue|go ahead|proceed|keep going|carry on|go on|resume|next)\b"),
    ("redirect", "P.control.redirect", H, r"\b(?:instead|focus on|rather than|let's not|switch to|first,? (?:let's|do))\b"),
    ("pause", "P.control.pause", H, r"\b(?:hold off|wait (?:until|for)|pause|don't (?:do|start) (?:anything|that) yet)\b"),
    ("cancel", "P.control.cancel", H, r"\b(?:stop|cancel|abort|never mind|nevermind|forget it)\b"),
    ("constraint", "P.frame.constraint", P_SIDE, r"\b(?:don't|do not|never|must not|without|only|keep (?:the|it)|make sure|must)\b"),
    ("criteria", "P.frame.criteria", P_SIDE, r"\b(?:should (?:pass|work|return|be able)|must (?:pass|return|work)|acceptance|success criteria|done when|so that)\b"),
    ("scope", "P.frame.scope", P_SIDE, r"\b(?:the goal is|scope|including|exclude|focus on|in particular|for (?:all|each|every))\b"),
    # --- Catcher feedback / input (human) ---
    ("accept", "C.feedback.accept", H, r"\b(?:looks good|lgtm|great|perfect|sounds good|nice|thanks|thank you|awesome|excellent|love it|that works|agreed|good call)\b"),
    ("challenge", "C.feedback.challenge", H, r"\b(?:still (?:not|doesn't|broken|seeing|see)|doesn't work|not working|is wrong|wrong|broken|bug|issue|problem|incorrect|fails|didn't)\b"),
    ("reject", "C.feedback.reject", H, r"\b(?:no,|nope|that's not what|i don't (?:like|want)|reject|not (?:what i|right))\b"),
    ("grant", "C.input.permission.grant", H, r"^\s*(?:yes|yep|yeah|sure|ok|okay|go ahead|do it|approved|please do|go for it)\b"),
    ("option", "C.input.option", H, r"\b(?:option [a-c1-3]|the (?:first|second|third|last) (?:one|option)|let's go with|i'd go with|go with)\b"),
    ("answer", "C.input.answer", H, r"\b(?:yes,? (?:it|that|the)|no,? (?:it|that|the)|it's because|the answer)\b"),
]


def rule_matrix(df):
    texts = df.text.fillna("").tolist()
    cohort = df.cohort.values
    out = np.zeros((len(texts), len(RULES)), np.float32)
    for j, (_, _, cohorts, pat) in enumerate(RULES):
        rx = re.compile(pat, I)
        allow = None if cohorts is None else np.isin(cohort, list(cohorts))
        for i, t in enumerate(texts):
            if allow is not None and not allow[i]:
                continue
            if rx.search(t[:3000]):
                out[i, j] = 1
    return out
