#!/usr/bin/env python3
"""Measure real query APIs; keep request fixtures and result digests for parity.

Example: python3 tools/query-bench.py --config /Volumes/euclid/pharos-dev-delhi/library.toml
         --cases .context/performance/cases.json --output .context/performance/baseline.json
Only reads the selected service. Never writes to the catalog or saves response text.
"""
import argparse
import copy
import hashlib
import json
from pathlib import Path
import statistics
import time
import tomllib
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
PROFILE = "qt-sqlite-v1"


def workloads():
    cases = []
    now = int(time.time() * 1000)
    for path in sorted((ROOT / "schemas").glob("*.schema.json")):
        schema = json.loads(path.read_text())
        dataset = schema["name"].removeprefix("pharos_")
        fields = {f["name"]: f for f in schema["fields"]}
        group = next((f for f in ("provider", "source_kind", "state", "executor", "tool_name") if f in fields), "id")
        number = next((f for f in ("total_tokens", "token_count", "typed_words", "duration_ms", "call_count", "messages", "content_bytes") if f in fields), None)
        timestamp = next((f for f in ("activity_at", "started_at", "sent_at", "first_usage_at", "last_message_at", "called_at", "created_at") if f in fields), None)
        rows = dict(version=2, profile=PROFILE, select=[x["field"] for x in schema["defaultSelect"] if fields[x["field"]].get("bindings", {}).get("sqlite")], limit=50, offset=0, where=[], orderBy=schema.get("defaultSort", []))

        def add(label, operation, body):
            cases.append(dict(name=f"{dataset}/{label}", path=f"/api/query/{dataset}/{operation}", body=copy.deepcopy(body)))

        add("rows-default", "rows-v2", rows)
        narrow = {**rows, "select": ["id", group]}
        add("rows-narrow", "rows-v2", narrow)
        add("rows-offset", "rows-v2", {**narrow, "offset": 500})
        metric = dict(version=2, profile=PROFILE, limit=50, offset=0, where=[], orderBy=[], metrics=[dict(id="count", op="count")])
        add("count", "metrics", metric)
        if number:
            summaries = [dict(id=op, op=op, field=number) for op in ("sum", "avg", "min", "max")]
            add("reductions", "metrics", {**metric, "metrics": summaries})
            add("metric-group", "metrics", {**metric, "metrics": [dict(id="sum", op="sum", field=number, groupBy=[group])]})
            add("formula", "metrics", {**metric, "metrics": [dict(id="ratio", expression=f"SUM([{number}])/NULLIF(COUNT(),0)", groupBy=[group])]})
            add("median", "metrics", {**metric, "metrics": [dict(id="median", expression=f"MEDIAN([{number}])", groupBy=[group])]})
            add("shown", "metrics", {**metric, "orderBy": rows["orderBy"], "metrics": [dict(id="sum", op="sum", field=number, scope="shownRows")]})
            add("numeric-filter", "rows-v2", {**narrow, "where": [dict(field=number, op=">", value="1000")]})
            add("numeric-sort", "rows-v2", {**narrow, "orderBy": [dict(field=number, dir="desc")]})
        summaries = [dict(id="records", op="count")]
        if number:
            summaries.append(dict(id="amount", op="sum", field=number))
        grouped = dict(version=3, profile=PROFILE, now=now, rows=True, source=dict(where=[]), grouping=dict(keys=[group], summaries=summaries), result=dict(select=[group] + [s["id"] for s in summaries], where=[], orderBy=[dict(field="records", dir="desc")], limit=50, offset=0))
        add("groupby", "query-v3", grouped)
        add("having", "query-v3", {**grouped, "result": {**grouped["result"], "where": [dict(field="records", op=">", value="10")]}, "metrics": [dict(id="groups", op="count"), dict(id="shown", op="sum", field="records", scope="shownRows")]})
        if timestamp:
            where = [dict(field=timestamp, op=">=", value="2026-10-01T00:00:00Z")]
            add("recent-rows", "rows-v2", {**rows, "where": where})
            add("recent-groupby", "query-v3", {**grouped, "source": dict(where=where)})
        if "repository_name" in fields and group != "repository_name":
            add("multi-groupby", "query-v3", {**grouped, "grouping": dict(keys=[group, "repository_name"], summaries=summaries), "result": {**grouped["result"], "select": [group, "repository_name"] + [s["id"] for s in summaries]}})
    def add(dataset, label, operation, body):
        cases.append(dict(name=f"{dataset}/{label}", path=f"/api/query/{dataset}/{operation}", body=body))

    recent = [dict(field="started_at", op=">=", value="2026-10-01T00:00:00Z")]
    for dataset, timestamp, number, group in [("tool_calls", "started_at", "duration_ms", "provider"), ("writing_messages", "sent_at", "typed_words", "main_category"), ("tools", None, "call_count", "tool_name"), ("usage", "first_usage_at", "total_tokens", "provider")]:
        where = [dict(field=timestamp, op=">=", value="2026-10-01T00:00:00Z")] if timestamp else []
        metric = dict(version=2, profile=PROFILE, limit=50, offset=0, where=where, orderBy=[], metrics=[])
        for kind in ("box", "histogram"):
            distribution = dict(kind=kind, input=f"[{number}]")
            if kind == "histogram":
                distribution["bins"] = 20
            add(dataset, f"{kind}-grouped", "metrics", {**metric, "metrics": [dict(id=kind, distribution=distribution, groupBy=[group])]})
        add(dataset, "batch-20", "metrics", {**metric, "metrics": [dict(id=f"m{i}", expression=f"SUM([{number}])+{i}", groupBy=[group]) for i in range(20)]})
        add(dataset, "distinct", "metrics", {**metric, "metrics": [dict(id="distinct", op="count_distinct", field=group)]})
        if timestamp:
            add(dataset, "calendar-day", "query-v3", dict(version=3, profile=PROFILE, now=now, rows=False, source={}, result=dict(select=["id"], where=where, limit=50, offset=0), metrics=[dict(id="days", op="sum", field=number, groupBy=[timestamp], dateGrouping=dict(field=timestamp, interval="day", timeZone="America/Denver", range="observed"))]))
        grouped = dict(version=3, profile=PROFILE, now=now, rows=True, source=dict(where=where), grouping=dict(keys=[group], summaries=[dict(id="records", op="count"), dict(id="amount", op="sum", field=number), dict(id="example", op="row", field="id")], representative=dict(orderBy=[dict(field=number, dir="desc", nulls="last")])), result=dict(select=[group, "records", "amount", "example"], limit=50, offset=0))
        add(dataset, "representative", "query-v3", grouped)
    row = dict(version=2, profile=PROFILE, select=["id", "started_at", "provider", "duration_ms"], limit=50, offset=0, orderBy=[dict(field="started_at", dir="desc")])
    add("tool_calls", "or-filter", "rows-v2", {**row, "where": recent + [dict(any=[dict(field="provider", op="=", value="claude"), dict(field="provider", op="=", value="codex")])]})
    add("tool_calls", "negative-filter", "rows-v2", {**row, "where": recent + [dict(field="status", op="=", value="error", negated=True)]})
    add("tool_calls", "command-search", "rows-v2", {**row, "where": recent + [dict(field="command", op="contains", value="go test")]})
    add("writing_messages", "text-regex", "rows-v2", dict(version=2, profile=PROFILE, select=["id", "sent_at", "typed_words"], limit=50, offset=0, where=[dict(field="sent_at", op=">=", value="2026-10-01T00:00:00Z"), dict(field="text", op="matches_regex", value="(?i)thank|please")], orderBy=[dict(field="sent_at", dir="desc")]))
    add("writing_messages", "array-membership", "rows-v2", dict(version=2, profile=PROFILE, select=["id", "sent_at", "categories"], limit=50, offset=0, where=[dict(field="categories", op="includes", value="typed")], orderBy=[dict(field="sent_at", dir="desc")]))
    return cases


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    parser.add_argument("--cases", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--repeat", type=int, default=2)
    parser.add_argument("--match", default="")
    args = parser.parse_args()
    config = tomllib.loads(args.config.read_text())
    if args.cases.exists():
        cases = json.loads(args.cases.read_text())
    else:
        cases = workloads()
        args.cases.parent.mkdir(parents=True, exist_ok=True)
        args.cases.write_text(json.dumps(cases, indent=2) + "\n")
    output = dict(config=str(args.config), started=time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), repeats=args.repeat, cases=[])
    for case in cases:
        if args.match not in case["name"]:
            continue
        timings, digests, statuses, errors = [], [], [], []
        for _ in range(args.repeat):
            request = urllib.request.Request(f"http://127.0.0.1:{config['port']}{case['path']}", data=json.dumps(case["body"]).encode(), headers={"Authorization": f"Bearer {config['api_token']}", "Content-Type": "application/json"})
            start = time.perf_counter()
            try:
                with urllib.request.urlopen(request, timeout=40) as response:
                    data, status = response.read(), response.status
            except urllib.error.HTTPError as error:
                data, status = error.read(), error.code
            except OSError as error:
                data, status = json.dumps(dict(error=str(error))).encode(), 0
            timings.append(round((time.perf_counter() - start) * 1000, 2))
            payload = json.loads(data)
            payload.pop("execution", None)
            payload.pop("now", None)
            digests.append(hashlib.sha256(json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()).hexdigest())
            statuses.append(status)
            errors.append(payload.get("error") if status != 200 else None)
            # A rejected request is not a completed query. One observation is
            # enough to report its diagnostic; do not repeatedly wait out a
            # deadline while measuring successful-query latency.
            if status != 200:
                break
        record = dict(name=case["name"], ms=timings, median_ms=statistics.median(timings), statuses=statuses, digests=digests, errors=errors)
        output["cases"].append(record)
        print(f"{case['name']:38} {str(timings):24} {statuses} {next((e for e in errors if e), '')}", flush=True)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(output, indent=2) + "\n")


if __name__ == "__main__":
    main()
