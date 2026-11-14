"""Checks the plugin contracts: schemas, examples, OpenAPI references and doc examples.

Run with: python3 -m unittest discover -s contracts/plugins
"""

import json
from pathlib import Path
import re
import unittest

try:
    import jsonschema
    from referencing import Registry, Resource
except ImportError:  # pragma: no cover - reported as a skip
    jsonschema = None

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
BASE = "https://pharos.pythia.software/contracts/plugins/"

# Example file -> schema it must satisfy ("file" or "file#/$defs/name").
EXAMPLES = {
    "manifest.pythia-usage.json": "plugin-manifest.schema.json",
    "manifest.ssh-importer.json": "plugin-manifest.schema.json",
    "manifest.labeling-usage.json": "plugin-manifest.schema.json",
    "manifest.webhook.json": "plugin-manifest.schema.json",
    "run-request.export.json": "run-request.schema.json",
    "run-request.import.json": "run-request.schema.json",
    "run-result.export.json": "run-result.schema.json",
    "run-result.import.json": "run-result.schema.json",
    "run-result.usage-import.json": "run-result.schema.json",
    "usage-records.jsonl": "usage-record.schema.json",
    "drop-bundle.json": "drop-bundle.schema.json",
    "report.batch.json": "report.schema.json",
    "connector.grant.json": "connector.schema.json#/$defs/grant",
    "connector.credential.json": "connector.schema.json#/$defs/credential",
    "connector.changes.json": "connector.schema.json#/$defs/changes",
    "connector.items.json": "connector.schema.json#/$defs/items_page",
    "connector.query-result.json": "connector.schema.json#/$defs/query_result",
    "connector.messages.json": "connector.schema.json#/$defs/messages_page",
    "connector.error.field-not-granted.json": "common.schema.json#/$defs/error",
    "receiver.token-response.json": "receiver.schema.json#/$defs/token_response",
    "receiver.hello-request.json": "receiver.schema.json#/$defs/hello_request",
    "receiver.hello-response.changes.json": "receiver.schema.json#/$defs/hello_response",
    "receiver.hello-response.everything.json": "receiver.schema.json#/$defs/hello_response",
    "receiver.hello-response.resend.json": "receiver.schema.json#/$defs/hello_response",
    "receiver.commit-request.json": "receiver.schema.json#/$defs/commit_request",
    "receiver.commit-request.takeover.json": "receiver.schema.json#/$defs/commit_request",
    "receiver.hello-request.takeover.json": "receiver.schema.json#/$defs/hello_request",
    "receiver.commit-response.json": "receiver.schema.json#/$defs/commit_response",
}

# Docs mark a JSON example for checking with <!-- contract: NAME --> on the line before its fence.
DOCS = [ROOT / "docs/plugins.md", ROOT / "docs/plugin-receiver.md"]
DOC_CONTRACT = re.compile(r"<!-- contract: (\S+) -->\n```json\n(.*?)\n```", re.S)


def example(name):
    return json.loads((HERE / "examples" / name).read_text())


@unittest.skipIf(jsonschema is None, "pip install jsonschema to check the plugin contracts")
class ContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.schemas = {path.name: json.loads(path.read_text()) for path in HERE.glob("*.schema.json")}
        cls.registry = Registry().with_resources(
            (BASE + name, Resource.from_contents(schema)) for name, schema in cls.schemas.items())

    def validator(self, ref):
        ref = ref if "#" in ref else ref + "#"
        return jsonschema.Draft202012Validator({"$ref": BASE + ref}, registry=self.registry,
                                               format_checker=jsonschema.FormatChecker())

    def errors(self, ref, instance):
        return [f"{'/'.join(map(str, error.absolute_path)) or '(root)'}: {error.message}"
                for error in self.validator(ref).iter_errors(instance)]

    def test_schemas_are_valid_and_identified_by_their_file(self):
        for name, schema in self.schemas.items():
            with self.subTest(schema=name):
                jsonschema.Draft202012Validator.check_schema(schema)
                self.assertEqual(schema["$id"], BASE + name)

    def test_every_example_satisfies_its_schema(self):
        listed = set(EXAMPLES)
        present = {path.name for path in (HERE / "examples").iterdir()}
        self.assertEqual(present, listed, "list every example in EXAMPLES")
        for name, ref in EXAMPLES.items():
            with self.subTest(example=name):
                path = HERE / "examples" / name
                if name.endswith(".jsonl"):
                    for number, line in enumerate(path.read_text().splitlines(), 1):
                        self.assertEqual(self.errors(ref, json.loads(line)), [], f"line {number}")
                else:
                    self.assertEqual(self.errors(ref, json.loads(path.read_text())), [])

    def test_schemas_refuse_what_the_design_forbids(self):
        manifest = example("manifest.pythia-usage.json")
        importer = example("manifest.ssh-importer.json")
        record = json.loads((HERE / "examples/usage-records.jsonl").read_text().splitlines()[0])
        result = example("run-result.import.json")
        batch = example("report.batch.json")
        resend = example("receiver.hello-response.resend.json")
        takeover = example("receiver.commit-request.takeover.json")
        send = manifest["run"]["send"][0]
        cases = {
            "an exporter that also produces": ("plugin-manifest.schema.json",
                {**manifest, "permissions": {**manifest["permissions"], "produce": {"kinds": ["claude"]}}}),
            "an importer that reads": ("plugin-manifest.schema.json",
                {**importer, "permissions": {**importer["permissions"], "read": manifest["permissions"]["read"]}}),
            "a declarative importer": ("plugin-manifest.schema.json", {**importer, "run": manifest["run"]}),
            "a declarative exporter without destinations": ("plugin-manifest.schema.json",
                {**manifest, "permissions": {k: v for k, v in manifest["permissions"].items() if k != "destinations"}}),
            "usage records without a namespace": ("plugin-manifest.schema.json",
                {**importer, "permissions": {"produce": {"kinds": ["usage-records/1"]}}}),
            "an unversioned dataset": ("plugin-manifest.schema.json",
                {**manifest, "permissions": {**manifest["permissions"], "read": {"datasets": {"usage": {"fields": ["day"], "mode": "rows"}}}}}),
            "two triggers in one entry": ("plugin-manifest.schema.json",
                {**manifest, "triggers": [{"manual": {}, "after_index": {}}]}),
            "priced cost without an amount": ("usage-record.schema.json", {**record, "cost": {"status": "priced"}}),
            "usage without automation": ("usage-record.schema.json", {k: v for k, v in record.items() if k != "automation"}),
            "a transcript drop without a machine": ("run-result.schema.json",
                {**result, "drops": [{"dir": "claude", "kind": "claude", "root": "/home/alice/.claude/projects"}]}),
            "a drop that climbs out of output_dir": ("run-result.schema.json",
                {**result, "drops": [{**result["drops"][0], "dir": "claude/../../etc"}]}),
            "a usage drop with a root": ("run-result.schema.json",
                {**result, "drops": [{"dir": "usage", "kind": "usage-records/1", "namespace": "x", "root": "/tmp"}]}),
            "an unknown status": ("run-result.schema.json", {**result, "status": "done"}),
            "a need that is not offered": ("receiver.schema.json#/$defs/hello_response",
                {"need": "more-permissions", "accepts": [], "max_batch_bytes": 65536, "max_version": 0}),
            "a hello answer without the receiver's highest version": ("receiver.schema.json#/$defs/hello_response",
                {k: v for k, v in resend.items() if k != "max_version"}),
            "a resend of an ID without its dataset": ("receiver.schema.json#/$defs/hello_response",
                {**resend, "need": {"resend": {"items": ["w_9tq3LmA2"]}}}),
            "a resend of a period without its series": ("receiver.schema.json#/$defs/hello_response",
                {**resend, "need": {"resend": {"periods": [{"dataset": "usage/1", "period": "2026-10-21T00:00:00-06:00"}]}}}),
            "a takeover commit that does not say whether it completes": ("receiver.schema.json#/$defs/commit_request",
                {**takeover, "takeover": {"id": takeover["takeover"]["id"]}}),
            "a replace-everything commit outside a takeover": ("receiver.schema.json#/$defs/commit_request",
                {"run_id": takeover["run_id"], "batches": 1, "replace_all": True}),
            "a query that both groups and selects": ("export-query.schema.json",
                {**send["query"], "select": ["provider"]}),
            "an incremental aggregate send without a period": ("plugin-manifest.schema.json",
                {**manifest, "run": {**manifest["run"], "send": [{**send, "query": {**send["query"], "group": {
                    k: v for k, v in send["query"]["group"].items() if k != "period"}}}]}}),
            "the stats preset over rows": ("plugin-manifest.schema.json",
                {**manifest, "permissions": {**manifest["permissions"], "redaction": {"preset": "stats"}}}),
            "the text-scrubbed preset without text": ("plugin-manifest.schema.json",
                {**manifest, "permissions": {**manifest["permissions"], "redaction": {"preset": "text-scrubbed"}}}),
            "an interpreter and an executable": ("plugin-manifest.schema.json",
                {**importer, "run": {**importer["run"], "executable": "bin/pull"}}),
            "an entry outside the bundle": ("plugin-manifest.schema.json",
                {**importer, "run": {"interpreter": "python3", "entry": "../elsewhere.py"}}),
            "a retraction without its dataset": ("report.schema.json",
                {**batch, "retract": [{"id": "w_2bH7xPq1", "version": 1}]}),
            "a period without its series": ("report.schema.json",
                {**batch, "periods": [{k: v for k, v in batch["periods"][0].items() if k != "series"}]}),
            "a filter without a value": ("export-query.schema.json",
                {"query": "export-query/1", "select": ["provider"], "where": [{"field": "provider", "op": "="}]}),
        }
        for description, (ref, instance) in cases.items():
            with self.subTest(case=description):
                self.assertNotEqual(self.errors(ref, instance), [], f"{description} should be refused")

    def test_openapi_references_resolve(self):
        for name in ("connector-api.openapi.json", "receiver-api.openapi.json"):
            document = json.loads((HERE / name).read_text())
            self.assertTrue(document["openapi"].startswith("3.1"))
            operations = [operation["operationId"] for path in document["paths"].values()
                          for operation in path.values() if isinstance(operation, dict) and "operationId" in operation]
            self.assertEqual(len(operations), len(set(operations)), name)
            for ref in references(document):
                with self.subTest(spec=name, ref=ref):
                    target, _, pointer = ref.partition("#")
                    root = document if not target else json.loads((HERE / target).read_text())
                    self.assertIsNotNone(resolve(root, pointer), ref)

    def test_json_examples_in_the_docs_satisfy_their_contracts(self):
        checked = 0
        for doc in DOCS:
            for name, body in DOC_CONTRACT.findall(doc.read_text()):
                with self.subTest(doc=doc.name, contract=name):
                    self.assertIn(name, {Path(ref.split("#")[0]).name for ref in EXAMPLES.values()} | set(EXAMPLES.values()))
                    self.assertEqual(self.errors(name, json.loads(body)), [])
                    checked += 1
        self.assertGreater(checked, 0, "no annotated examples found in the docs")


def references(node):
    if isinstance(node, dict):
        if isinstance(node.get("$ref"), str):
            yield node["$ref"]
        for value in node.values():
            yield from references(value)
    elif isinstance(node, list):
        for value in node:
            yield from references(value)


def resolve(root, pointer):
    node = root
    for part in [p for p in pointer.split("/") if p]:
        part = part.replace("~1", "/").replace("~0", "~")
        if not isinstance(node, dict) or part not in node:
            return None
        node = node[part]
    return node


if __name__ == "__main__":
    unittest.main()
