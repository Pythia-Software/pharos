package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func readExportRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		rows = append(rows, row)
	}
	return rows
}

func strs(value any) []string {
	out := []string{}
	for _, v := range value.([]any) {
		out = append(out, v.(string))
	}
	return out
}

// TestLabelingExport exports a fake-CLI job over a dataset prepared from
// the test catalog, after giving some items every other outcome.
func TestLabelingExport(t *testing.T) {
	quickLabelingTimers(t)
	catalog, _ := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	dir := filepath.Join(t.TempDir(), "dataset")
	if _, err := prepareLabelingDataset(context.Background(), labelingPrepareOptions{Dir: dir, CatalogPath: catalog.Path, Live: true, FreshDays: 14,
		Progress: io.Discard, Now: func() time.Time { return labelingFixtureNow }}, false); err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, dir)
	// One family becomes test, so there is a held-out split besides fresh.
	family := labelingRows(t, db, `SELECT u.family_id FROM units u JOIN items i ON i.unit_id=u.id WHERE u.split IN ('train','dev')
		AND u.duplicate_of IS NULL GROUP BY u.family_id ORDER BY COUNT(*) DESC LIMIT 1`)[0]["family_id"]
	for _, statement := range []string{`UPDATE units SET split='test' WHERE family_id=?`, `UPDATE families SET split='test' WHERE family_id=?`} {
		if _, err := db.Exec(statement, family); err != nil {
			t.Fatal(err)
		}
	}
	cli, _, _ := fakeLabelingCLI(t, "ok")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.RetryInvalid, opts.Spec.IncludeDuplicates = 0, true
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}

	// Items by message, and every item of the job.
	itemOf := map[string]string{}
	for _, row := range labelingRows(t, db, `SELECT i.id,u.message_id,i.chunk_count FROM items i JOIN units u ON u.id=i.unit_id ORDER BY i.id`) {
		if integer(row["chunk_count"]) == 1 {
			itemOf[firstString(row["message_id"])] = firstString(row["id"])
		}
	}
	sources, err := loadLabelingItemSources(context.Background(), db, []string{itemOf["m-a1"]})
	if err != nil {
		t.Fatal(err)
	}
	text := sources[itemOf["m-a1"]].text
	quote := text[:len("I will look")]
	empty := func(item, outcome string, issues ...string) labelingItemResult {
		return labelingItemResult{ItemID: item, Outcome: outcome, Labels: []string{}, Uncertain: []string{}, Evidence: []labelingEvidence{}, Issues: issues}
	}
	yes, no := true, false
	broad := labelingItemResult{ItemID: itemOf["m-a1"], Outcome: itemEvidenceIssues, Labels: []string{"P.request.work", "C.report.finding"},
		Uncertain: []string{"P.request.work.investigate"}, NeedContext: &no, Issues: []string{"label_without_valid_evidence"},
		Evidence: []labelingEvidence{{Label: "P.request.work", Quote: quote, Status: "valid", Occurrences: []labelingOccurrence{{ItemStart: 0, ItemEnd: len(quote),
			Source: []map[string]any{{"start": 0, "end": len(quote)}}}}}, {Label: "C.report.finding", Quote: "nowhere", Status: "not_found"},
			// A rejected occurrence: where the quote is, why it is not evidence.
			{Label: "C.report.finding", Quote: quote, Status: "outside_target", Occurrences: []labelingOccurrence{{ItemStart: 0, ItemEnd: len(quote), Status: "outside_target"}}}},
		LabelsEvidence: map[string]string{"P.request.work": "supported", "C.report.finding": "not_found"}}
	needs := empty(itemOf["m-o3"], itemUsable)
	needs.NeedContext, needs.Labels, needs.LabelsEvidence = &yes, []string{fakeLabel}, map[string]string{fakeLabel: "supported"}
	// Ambiguous evidence in target and marker alike is not supported.
	mixed := empty(itemOf["m-sub1"], itemEvidenceIssues)
	mixed.Labels, mixed.LabelsEvidence, mixed.NeedContext = []string{fakeLabel}, map[string]string{fakeLabel: "ambiguous_mixed"}, &no
	held := empty(itemOf["m-c2"], itemHeld, servedModelDiffers)
	held.Labels, held.HeldOutcome = []string{fakeLabel}, itemUsable
	crafted := []struct {
		message string
		results []labelingItemResult
		served  string
	}{
		{"m-h1", []labelingItemResult{empty(itemOf["m-h1"], itemRefused, "refused: no", "refused_copacked_unresolved")}, servedModelVerified},
		{"m-h2", []labelingItemResult{empty(itemOf["m-h2"], itemInvalid, "tool_use")}, servedModelVerified},
		{"m-h3", []labelingItemResult{empty(itemOf["m-h3"], itemInvalid, "unknown_label"), empty(itemOf["m-h3"], itemMissing, "request_id_set_mismatch")}, servedModelVerified},
		{"m-a1", []labelingItemResult{broad}, servedModelUnverified},
		{"m-o3", []labelingItemResult{needs}, servedModelVerified},
		{"m-c2", []labelingItemResult{held}, servedModelDiffers},
		{"m-o5", nil, ""},
		{"m-o1", []labelingItemResult{empty(itemOf["m-o1"], itemRefusalSuspected, "refusal_suspected")}, servedModelVerified},
		{"m-o6", []labelingItemResult{empty(itemOf["m-o6"], itemInvalid, "unknown_label")}, servedModelVerified},
		{"m-f3-2", []labelingItemResult{empty(itemOf["m-f3-2"], itemRefusedElsewhere, "blocked_by_refusal_key")}, servedModelVerified},
		{"m-f4-2", []labelingItemResult{empty(itemOf["m-f4-2"], "blocked_oversize")}, servedModelVerified},
		{"m-sub1", []labelingItemResult{mixed}, servedModelVerified},
	}
	tx := seedTx(t, db)
	for _, c := range crafted {
		if _, err := tx.Exec(`DELETE FROM item_results WHERE item_id=?`, itemOf[c.message]); err != nil {
			t.Fatal(err)
		}
		for index, result := range c.results {
			if err := insertLabelingItemResult(tx, "pilot:crafted/"+c.message+"/"+string(rune('a'+index)), "pilot", "", result, "hash", "", c.served); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A later refusal does not replace an earlier usable result.
	if err := insertLabelingItemResult(tx, "pilot:crafted/later", "pilot", "", empty(itemOf["m-o4"], itemRefused, "refused: later"), "hash", "", servedModelVerified); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, tx)
	datasetBefore, _ := os.ReadFile(filepath.Join(dir, labelingDatasetFile))

	var printed bytes.Buffer
	if err := runLabelingCLI("", []string{"export", "--dir", dir, "--job", "pilot"}, &printed, io.Discard); err != nil {
		t.Fatal(err)
	}
	datasetAfter, _ := os.ReadFile(filepath.Join(dir, labelingDatasetFile))
	if !bytes.Equal(datasetBefore, datasetAfter) {
		t.Fatal("export changed the dataset")
	}
	exports, _ := os.ReadDir(filepath.Join(dir, labelingExportsDir))
	if len(exports) != 1 || !strings.Contains(printed.String(), "Exported") {
		t.Fatalf("exports %v, printed %s", exports, printed.String())
	}
	out := filepath.Join(dir, labelingExportsDir, exports[0].Name())
	manifestData, err := os.ReadFile(filepath.Join(out, exportManifest))
	if err != nil {
		t.Fatal(err)
	}
	var manifest, heldOutManifest map[string]any
	json.Unmarshal(manifestData, &manifest)
	heldOutData, _ := os.ReadFile(filepath.Join(out, exportHeldOutManifest))
	json.Unmarshal(heldOutData, &heldOutManifest)
	// Aggregates of a split are in the manifest of its side; the training
	// manifest names no held-out split in its aggregates.
	sideOf := func(split string) map[string]any {
		if labelingTrainingSplit(split) {
			return manifest
		}
		return heldOutManifest
	}
	for _, key := range []string{"label_support", "counts", "strata"} {
		if data, _ := json.Marshal(manifest[key]); strings.Contains(string(data), `"test"`) || strings.Contains(string(data), `"fresh"`) {
			t.Errorf("export manifest %s mentions a held-out split: %s", key, data)
		}
	}
	if !strings.Contains(string(heldOutData), `"fresh"`) || manifest["files"].(map[string]any)[exportHeldOutManifest] == nil {
		t.Errorf("held-out manifest %s", heldOutData)
	}
	if manifest["export_id"] != exports[0].Name() {
		t.Fatalf("export id %v in directory %s", manifest["export_id"], exports[0].Name())
	}
	// Every file matches its manifest hash and is private.
	for name, entry := range manifest["files"].(map[string]any) {
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		rows, hasRows := entry.(map[string]any)["rows"]
		if hex.EncodeToString(sum[:]) != entry.(map[string]any)["sha256"] || hasRows && float64(bytes.Count(data, []byte("\n"))) != rows {
			t.Fatalf("%s does not match the manifest", name)
		}
		if info, _ := os.Stat(filepath.Join(out, name)); info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", name, info.Mode())
		}
	}

	inputs := readExportRows(t, filepath.Join(out, exportInputs))
	jobItems := labelingRows(t, db, `SELECT DISTINCT item_id FROM request_items ORDER BY item_id`)
	if len(inputs) != len(jobItems) || manifest["items"] != float64(len(jobItems)) {
		t.Fatalf("%d inputs for %d job items", len(inputs), len(jobItems))
	}
	// Inputs hold only what a student sees: no label, teacher or evidence.
	wantKeys := []string{"actor_kind", "channel", "chars", "chunk_count", "chunk_index", "cohort", "context", "context_excerpt", "dataset_id", "depth",
		"item_id", "kind", "provider", "reasoning_format", "sender", "split", "text", "unit_id", "units_version"}
	for _, row := range inputs {
		keys := make([]string, 0, len(row))
		for key := range row {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if !slices.Equal(keys, wantKeys) {
			t.Fatalf("input keys %v", keys)
		}
	}
	inputData, _ := os.ReadFile(filepath.Join(out, exportInputs))
	for _, leak := range []string{fakeLabel, "C.report", "P.request", "claude-haiku", "evidence", "refused"} {
		if strings.Contains(string(inputData), leak) {
			t.Fatalf("inputs.jsonl mentions %q", leak)
		}
	}

	// Held-out labels are apart; every item has exactly one label row.
	labels := map[string]map[string]any{}
	for file, training := range map[string]bool{exportLabels: true, exportLabelsHeldOut: false} {
		for _, row := range readExportRows(t, filepath.Join(out, file)) {
			if labelingTrainingSplit(row["split"].(string)) != training {
				t.Fatalf("%s holds split %v", file, row["split"])
			}
			if labels[row["item_id"].(string)] != nil {
				t.Fatalf("%v labeled twice", row["item_id"])
			}
			labels[row["item_id"].(string)] = row
		}
	}
	splits := map[string]bool{}
	for _, row := range inputs {
		if labels[row["item_id"].(string)] == nil {
			t.Fatalf("%v has no label row", row["item_id"])
		}
		splits[row["split"].(string)] = true
	}
	if !splits["test"] || !splits["fresh"] || !splits["train"] {
		t.Fatalf("splits %v", splits)
	}

	// Each outcome keeps its status; only usable rows have labels.
	for message, want := range map[string]string{"m-h1": "refused", "m-h2": exportToolUse, "m-h3": itemMissing, "m-a1": exportUsable, "m-o3": exportNeedContext,
		"m-c2": itemHeld, "m-o5": exportUnlabeled, "m-o1": exportUnlabeled, "m-o4": exportUsable, "m-sub2": exportUsable,
		"m-o6": itemInvalid, "m-f3-2": "refused_elsewhere", "m-f4-2": "blocked_oversize", "m-sub1": exportUsable} {
		row := labels[itemOf[message]]
		if row["status"] != want {
			t.Errorf("%s: status %v, want %s", message, row["status"], want)
		}
		if labelingExportLabeled(want) != (row["labels"] != nil) || labelingExportLabeled(want) != (row["implied_ancestors"] != nil) {
			t.Errorf("%s (%s): labels %v", message, want, row["labels"])
		}
	}
	if row := labels[itemOf["m-h1"]]; !slices.Contains(strs(row["flags"]), "refused_copacked_unresolved") || row["returned"] == nil {
		t.Errorf("refused row = %v", row)
	}
	if row := labels[itemOf["m-c2"]]; !slices.Equal(strs(row["returned"].(map[string]any)["labels"]), []string{fakeLabel}) ||
		!slices.Contains(strs(row["flags"]), servedModelDiffers) {
		t.Errorf("held row = %v", row)
	}
	if row := labels[itemOf["m-sub1"]]; len(strs(row["labels"])) != 0 || row["unsupported_labels"].(map[string]any)[fakeLabel] != "ambiguous_mixed" {
		t.Errorf("ambiguous_mixed row = %v", row)
	}
	// Rows without labels say why; a suspected refusal is held, not labeled.
	for message, want := range map[string]string{"m-f3-2": "blocked_by_refusal_key", "m-o1": "refusal suspected: held for review, not yet labeled",
		"m-o5": "no result yet; request completed"} {
		if got := labels[itemOf[message]]["reason"]; got != want {
			t.Errorf("%s reason = %v, want %s", message, got, want)
		}
	}
	// Open items make the job incomplete; the reasons are named, their
	// counts (which span held-out items) are in the held-out manifest.
	job := manifest["job"].(map[string]any)
	if job["complete"] != false || !slices.Equal(strs(job["incomplete_reasons"]), []string{"items_blocked_oversize", "items_unlabeled"}) || job["request_states"] != nil {
		t.Errorf("job = %v", job)
	}
	if open := heldOutManifest["job_wide"].(map[string]any)["open"].(map[string]any); open["items_unlabeled"] != float64(2) {
		t.Errorf("open = %v", open)
	}
	// Masked: the unsupported label with its ancestors, the uncertain one,
	// and every child of the returned broad parent.
	if masked := strs(labels[itemOf["m-a1"]]["masked_labels"]); !slices.Equal(masked, []string{"C", "C.report", "C.report.finding", "P.request.work.implement",
		"P.request.work.investigate", "P.request.work.review", "P.request.work.revise", "P.request.work.verify"}) {
		t.Errorf("masked = %v", masked)
	}
	// A need-context result keeps its labels, apart from usable ones.
	if row := labels[itemOf["m-o3"]]; !slices.Equal(strs(row["labels"]), []string{fakeLabel}) || row["returned"] != nil || row["need_context"] != true {
		t.Errorf("need-context row = %v", row)
	}
	if support := sideOf(labels[itemOf["m-o3"]]["split"].(string))["label_support"].(map[string]any)[fakeLabel].(map[string]any)[labels[itemOf["m-o3"]]["split"].(string)].(map[string]any); support["need_context_positive"] != float64(1) {
		t.Errorf("need-context support = %v", support)
	}
	if history := labels[itemOf["m-h3"]]["receipt"].(map[string]any)["history"].([]any); len(history) != 2 {
		t.Errorf("history = %v", history)
	}
	later := labels[itemOf["m-o4"]]
	if later["outcome"] != itemUsable || len(later["receipt"].(map[string]any)["history"].([]any)) != 2 || later["receipt"].(map[string]any)["cost_status"] != "priced" {
		t.Errorf("m-o4 = %v", later)
	}
	// A broad parent is a positive as returned; its children are not, and
	// an accepted label without evidence is neither.
	broadRow := labels[itemOf["m-a1"]]
	if !slices.Equal(strs(broadRow["labels"]), []string{"P.request.work"}) || !slices.Equal(strs(broadRow["implied_ancestors"]), []string{"P", "P.request"}) ||
		broadRow["unsupported_labels"].(map[string]any)["C.report.finding"] != "not_found" ||
		!slices.Equal(strs(broadRow["uncertain_labels"]), []string{"P.request.work.investigate"}) ||
		!slices.Contains(strs(broadRow["flags"]), servedModelUnverified) {
		t.Errorf("broad row = %v", broadRow)
	}
	// Evidence is exported as stored, rejected occurrences included; the
	// label they belong to is not a positive.
	if evidence := compactJSON(broadRow["evidence"]); !strings.Contains(evidence, `{"item_end":11,"item_start":0,"source":null,"status":"outside_target"}`) {
		t.Errorf("evidence = %s", evidence)
	}

	// Provenance: duplicates, families and round-tripped ranges.
	provenance := map[string]map[string]any{}
	for _, row := range readExportRows(t, filepath.Join(out, exportProvenance)) {
		provenance[row["item_id"].(string)] = row
	}
	if provenance[itemOf["m-del"]]["duplicate_of"] == "" || provenance[itemOf["m-del"]]["family_id"] == "" {
		t.Errorf("delegation copy provenance = %v", provenance[itemOf["m-del"]])
	}
	transformed := 0
	for id, row := range provenance {
		for _, value := range row["source_ranges"].([]any) {
			source := value.(map[string]any)
			if source["transform"] != nil {
				transformed++
				continue
			}
			var input map[string]any
			for _, candidate := range inputs {
				if candidate["item_id"] == id {
					input = candidate
				}
			}
			piece := input["text"].(string)[int(source["item_start"].(float64)):int(source["item_end"].(float64))]
			sum := sha256.Sum256([]byte(piece))
			if hex.EncodeToString(sum[:]) != source["sha256"] {
				t.Fatalf("%s: range %v does not round-trip", id, source)
			}
		}
	}
	transformCounts := func(path string) float64 {
		total := 0.0
		for _, name := range []string{exportManifest, exportHeldOutManifest} {
			var side map[string]any
			data, _ := os.ReadFile(filepath.Join(path, name))
			json.Unmarshal(data, &side)
			total += side["transforms"].(map[string]any)["unverified"].(float64)
			if side["transforms"].(map[string]any)["verified"].(float64)+side["transforms"].(map[string]any)["unverified"].(float64) > float64(transformed) {
				t.Errorf("%s counts more transforms than exist", name)
			}
		}
		return total
	}
	if transformed == 0 || transformCounts(out) != 0 {
		t.Errorf("transformed ranges %d", transformed)
	}

	// The chunked long message is merged, flagged and held out with its split.
	units := readExportRows(t, filepath.Join(out, exportUnitsHeldOut))
	if len(units) != 1 || units[0]["unvalidated_merge"] != true || units[0]["chunking_version"] != labelingChunkVersion || units[0]["status"] != exportUsable ||
		len(units[0]["chunks"].([]any)) != int(units[0]["chunk_count"].(float64)) || units[0]["split"] != "fresh" {
		t.Errorf("unit rows = %v", units)
	}

	// Counts: every taxonomy label in every split, zeros included.
	for split := range splits {
		support := sideOf(split)["label_support"].(map[string]any)
		if len(support) != len(labelingLabels()) {
			t.Errorf("support for %d labels", len(support))
		}
		for label, perSplit := range support {
			if perSplit.(map[string]any)[split] == nil {
				t.Errorf("%s has no %s support", label, split)
			}
		}
	}
	coverage := manifest["coverage"].(map[string]any)["not_labeled_by_status"].(map[string]any)
	heldOutCoverage := heldOutManifest["coverage"].(map[string]any)["not_labeled_by_status"].(map[string]any)
	if coverage["refused_copacked_unresolved"] == nil && heldOutCoverage["refused_copacked_unresolved"] == nil {
		t.Errorf("coverage = %v / %v", coverage, heldOutCoverage)
	}
	support := manifest["label_support"].(map[string]any)
	if support["P.request.work.investigate"].(map[string]any)["train"].(map[string]any)["positive"] == float64(0) {
		t.Error("the fake label has no train support")
	}
	counts := manifest["counts"].(map[string]any)
	for _, dimension := range []string{"split", "cohort", "provider", "depth", "outcome", "length_bucket"} {
		if counts[dimension] == nil {
			t.Errorf("no counts by %s", dimension)
		}
	}
	if manifest["families_in_several_splits"] != float64(0) {
		t.Errorf("families = %v", manifest["families_in_several_splits"])
	}
	duplicates := 0.0
	for _, side := range []map[string]any{manifest, heldOutManifest} {
		for _, values := range side["strata"].(map[string]any) {
			duplicates += values.(map[string]any)["duplicates"].(float64)
		}
	}
	if duplicates == 0 {
		t.Error("no duplicates counted")
	}

	// The same dataset and job export to the same bytes.
	again, _, err := exportLabeling(context.Background(), dir, "pilot", filepath.Join(t.TempDir(), "again"), false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range append(slices.Clone(labelingExportFiles), exportManifest, exportHeldOutManifest) {
		first, _ := os.ReadFile(filepath.Join(out, name))
		second, _ := os.ReadFile(filepath.Join(again, name))
		if !bytes.Equal(first, second) {
			t.Fatalf("%s differs between exports", name)
		}
	}
	if same, _, err := exportLabeling(context.Background(), dir, "pilot", "", false, false); err != nil || same != out {
		t.Fatalf("re-export to %s: %v", same, err)
	}
	// An identical export in place is accepted; a different one is not.
	if same, _, err := exportLabeling(context.Background(), dir, "pilot", again, false, false); err != nil || same != again {
		t.Fatalf("re-export to %s: %v", same, err)
	}
	if _, _, err := exportLabeling(context.Background(), dir, "pilot", filepath.Dir(out), false, false); err == nil {
		t.Fatal("exported into a directory holding other files")
	}

	// A damaged export is not taken for an intact one; --replace rewrites it.
	if err := os.WriteFile(filepath.Join(again, exportInputs), []byte("truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := exportLabeling(context.Background(), dir, "pilot", again, false, false); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("damaged export accepted: %v", err)
	}
	// Files the old manifest does not list are not deleted without asking.
	if err := os.WriteFile(filepath.Join(again, "smoke-model.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := exportLabeling(context.Background(), dir, "pilot", again, true, false); err == nil || !strings.Contains(err.Error(), "smoke-model.json") {
		t.Fatalf("replace over an unlisted file: %v", err)
	}
	if _, _, err := exportLabeling(context.Background(), dir, "pilot", again, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(again, "smoke-model.json")); !os.IsNotExist(err) {
		t.Fatal("--force-replace kept the unlisted file")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(again), ".export-*")); len(leftovers) != 0 {
		t.Fatalf("replace left %v", leftovers)
	}
	if data, _ := os.ReadFile(filepath.Join(again, exportInputs)); !bytes.Equal(data, inputData) {
		t.Fatal("--replace did not rewrite the damaged export")
	}

	// Chunk merges: a chunk that needed context makes the unit
	// usable_need_context, a chunk without labels makes it partial.
	chunks := labelingRows(t, db, `SELECT i.id FROM items i JOIN units u ON u.id=i.unit_id WHERE u.message_id='m-n2' ORDER BY i.chunk_index`)
	mergedStatus := func(name string) string {
		t.Helper()
		path, _, err := exportLabeling(context.Background(), dir, "pilot", filepath.Join(t.TempDir(), name), false, false)
		if err != nil {
			t.Fatal(err)
		}
		rows := readExportRows(t, filepath.Join(path, exportUnitsHeldOut))
		return rows[0]["status"].(string)
	}
	if _, err := db.Exec(`UPDATE item_results SET need_context=1 WHERE item_id=?`, chunks[0]["id"]); err != nil {
		t.Fatal(err)
	}
	if got := mergedStatus("need"); got != exportNeedContext {
		t.Errorf("merge with a need-context chunk = %s", got)
	}
	if _, err := db.Exec(`UPDATE item_results SET outcome='invalid' WHERE item_id=?`, chunks[1]["id"]); err != nil {
		t.Fatal(err)
	}
	if got := mergedStatus("partial"); got != exportPartial {
		t.Errorf("merge with an invalid chunk = %s", got)
	}

	// A decoded piece that no longer matches its encoded bytes is not
	// published: its item is transform_unverified.
	if _, err := db.Exec(`UPDATE units SET message_text=REPLACE(message_text,'Find the CSS','Gind the CSS') WHERE message_id='m-del'`); err != nil {
		t.Fatal(err)
	}
	path, unverified, err := exportLabeling(context.Background(), dir, "pilot", filepath.Join(t.TempDir(), "transform"), false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range readExportRows(t, filepath.Join(path, exportLabels)) {
		if row["item_id"] == itemOf["m-del"] && (row["status"] != exportTransformUnverified || row["labels"] != nil) {
			t.Errorf("m-del = %v", row)
		}
	}
	for _, row := range readExportRows(t, filepath.Join(path, exportLabelsHeldOut)) {
		if row["item_id"] == itemOf["m-del"] && (row["status"] != exportTransformUnverified || row["labels"] != nil) {
			t.Errorf("m-del = %v", row)
		}
	}
	if unverified["job"] == nil || transformCounts(path) != 1 {
		t.Errorf("transforms = %v", transformCounts(path))
	}

	// A job labeled under another taxonomy is not exported by this build.
	if _, err := db.Exec(`UPDATE jobs SET taxonomy_sha256='0000' WHERE name='pilot'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := exportLabeling(context.Background(), dir, "pilot", filepath.Join(t.TempDir(), "taxonomy"), false, false); err == nil || !strings.Contains(err.Error(), "taxonomy") {
		t.Fatalf("other taxonomy exported: %v", err)
	}
	if _, err := db.Exec(`UPDATE jobs SET taxonomy_sha256=? WHERE name='pilot'`, hashBytes(labelingTaxonomy)); err != nil {
		t.Fatal(err)
	}

	// Evidence that is not the item's bytes stops the export.
	if _, err := db.Exec(`UPDATE item_results SET evidence=REPLACE(evidence,'"item_start":0','"item_start":1') WHERE item_id=?`, itemOf["m-a1"]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := exportLabeling(context.Background(), dir, "pilot", filepath.Join(t.TempDir(), "bad"), false, false); err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("corrupt evidence exported: %v", err)
	}
	if _, _, err := exportLabeling(context.Background(), dir, "nope", filepath.Join(t.TempDir(), "none"), false, false); err == nil {
		t.Fatal("an unknown job exported")
	}
}
