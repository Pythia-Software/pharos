#!/usr/bin/env python3
"""Fit a reviewed response model from scalar evidence, without transcript access.

Requires NumPy and SciPy. Input is {"version":1,"rows":[...]}; every row has
family, split (train/calibration/test), target and numeric features. Optional
baseline is used only for evaluation. See estimation/README.md for the contract.
"""

import argparse
import collections
import json
import math
from pathlib import Path


FEATURES = {
    "visible_kchars", "argument_kbytes", "tools", "tool_command", "tool_read",
    "tool_edit", "tool_search", "tool_web", "tool_agent", "tool_plan",
    "tool_user", "tool_meta", "tool_mcp", "tool_other", "elapsed_minutes",
    "residual_minutes", "first_call_minutes", "last_call_minutes",
    "first_call_fraction", "last_call_fraction",
    "gap_excess_minutes",
}
VISIBLE_FEATURES = {"thinking_kchars", "thinking_blocks"}


def validate_rows(document, names, visible=False):
    if not isinstance(document, dict) or set(document) != {"version", "rows"} or document.get("version") != 1 or not isinstance(document.get("rows"), list) or not document["rows"]:
        raise ValueError("a version-1 scalar dataset with rows is required")
    allowed = VISIBLE_FEATURES if visible else FEATURES
    if not names or len(set(names)) != len(names) or set(names) - allowed:
        raise ValueError("features must be unique, supported response-model inputs")
    families = {}
    rows = document["rows"]
    for row in rows:
        if not isinstance(row, dict) or set(row) - {"family", "split", "target", "features", "baseline"}:
            raise ValueError("unexpected row fields; supply scalar evidence only")
        if not isinstance(row.get("family"), str) or not row["family"]:
            raise ValueError("each row needs its linked root/helper family")
        if row.get("split") not in {"train", "calibration", "test"}:
            raise ValueError("every row needs an explicit data split")
        old = families.setdefault(row["family"], row["split"])
        if old != row["split"]:
            raise ValueError("a family cannot cross training and evaluation splits")
        values = [row.get("target")]
        if "baseline" in row:
            values.append(row["baseline"])
        features = row.get("features", {})
        required = VISIBLE_FEATURES if visible else {"tools", "visible_kchars"}
        if not isinstance(features, dict) or set(features) - allowed or any(name not in features for name in set(names) | required):
            raise ValueError("unknown or missing features; unavailable is not zero")
        values.extend(features.values())
        if any(isinstance(v, bool) or not isinstance(v, (int, float))
               or not math.isfinite(v) or v < 0 for v in values):
            raise ValueError("targets and features must be finite nonnegative numbers")
        if visible:
            if not 0 < features["thinking_kchars"] <= 1000 or not 1 <= features["thinking_blocks"] <= 1000 or features["thinking_blocks"] != int(features["thinking_blocks"]):
                raise ValueError("visible thinking needs positive character counts and integral blocks")
        elif not 1 <= features["tools"] <= 1000 or features["tools"] != int(features["tools"]) or features["visible_kchars"] > 10000:
            raise ValueError("response models require 1..1000 tools and at most ten million text characters")
    if not all(any(r["split"] == split for r in rows)
               for split in ("train", "calibration", "test")):
        raise ValueError("train, calibration and test evidence are all required")
    return rows


def fit_nonnegative(rows, names, alpha, prior=None, intercept=True):
    import numpy as np
    from scipy.optimize import nnls

    counts = collections.Counter(r["family"] for r in rows)
    weights = np.array([1 / counts[r["family"]] for r in rows])
    weights *= len(weights) / sum(weights)
    x = np.array([[1. if intercept else 0.] + [r["features"][name] for name in names] for r in rows])
    y = np.array([r["target"] for r in rows])
    scale = np.maximum(np.sqrt(np.average(x * x, axis=0, weights=weights)), .01)
    scale[0] = 1
    penalty = np.sqrt(alpha) * np.eye(len(names) + 1)
    penalty[0, 0] = 0
    target = np.zeros(len(names) + 1) if prior is None else penalty @ (prior * scale)
    beta, _ = nnls(np.vstack([x / scale * np.sqrt(weights[:, None]), penalty]),
                   np.r_[y * np.sqrt(weights), target], maxiter=3000)
    beta = beta / scale
    if prior is not None:
        weight = len(counts) / (len(counts) + 20)
        beta = prior * (1 - weight) + beta * weight
    return beta


def predict(beta, names, rows):
    return [max(0., float(beta[0] + sum(value * r["features"][name]
                                      for name, value in zip(names, beta[1:]))))
            for r in rows]


def metrics(rows, predictions):
    total = max(sum(r["target"] for r in rows), 1)
    result = {"responses": len(rows), "families": len({r["family"] for r in rows}),
              "wape": sum(abs(p - r["target"]) for p, r in zip(predictions, rows)) / total,
              "signed_bias": sum(p - r["target"] for p, r in zip(predictions, rows)) / total}
    if all("baseline" in r for r in rows):
        result["baseline_wape"] = sum(abs(r["baseline"] - r["target"]) for r in rows) / total
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--report", required=True, type=Path)
    parser.add_argument("--id", required=True)
    parser.add_argument("--features", required=True, help="comma-separated feature names")
    parser.add_argument("--unit", required=True, choices=["seconds", "tokens"])
    parser.add_argument("--evidence", required=True, choices=["body", "body_prior", "window", "observed_window", "observed_gaps", "visible_unclocked"])
    parser.add_argument("--basis", required=True, choices=["coding-study", "chat-duration-study"])
    parser.add_argument("--model", default="*")
    parser.add_argument("--effort", default="*")
    parser.add_argument("--speed", default="*")
    parser.add_argument("--alpha", type=float, default=1.)
    parser.add_argument("--prior-model", type=Path)
    parser.add_argument("--calibrate-mean", action="store_true")
    parser.add_argument("--refit-calibration", action="store_true")
    args = parser.parse_args()
    names = args.features.split(",")
    visible = args.evidence == "visible_unclocked"
    body = args.evidence in {"body", "body_prior"} or visible
    if visible and set(names) != VISIBLE_FEATURES:
        parser.error("visible_unclocked requires thinking_kchars and thinking_blocks")
    if not args.id.strip() or not args.model.strip() or args.model != args.model.strip().lower() or not args.effort.strip() or args.effort != args.effort.strip().lower() or args.speed not in {"*", "standard", "fast"}:
        parser.error("use a nonempty ID, normalized model/effort and standard, fast or wildcard speed")
    if not math.isfinite(args.alpha) or args.alpha <= 0:
        parser.error("alpha must be finite and positive")
    if body and any(name in args.features for name in ("minutes", "fraction")):
        parser.error("body models cannot require clocks")
    if args.evidence not in {"observed_window", "observed_gaps"} and "residual_minutes" in args.features:
        parser.error("this contract reserves residuals for fully observed tool clocks")
    if (args.evidence == "observed_gaps") != ("gap_excess_minutes" in names):
        parser.error("observed_gaps models require gap_excess_minutes; other models cannot use it")
    if args.evidence == "body" and "*" in (args.model, args.effort, args.speed):
        parser.error("published coding token models require exact execution settings")
    if body != (args.unit == "tokens") or (args.unit == "tokens") != (args.basis == "coding-study"):
        parser.error("body models predict coding tokens; window models predict native chat thinking seconds")
    rows = validate_rows(json.loads(args.input.read_text()), names, visible)
    if not body and any("elapsed_minutes" not in r["features"] or not .1 <= r["features"]["elapsed_minutes"] * 60 <= 1800 or r["target"] > r["features"]["elapsed_minutes"] * 60 for r in rows):
        parser.error("window rows need native elapsed_minutes within 0.1..1800 seconds and a target within that window")
    parts = {k: [r for r in rows if r["split"] == k] for k in ("train", "calibration", "test")}
    fit_rows = parts["train"] + parts["calibration"] if args.refit_calibration else parts["train"]
    n, families = len(fit_rows), len({r["family"] for r in fit_rows})
    if len(parts["train"]) < 20 or len({r["family"] for r in parts["train"]}) < 5:
        parser.error("training needs at least 20 responses and five families")
    prior = None
    if args.prior_model:
        import numpy as np
        parent = json.loads(args.prior_model.read_text())
        if visible:
            parser.error("visible_unclocked candidates use the standalone scalar fit; review model-group shrinkage separately")
        if parent.get("unit") != args.unit or parent.get("evidence") != args.evidence:
            parser.error("prior model units and evidence must match")
        prior = np.array([parent["intercept"]] + [parent["coefficients"].get(n, 0) for n in names])
        if any(not math.isfinite(v) or v < 0 for v in prior):
            parser.error("prior coefficients must be finite and nonnegative")
    beta = fit_nonnegative(parts["train"], names, args.alpha, prior, not visible)
    gain = 1.
    if args.calibrate_mean:
        predicted = sum(predict(beta, names, parts["calibration"]))
        gain = min(2., max(.5, sum(r["target"] for r in parts["calibration"]) / max(predicted, 1)))
    report = {"feature_contract": "claude-response-v4", "alpha": args.alpha,
              "calibration_gain": gain, "validation": {}, "test_used_for_fit": False,
              "refit_calibration": args.refit_calibration}
    report["validation"]["calibration_before_refit"] = metrics(parts["calibration"], [v * gain for v in predict(beta, names, parts["calibration"])])
    if args.refit_calibration:
        beta = fit_nonnegative(fit_rows, names, args.alpha, prior, not visible)
    beta *= gain
    if not any(v > 1e-9 for v in beta[1:]):
        parser.error("fit has no supported feature signal; retain the published prior")
    report["validation"]["test"] = metrics(parts["test"], predict(beta, names, parts["test"]))
    if not visible:
        result = {"id": args.id, "evidence": args.evidence, "unit": args.unit,
              "model": args.model, "effort": args.effort, "speed": args.speed,
              "intercept": float(beta[0]),
              "coefficients": {name: float(v) for name, v in zip(names, beta[1:]) if v > 1e-9},
              "donor_responses": n, "donor_families": families,
              "minimum_tools": 1, "maximum_tools": min(1000, max(int(r["features"]["tools"]) for r in fit_rows)),
              "maximum_visible_characters": min(10000000, max(1, math.ceil(max(r["features"]["visible_kchars"] * 1000 for r in fit_rows) * 1.25))),
              "minimum_window_seconds": 0 if body else .1,
              "maximum_window_seconds": 0 if body else 1800,
              "lower_multiplier": 0, "upper_multiplier": 4,
              "requires_reported_settings": args.evidence == "body",
              "basis": args.basis,
              "notes": "Nonnegative arithmetic fit from scalar evidence; family-balanced. Review source regime, domain and held-out results before publication. Sensitivity is not a chat confidence interval."}
    if visible:
        coefficients = dict(zip(names, beta[1:]))
        if coefficients["thinking_kchars"] <= 0:
            parser.error("visible thinking needs an identifiable character contribution")
        result = {"model": args.model, "effort": args.effort, "speed": args.speed,
                  "tokens_per_character": float(coefficients["thinking_kchars"] / 1000),
                  "tokens_per_block": float(coefficients["thinking_blocks"]),
                  "donor_requests": n, "donor_families": families,
                  "maximum_characters": min(1000000, math.ceil(max(r["features"]["thinking_kchars"] * 1000 for r in fit_rows) * 1.25)),
                  "maximum_blocks": max(int(r["features"]["thinking_blocks"]) for r in fit_rows),
                  "lower_multiplier": 0, "upper_multiplier": 4,
                  "notes": "Complete explicitly visible unclocked thinking only. Family-balanced arithmetic character/block model; native clocks and incomplete-trace fallbacks remain separate. Sensitivity is not confidence."}
    args.output.write_text(json.dumps(result, indent=2, allow_nan=False) + "\n")
    args.report.write_text(json.dumps(report, indent=2, allow_nan=False) + "\n")


if __name__ == "__main__":
    main()
