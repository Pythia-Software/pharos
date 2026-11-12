import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import fit_usage_estimation as fitter


class UsageEstimationFitterTests(unittest.TestCase):
    def dataset(self):
        rows = []
        for split, families, multiplier in [("train", 6, 1), ("calibration", 4, 2), ("test", 4, 1)]:
            for family in range(families):
                for tools in range(1, 5):
                    rows.append({"family": f"{split}-{family}", "split": split,
                                 "target": multiplier * (100 + 200 * tools),
                                 "baseline": 400,
                                 "features": {"tools": tools, "visible_kchars": .1}})
        return {"version": 1, "rows": rows}

    def test_family_and_scalar_boundaries(self):
        document = self.dataset()
        self.assertEqual(len(fitter.validate_rows(document, ["tools"])), 56)
        for change in [
            lambda d: d["rows"][-1].update(family="train-0"),
            lambda d: d["rows"][0].update(raw_text="private transcript"),
            lambda d: d["rows"][0]["features"].pop("visible_kchars"),
            lambda d: d["rows"][0]["features"].pop("tools"),
            lambda d: d["rows"][0]["features"].update(tools=float("nan")),
            lambda d: d["rows"][0].update(target=True),
        ]:
            with self.subTest(change=change):
                invalid = copy.deepcopy(document)
                change(invalid)
                with self.assertRaises(ValueError):
                    fitter.validate_rows(invalid, ["tools"])

    @unittest.skipUnless(importlib.util.find_spec("numpy") and importlib.util.find_spec("scipy"), "fitter needs NumPy and SciPy")
    def test_visible_model_uses_explicit_block_count_without_free_intercept(self):
        rows=[]
        for i in range(40):
            characters=(i%7+1)*.1;blocks=i%2+1
            rows.append({"family":f"family-{i//4}","split":"train" if i<24 else "calibration" if i<32 else "test","target":characters*700+blocks*8,"features":{"thinking_kchars":characters,"thinking_blocks":blocks}})
        document={"version":1,"rows":rows}
        fitter.validate_rows(document,["thinking_kchars","thinking_blocks"],True)
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);(root/"rows.json").write_text(json.dumps(document))
            subprocess.run([sys.executable,str(Path(fitter.__file__)),"--input",str(root/"rows.json"),"--output",str(root/"model.json"),"--report",str(root/"report.json"),"--id","visible-test","--features","thinking_kchars,thinking_blocks","--unit","tokens","--evidence","visible_unclocked","--basis","coding-study","--alpha","0.01"],check=True,capture_output=True,text=True)
            model=json.loads((root/"model.json").read_text());report=json.loads((root/"report.json").read_text())
            self.assertNotIn("intercept",model)
            self.assertGreater(model["tokens_per_character"],.6)
            self.assertLess(model["tokens_per_character"],.8)
            self.assertEqual(model["maximum_blocks"],2)
            self.assertLess(report["validation"]["test"]["wape"],.01)

    @unittest.skipUnless(importlib.util.find_spec("numpy") and importlib.util.find_spec("scipy"), "fitter needs NumPy and SciPy")
    def test_export_excludes_test_labels_and_reports_refitted_model(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            document = self.dataset()

            def run():
                (root / "rows.json").write_text(json.dumps(document))
                subprocess.run([sys.executable, str(Path(fitter.__file__)),
                                "--input", str(root / "rows.json"),
                                "--output", str(root / "candidate.json"),
                                "--report", str(root / "report.json"),
                                "--id", "synthetic-test", "--features", "tools",
                                "--unit", "tokens", "--evidence", "body",
                                "--basis", "coding-study", "--model", "claude-test",
                                "--effort", "high", "--speed", "standard",
                                "--refit-calibration"], check=True, capture_output=True, text=True)
                return json.loads((root / "candidate.json").read_text()), json.loads((root / "report.json").read_text())

            candidate, report = run()
            self.assertEqual(candidate["donor_responses"], 40)
            self.assertEqual(candidate["donor_families"], 10)
            self.assertFalse(report["test_used_for_fit"])
            self.assertEqual(report["validation"]["test"]["families"], 4)
            self.assertGreater(report["validation"]["test"]["signed_bias"], .2)
            self.assertGreater(candidate["maximum_visible_characters"], 100)
            for row in document["rows"]:
                if row["split"] == "test":
                    row["target"] *= 10
            changed, new_report = run()
            self.assertEqual(candidate, changed)
            self.assertLess(new_report["validation"]["test"]["signed_bias"], -.8)


if __name__ == "__main__":
    unittest.main()
