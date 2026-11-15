package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
)

// One preference per stable identity keeps automatic assignments and deliberate
// edits with the library, without overwriting other projects' edits.
const projectColorPrefix = "pharos-project-color:"

type projectColorChoice struct {
	Default string `json:"default_color"`
	Source  string `json:"source"`
	Custom  string `json:"custom_color,omitempty"`
}

type projectColorRecord struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	projectColorChoice
	// Settings sorts by these, so the projects people edit most come first.
	Conversations      int64  `json:"conversations"`
	LatestConversation string `json:"latest_conversation_at,omitempty"`
}

func projectColorKey(id string) string {
	return fmt.Sprintf("%s%x", projectColorPrefix, sha256.Sum256([]byte(id)))
}

func (p projectColorChoice) color() string {
	if validProjectColor(p.Custom) {
		return p.Custom
	}
	return p.Default
}

func validProjectColor(color string) bool {
	if len(color) != 7 || color[0] != '#' {
		return false
	}
	for _, c := range color[1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func projectColorChoices(ctx context.Context, q queryer) (map[string]projectColorChoice, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT key,value FROM ui_preferences WHERE key LIKE 'pharos-project-color:%'`)
	choices := map[string]projectColorChoice{}
	for _, row := range rows {
		var choice projectColorChoice
		if json.Unmarshal([]byte(firstString(row["value"])), &choice) == nil && validProjectColor(choice.Default) {
			choices[firstString(row["key"])] = choice
		}
	}
	return choices, err
}

func projectColorRows(ctx context.Context, q queryer, predicate string, args ...any) ([]map[string]any, error) {
	// Select affected project identities first, then count conversations across
	// all of each project's workspaces, not just those in the input predicate.
	return queryMapsContext(ctx, q, `SELECT `+projectIDSQL("w")+` id,`+projectNameSQL("w")+` name,`+projectKindSQL("w")+` kind,
		MAX(pj.local_locations_json) locations, COUNT(cv.id) conversations,
		MAX(COALESCE(cv.ended_at,cv.started_at)) latest FROM workspaces w`+projectJoin("w")+`
		JOIN conversations cv ON cv.workspace_id=w.id
		WHERE `+projectIDSQL("w")+` IN (SELECT `+projectIDSQL("w")+` FROM workspaces w`+projectJoin("w")+` WHERE (`+predicate+`))
		GROUP BY 1 HAVING COUNT(cv.id)>3 ORDER BY 1`, args...)
}

// Called by project resolution, inside ingest/migration's transaction. Only new
// identities read brand hints or allocate a color. Sorting makes a first import
// reproducible; adding or removing projects never rebalances saved colors.
func ensureProjectColors(ctx context.Context, tx *sql.Tx, predicate string, args ...any) error {
	rows, err := projectColorRows(ctx, tx, predicate, args...)
	if err != nil || len(rows) == 0 {
		return err
	}
	var choices map[string]projectColorChoice
	for _, row := range rows {
		key := projectColorKey(firstString(row["id"]))
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM ui_preferences WHERE key=?`, key).Scan(&raw); err != nil && err != sql.ErrNoRows {
			return err
		}
		var choice projectColorChoice
		if json.Unmarshal([]byte(raw), &choice) == nil && validProjectColor(choice.Default) {
			continue
		}
		if choices == nil {
			if choices, err = projectColorChoices(ctx, tx); err != nil {
				return err
			}
		}
		used := []string{"#85909b"} // Other / no project is always neutral.
		for _, existing := range choices {
			used = append(used, existing.Default, existing.color())
		}
		hint, source := projectBrandHint(row)
		choice = projectColorChoice{Default: allocateProjectColor(firstString(row["id"]), hint, used), Source: source}
		if hint != "" && projectHueDistance(projectHue(hint), projectHue(choice.Default)) > 26 {
			choice.Source = "Automatic palette (repository colors already in use)"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ui_preferences(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, jsonText(choice)); err != nil {
			return err
		}
		choices[key] = choice
	}
	return nil
}

func (c *Catalog) projectColors(ctx context.Context) ([]projectColorRecord, error) {
	rows, err := projectColorRows(ctx, c.reader(), "1")
	if err != nil {
		return nil, err
	}
	choices, err := projectColorChoices(ctx, c.reader())
	if err != nil {
		return nil, err
	}
	result := []projectColorRecord{}
	for _, row := range rows {
		id := firstString(row["id"])
		if choice, ok := choices[projectColorKey(id)]; ok {
			conversations, _ := row["conversations"].(int64)
			result = append(result, projectColorRecord{id, strings.TrimPrefix(projectColorKey(id), projectColorPrefix), firstString(row["name"]), firstString(row["kind"]), choice, conversations, firstString(row["latest"])})
		}
	}
	return result, nil
}

// An older build sharing the library may index projects without assigning
// colors. Maintenance fills those gaps without needing a project-column change.
func (c *Catalog) repairProjectColors(ctx context.Context) error {
	rows, err := projectColorRows(ctx, c.reader(), "1")
	if err != nil || len(rows) == 0 {
		return err
	}
	choices, err := projectColorChoices(ctx, c.reader())
	if err != nil {
		return err
	}
	missing := []string{}
	for _, row := range rows {
		id := firstString(row["id"])
		if _, ok := choices[projectColorKey(id)]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return c.writeTransaction(ctx, "repair project colors", func(tx *sql.Tx) error {
		return ensureProjectColors(ctx, tx, projectIDSQL("w")+` IN (SELECT value FROM json_each(?))`, jsonText(missing))
	})
}

func (c *Catalog) setProjectColor(ctx context.Context, id, color string) error {
	if color != "" && !validProjectColor(color) {
		return fmt.Errorf("choose a six-digit hex color")
	}
	return c.writeTransaction(ctx, "project color", func(tx *sql.Tx) error {
		rows, err := projectColorRows(ctx, tx, projectIDSQL("w")+`=?`, id)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("project color unavailable")
		}
		key := projectColorKey(id)
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM ui_preferences WHERE key=?`, key).Scan(&raw); err != nil {
			return fmt.Errorf("project color unavailable: %w", err)
		}
		var choice projectColorChoice
		if json.Unmarshal([]byte(raw), &choice) != nil || !validProjectColor(choice.Default) {
			return fmt.Errorf("project color unavailable")
		}
		choice.Custom = strings.ToLower(color)
		_, err = tx.ExecContext(ctx, `UPDATE ui_preferences SET value=? WHERE key=?`, jsonText(choice), key)
		return err
	})
}

// OKLab distance separates perceived colors, rather than RGB channel values.
// Defaults share a luminance of .21: >=3:1 against both the light page (#eee7d8)
// and the dark panel (#183529). They are marks beside normal text, not text ink.
func projectColorLab(color string) [3]float64 {
	var r, g, b uint8
	fmt.Sscanf(color, "#%02x%02x%02x", &r, &g, &b)
	linear := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= .04045 {
			return x / 12.92
		}
		return math.Pow((x+.055)/1.055, 2.4)
	}
	rr, gg, bb := linear(r), linear(g), linear(b)
	l := math.Cbrt(.4122214708*rr + .5363325363*gg + .0514459929*bb)
	m := math.Cbrt(.2119034982*rr + .6806995451*gg + .1073969566*bb)
	s := math.Cbrt(.0883024619*rr + .2817188376*gg + .6299787005*bb)
	return [3]float64{.2104542553*l + .793617785*m - .0040720468*s, 1.9779984951*l - 2.428592205*m + .4505937099*s, .0259040371*l + .7827717662*m - .808675766*s}
}

func projectLabDistance(a, b [3]float64) float64 {
	return math.Sqrt(math.Pow(a[0]-b[0], 2) + math.Pow(a[1]-b[1], 2) + math.Pow(a[2]-b[2], 2))
}

func projectHue(color string) float64 {
	lab := projectColorLab(color)
	return math.Atan2(lab[2], lab[1]) * 180 / math.Pi
}

func projectHueDistance(a, b float64) float64 {
	difference := math.Mod(math.Abs(a-b), 360)
	return math.Min(difference, 360-difference)
}

func projectHueColor(hue, chroma float64) string {
	a, b := chroma*math.Cos(hue*math.Pi/180), chroma*math.Sin(hue*math.Pi/180)
	rgb := func(light float64) [3]float64 {
		l, m, s := math.Pow(light+.3963377774*a+.2158037573*b, 3), math.Pow(light-.1055613458*a-.0638541728*b, 3), math.Pow(light-.0894841775*a-1.291485548*b, 3)
		return [3]float64{4.0767416621*l - 3.3077115913*m + .2309699292*s, -1.2684380046*l + 2.6097574011*m - .3413193965*s, -.0041960863*l - .7034186147*m + 1.707614701*s}
	}
	for {
		low, high := 0.0, 1.0
		for range 32 {
			mid := (low + high) / 2
			c := rgb(mid)
			if .2126*c[0]+.7152*c[1]+.0722*c[2] < .21 {
				low = mid
			} else {
				high = mid
			}
		}
		c := rgb((low + high) / 2)
		if c[0] >= 0 && c[0] <= 1 && c[1] >= 0 && c[1] <= 1 && c[2] >= 0 && c[2] <= 1 {
			channel := func(x float64) int {
				if x <= .0031308 {
					x *= 12.92
				} else {
					x = 1.055*math.Pow(x, 1/2.4) - .055
				}
				return int(math.Round(x * 255))
			}
			return fmt.Sprintf("#%02x%02x%02x", channel(c[0]), channel(c[1]), channel(c[2]))
		}
		a *= .94
		b *= .94
	}
}

func allocateProjectColor(id, hint string, used []string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(id))
	seed := float64(hash.Sum32()%36000) / 100
	brand := validProjectColor(hint)
	if brand {
		seed = projectHue(hint)
	}
	labs := make([][3]float64, 0, len(used))
	taken := map[string]bool{}
	for _, color := range used {
		if validProjectColor(color) {
			labs = append(labs, projectColorLab(color))
			taken[strings.ToLower(color)] = true
		}
	}
	best, score := "", -1.0
	for i := 0; i < 720; i++ {
		hue := seed + float64(i)*137.507764
		if brand {
			hue = seed + float64(i%49-24)
			if i == 0 {
				hue = seed
			}
		}
		color := projectHueColor(hue, .12+float64(i%5)*.025)
		if taken[color] {
			continue
		}
		lab, nearest := projectColorLab(color), 1.0
		for _, other := range labs {
			nearest = math.Min(nearest, projectLabDistance(lab, other))
		}
		// Keep an unoccupied brand hue; otherwise try nearby shades first.
		if i == 0 && brand && nearest >= .065 {
			return color
		}
		if nearest > score {
			best, score = color, nearest
		}
	}
	if brand && score < .055 {
		return allocateProjectColor(id, "", used)
	}
	if best != "" {
		return best
	}
	// Exhausting a perceptual palette must not reuse an exact color. Keep the
	// same luminance and search smaller hue/chroma steps for large libraries.
	for i := 720; i < 10720; i++ {
		color := projectHueColor(seed+float64(i)*137.507764, .08+float64(i%701)/5000)
		if !taken[color] {
			return color
		}
	}
	return projectHueColor(seed, .18)
}
