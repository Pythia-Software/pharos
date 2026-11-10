//go:build probe

// Renders random settled entities of a library copy with this renderer and
// compares each PNG with the one stored, timing evidence loading and
// rendering. Run it before adding a version to fingerprintSameImages. The
// copy is opened read-only and immutable; nothing is written.
//
//	PHAROS_FP_SNAPSHOT=/path/to/copy/catalog.sqlite3 PHAROS_FP_N=1500 \
//	  go test -tags probe -run '^TestFingerprintStoredParity$' -v ./internal/archive

package archive

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFingerprintStoredParity(t *testing.T) {
	path := os.Getenv("PHAROS_FP_SNAPSHOT")
	if path == "" {
		t.Skip("set PHAROS_FP_SNAPSHOT to a copy of a library catalog")
	}
	if strings.Contains(path, "/Pharos/catalog/") {
		t.Fatal("probe a copy, never the live catalog")
	}
	n, _ := strconv.Atoi(os.Getenv("PHAROS_FP_N"))
	if n == 0 {
		n = 100
	}
	u := &url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&immutable=1&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := &Catalog{DB: db, Path: path, readOnly: true, queryOnly: true}
	r, _ := newFingerprintRenderer()
	ctx := context.Background()
	for _, entity := range []string{"conversation", "workspace"} {
		rows, err := queryMapsContext(ctx, db, `SELECT id FROM fingerprint_entities WHERE entity=? AND dirty=0 ORDER BY random() LIMIT ?`, entity, n)
		if err != nil {
			t.Fatal(err)
		}
		var load, render time.Duration
		same, differ, missing := 0, 0, 0
		for _, row := range rows {
			id := firstString(row["id"])
			stored, err := queryMapsContext(ctx, db, `SELECT profile,theme,events,hash FROM fingerprint_images WHERE entity=? AND entity_id=?`, entity, id)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{}
			for _, s := range stored {
				want[firstString(s["profile"])+"/"+firstString(s["theme"])+"/"+strconv.FormatInt(integer(s["events"]), 10)] = firstString(s["hash"])
			}
			start := time.Now()
			input, err := c.messageLabelFingerprintInput(ctx, id, entity == "workspace", 0, true)
			load += time.Since(start)
			if err != nil {
				t.Fatalf("%s %s: %v", entity, id, err)
			}
			input["events"] = input["agent_events"]
			start = time.Now()
			result, err := r.Render(ctx, input, 1)
			render += time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			for _, image := range result.Images {
				ev := "0"
				if image.Events {
					ev = "1"
				}
				key := image.Profile + "/" + image.Theme + "/" + ev
				switch want[key] {
				case "":
					missing++
				case image.Hash:
					same++
				default:
					differ++
					if differ <= 5 {
						t.Logf("DIFF %s %s %s", entity, id, key)
					}
				}
			}
		}
		t.Logf("%s: %d entities, images same=%d differ=%d missing=%d; load %s (%.0f ms/entity), render %s (%.0f ms/entity)", entity, len(rows), same, differ, missing, load, float64(load.Milliseconds())/float64(len(rows)), render, float64(render.Milliseconds())/float64(len(rows)))
	}
}
