package archive

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectColorsKeepDefaultsAndEditsWithIdentity(t *testing.T) {
	c, config := testCatalog(t)
	root := t.TempDir()
	writeSourceFile(t, filepath.Join(root, "manifest.json"), `{"theme_color":"#174ee8"}`)
	tx := seedTx(t, c.DB)
	for _, id := range []string{"app", "twin"} {
		if _, err := tx.Exec(`INSERT INTO repositories(id,display_name,local_locations_json,created_at,updated_at) VALUES(?, 'app', ?, '2026-01-01', '2026-01-01')`, id, jsonText([]string{root})); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,indexed_at) VALUES(?, 'canonical','local',?,?,'Workspace','2026-01-01')`, id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := resolveProjects(t.Context(), tx, "1"); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, tx)
	before, err := c.projectColors(t.Context())
	if err != nil || len(before) != 2 {
		t.Fatalf("colors: %+v %v", before, err)
	}
	if before[0].Default == before[1].Default || before[0].ID == before[1].ID {
		t.Fatal("same names and brand must still receive separate identities and colors")
	}
	if before[0].Source != "manifest.json theme color" {
		t.Fatalf("missing brand provenance: %+v", before[0])
	}
	server := testServer(t, config, c)
	body := jsonText(map[string]any{"id": before[0].ID, "color": "#ABCDEF"})
	if response := preferencesCall(t, server, http.MethodPost, "/api/project-colors", body); response.Code != 200 {
		t.Fatalf("save: %d %s", response.Code, response.Body.String())
	}
	for _, bad := range []string{`{"id":"repository:app","color":"red"}`, `{"id":"missing","color":"#123456"}`, `{"id":"repository:app"}`, `{"id":"repository:app","color":null}`} {
		if response := preferencesCall(t, server, http.MethodPost, "/api/project-colors", bad); response.Code != 400 {
			t.Fatalf("invalid edit accepted: %s %d", bad, response.Code)
		}
	}
	// Name, repository branding, and surrounding projects may change. A saved
	// default/override must not be recomputed by reindex or a new service.
	writeSourceFile(t, filepath.Join(root, "manifest.json"), `{"theme_color":"#ee3311"}`)
	tx = seedTx(t, c.DB)
	if _, err := tx.Exec(`UPDATE repositories SET display_name='renamed' WHERE id='app'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,location,indexed_at) VALUES('new','canonical','local','new','New project','/home/person/notes','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if err := resolveProjects(t.Context(), tx, "1"); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, tx)
	reopened := reopenCatalog(t, c.Path)
	after, err := reopened.projectColors(t.Context())
	if err != nil || len(after) != 3 {
		t.Fatalf("reopened colors: %+v %v", after, err)
	}
	for _, original := range before {
		for _, current := range after {
			if current.ID == original.ID {
				if current.Default != original.Default || current.Source != original.Source {
					t.Fatalf("default changed: %+v -> %+v", original, current)
				}
				if current.ID == before[0].ID && current.Custom != "#abcdef" {
					t.Fatalf("lost user choice: %+v", current)
				}
			}
		}
	}
	response := preferencesCall(t, server, http.MethodGet, "/assets/preferences.js", "")
	if !strings.Contains(response.Body.String(), "window.pharosProjectColors=") || !strings.Contains(response.Body.String(), `"custom_color":"#abcdef"`) {
		t.Fatalf("palette missing from first-paint script: %s", response.Body.String())
	}
	if err := reopened.setProjectColor(t.Context(), before[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	reset, err := reopened.projectColors(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range reset {
		if current.ID == before[0].ID && (current.Custom != "" || current.color() != before[0].Default) {
			t.Fatalf("reset must restore original default: %+v", current)
		}
	}
	// A previous build can add already-resolved project columns, so there
	// need not be a project-column repair to trigger color assignment.
	execTx(t, reopened.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,location,project_folder,indexed_at) VALUES('legacy','canonical','local','legacy','Legacy project','/home/person/legacy','~/legacy','2026-01-01')`)
	if err := reopened.repairProjects(t.Context()); err != nil {
		t.Fatal(err)
	}
	repaired, err := reopened.projectColors(t.Context())
	if err != nil || len(repaired) != 4 {
		t.Fatalf("missing legacy assignment: %+v %v", repaired, err)
	}
}

func TestProjectColorDefaultsContrastAndSeparation(t *testing.T) {
	luminance := func(color string) float64 {
		var r, g, b uint8
		fmt.Sscanf(color, "#%02x%02x%02x", &r, &g, &b)
		linear := func(v uint8) float64 {
			x := float64(v) / 255
			if x <= .04045 {
				return x / 12.92
			}
			return math.Pow((x+.055)/1.055, 2.4)
		}
		return .2126*linear(r) + .7152*linear(g) + .0722*linear(b)
	}
	// All hues must remain usable against the page and panel in either theme.
	for hue := 0; hue < 360; hue += 3 {
		for _, chroma := range []float64{.12, .22} {
			color := projectHueColor(float64(hue), chroma)
			for _, background := range []string{"#eee7d8", "#fffaf0", "#10241b", "#183529"} {
				a, b := luminance(color), luminance(background)
				contrast := (math.Max(a, b) + .05) / (math.Min(a, b) + .05)
				if contrast < 3 {
					t.Fatalf("%s against %s: contrast %.3f", color, background, contrast)
				}
			}
		}
	}
	used := []string{"#85909b"}
	for i := range 36 {
		id := fmt.Sprintf("repository:%d", i)
		color := allocateProjectColor(id, "", used)
		if color != allocateProjectColor(id, "", used) {
			t.Fatal("allocation is not deterministic")
		}
		for _, other := range used {
			if color == other {
				t.Fatalf("reused %s", color)
			}
			if i < 10 && projectLabDistance(projectColorLab(color), projectColorLab(other)) < .055 {
				t.Fatalf("early palette colors too close: %s %s", color, other)
			}
		}
		used = append(used, color)
	}
	brand := "#2255ee"
	first := allocateProjectColor("a", brand, nil)
	second := allocateProjectColor("b", brand, []string{first})
	for _, color := range []string{first, second} {
		d := math.Abs(projectHue(color) - projectHue(brand))
		d = math.Min(d, 360-d)
		if d > 26 {
			t.Fatalf("brand hue lost: %s -> %s", brand, color)
		}
	}
	if first == second {
		t.Fatal("identical brand hints reused an exact color")
	}
}

func TestProjectBrandHintsAreLocalBoundedAndOptional(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	writeSourceFile(t, filepath.Join(dir, "favicon.svg"), `<svg><path fill="#fff"/><path fill="#e46"/></svg>`)
	if hint, source := projectBrandInRoot(root); hint != "#ee4466" || source != "favicon.svg" {
		t.Fatalf("SVG brand: %s %s", hint, source)
	}
	writeSourceFile(t, filepath.Join(dir, "manifest.json"), `{"theme_color":"#2244aa"}`)
	if hint, source := projectBrandInRoot(root); hint != "#2244aa" || source != "manifest.json theme color" {
		t.Fatalf("manifest precedence: %s %s", hint, source)
	}
	if hint, source := projectBrandHint(map[string]any{"kind": "repository", "locations": `["/unavailable/project"]`}); hint != "" || source != "Automatic palette" {
		t.Fatalf("missing checkout: %s %s", hint, source)
	}
	outside := filepath.Join(t.TempDir(), "outside.svg")
	writeSourceFile(t, outside, `<svg fill="#ee4466"/>`)
	if err := os.Symlink(outside, filepath.Join(dir, "escape.svg")); err != nil {
		t.Fatal(err)
	}
	if data := projectBrandRead(root, "escape.svg"); len(data) != 0 {
		t.Fatal("followed symlink outside checkout")
	}
	if data := projectBrandRead(root, ".."); len(data) != 0 {
		t.Fatal("read a directory")
	}
	writeSourceFile(t, filepath.Join(dir, "huge.svg"), strings.Repeat("x", (512<<10)+1))
	if data := projectBrandRead(root, "huge.svg"); len(data) != 0 {
		t.Fatal("read an oversized file")
	}
	icon := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			icon.Set(x, y, color.NRGBA{R: 230, G: 50, B: 110, A: 255})
		}
	}
	icon.Set(0, 0, color.White)
	icon.Set(1, 0, color.Transparent)
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, icon); err != nil {
		t.Fatal(err)
	}
	if got := projectIconColor(pngBytes.Bytes()); projectBrandColor(got) == "" {
		t.Fatalf("PNG brand: %q", got)
	}
	ico := make([]byte, 22)
	ico[2] = 1
	ico[4] = 1
	binary.LittleEndian.PutUint32(ico[14:18], uint32(pngBytes.Len()))
	binary.LittleEndian.PutUint32(ico[18:22], 22)
	ico = append(ico, pngBytes.Bytes()...)
	if got := projectIconColor(ico); got != projectIconColor(pngBytes.Bytes()) {
		t.Fatalf("ICO PNG frame: %s", got)
	}
	for _, data := range [][]byte{nil, []byte("bad image"), []byte("<svg fill='red'/>"), {0, 0, 1, 0, 255, 255}} {
		if projectIconColor(data) != "" {
			t.Fatal("invalid image produced a brand")
		}
	}
}
