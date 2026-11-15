package archive

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Same palette as conversation-labels.ts; cross-language fixtures check it.
var fingerprintLabelPalette = map[string]string{
	"P":                             "#28bfdf",
	"P.request":                     "#399bed",
	"P.request.work":                "#399bed",
	"P.request.work.investigate":    "#20bfae",
	"P.request.work.implement":      "#399bed",
	"P.request.work.review":         "#b36df1",
	"P.request.work.revise":         "#678fe9",
	"P.request.work.verify":         "#2bc48e",
	"P.request.input":               "#28bfdf",
	"P.request.input.information":   "#28bfdf",
	"P.request.input.clarification": "#53c8ed",
	"P.request.input.choice":        "#7b73ed",
	"P.request.input.permission":    "#b76de5",
	"P.request.input.guidance":      "#28ad96",
	"P.frame":                       "#36b4e9",
	"P.frame.scope":                 "#32b4e8",
	"P.frame.constraint":            "#4a88e3",
	"P.frame.criteria":              "#777ae5",
	"P.control":                     "#ac62df",
	"P.control.continue":            "#ac62df",
	"P.control.redirect":            "#8060dc",
	"P.control.pause":               "#9d73df",
	"P.control.cancel":              "#b760b5",
	"C":                             "#f4b632",
	"C.input":                       "#e6cd35",
	"C.input.answer":                "#e6cd35",
	"C.input.option":                "#dbb323",
	"C.input.permission":            "#e5c347",
	"C.input.permission.grant":      "#e5c347",
	"C.input.permission.deny":       "#ee7652",
	"C.input.permission.defer":      "#d7a93e",
	"C.feedback":                    "#f28a30",
	"C.feedback.accept":             "#f5c33d",
	"C.feedback.qualify":            "#f28a30",
	"C.feedback.challenge":          "#ed6742",
	"C.feedback.reject":             "#e04450",
	"C.deliberation":                "#f16b52",
	"C.deliberation.plan":           "#f47843",
	"C.deliberation.hypothesis":     "#dc54b9",
	"C.deliberation.evidence":       "#e65075",
	"C.deliberation.reconsider":     "#ef638e",
	"C.difficulty":                  "#ed505d",
	"C.difficulty.ambiguity":        "#ed7762",
	"C.difficulty.inconsistency":    "#d84d83",
	"C.difficulty.guidance":         "#e68540",
	"C.difficulty.blocker":          "#e42d48",
	"C.report":                      "#efbb2b",
	"C.report.intent":               "#ed7846",
	"C.report.progress":             "#f4b632",
	"C.report.product":              "#ef9535",
	"C.report.finding":              "#eace32",
	"C.report.verification":         "#d3c437",
	"C.report.completion":           "#f1c13d",
	"C.report.limitation":           "#d7a02f",
}
var fingerprintToolColors = map[string]string{"read": "#6d8a93", "write": "#a361c4", "edit": "#a361c4", "run": "#d2853d", "execute": "#d2853d", "git": "#348b8a", "network": "#638bd5", "delegate": "#9e78c3", "wait": "#93918d", "poll": "#93918d", "other": "#80858b"}
var fingerprintEventPalettes = [2][4]string{{"#b0102c", "#806400", "#2fa24f", "#1849b8"}, {"#ff7070", "#d2b658", "#5edc82", "#9fb6ff"}}

func fingerprintNumber(n float64) string {
	if n == 0 {
		return "0"
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

// Number(value.toFixed(2)): JS rounds exact halfway decimal values away from
// zero, while strconv's normal tie rule is even. At two places, representable
// binary halfway values are precisely the odd multiples of 1/8.
func fingerprintN(n float64) string {
	if math.Mod(math.Abs(n)*8, 2) == 1 {
		n = math.Copysign(math.Floor(float64(math.Abs(n)*100)+.5)/100, n)
		return fingerprintNumber(n)
	}
	text := strconv.FormatFloat(n, 'f', 2, 64)
	value, _ := strconv.ParseFloat(text, 64)
	return fingerprintNumber(value)
}
func fingerprintGlyph(shape int, x, y, r float64) string {
	points := [][2]float64{{0, -1.1}, {1, .75}, {-1, .75}}
	switch shape {
	case 1:
		points = [][2]float64{{0, -1.15}, {1, 0}, {0, 1.15}, {-1, 0}}
	case 2:
		points = [][2]float64{{-.8, -.8}, {.8, -.8}, {.8, .8}, {-.8, .8}}
	case 3:
		points = [][2]float64{{0, 1.1}, {1, -.75}, {-1, -.75}}
	}
	var out strings.Builder
	for i, p := range points {
		if i == 0 {
			out.WriteByte('M')
		} else {
			out.WriteByte('L')
		}
		out.WriteString(fingerprintNumber(math.Floor(float64((x+float64(p[0]*r))*10)+.5) / 10))
		out.WriteByte(' ')
		out.WriteString(fingerprintNumber(math.Floor(float64((y+float64(p[1]*r))*10)+.5) / 10))
	}
	out.WriteByte('Z')
	return out.String()
}
func fingerprintColorID(id, muted string) string {
	if id == "no-match" {
		return "transparent"
	}
	for {
		if color, ok := fingerprintLabelPalette[id]; ok {
			return color
		}
		i := strings.LastIndexByte(id, '.')
		if i < 0 {
			return muted
		}
		id = id[:i]
	}
}
func fingerprintEnvelopes(bands []fingerprintBand, height float64) string {
	var out strings.Builder
	for i := 0; i < len(bands); {
		if bands[i].parallelism <= 0 {
			i++
			continue
		}
		j := i + 1
		for j < len(bands) && bands[j].parallelism > 0 {
			j++
		}
		group := bands[i:j]
		start := group[0].x * fingerprintWidth
		last := group[len(group)-1]
		end := float64(last.x*fingerprintWidth) + float64(last.width*fingerprintWidth)
		type point struct{ x, upper, lower float64 }
		points := make([]point, len(group))
		for k, s := range group {
			half := 13 * float64(s.parallelism) / float64(s.parallelism+3)
			points[k] = point{float64(s.x*fingerprintWidth) + float64(float64(s.width*fingerprintWidth)/2), 19 - half, 19 + half}
		}
		var path strings.Builder
		num := fingerprintNumber
		fmt.Fprintf(&path, "M%s 19 Q%s %s %s %s", num(start), num(start), num(points[0].upper), num(points[0].x), num(points[0].upper))
		for k := 1; k < len(points); k++ {
			p, q := points[k-1], points[k]
			mid := (p.x + q.x) / 2
			fmt.Fprintf(&path, " C%s %s %s %s %s %s", num(mid), num(p.upper), num(mid), num(q.upper), num(q.x), num(q.upper))
		}
		p := points[len(points)-1]
		fmt.Fprintf(&path, " Q%s %s %s 19 Q%s %s %s %s", num(end), num(p.upper), num(end), num(end), num(p.lower), num(p.x), num(p.lower))
		for k := len(points) - 2; k >= 0; k-- {
			p, q := points[k+1], points[k]
			mid := (p.x + q.x) / 2
			fmt.Fprintf(&path, " C%s %s %s %s %s %s", num(mid), num(p.lower), num(mid), num(q.lower), num(q.x), num(q.lower))
		}
		fmt.Fprintf(&path, " Q%s %s %s 19 Z", num(start), num(points[0].lower), num(start))
		fmt.Fprintf(&out, `<path d="%s" transform="translate(0 0) scale(1 %s)"/>`, path.String(), num(height/38))
		i = j
	}
	return out.String()
}
func fingerprintSVGSource(result fingerprintLayout, theme string) string {
	dark := theme == "dark"
	palette := fingerprintEventPalettes[0]
	ink, muted := "#332e29", "#c4c3bf"
	if dark {
		palette = fingerprintEventPalettes[1]
		ink, muted = "#e9e2d8", "#696a6a"
	}
	p := result.prepared
	g := result.geometry
	height := result.height
	num := fingerprintN
	var bands, arcs, markers, tails, dots strings.Builder
	for _, band := range p.bands {
		y := 0.
		for _, slice := range band.slices {
			top, h := y, height*slice.share
			y += float64(h)
			fmt.Fprintf(&bands, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s"/>`, num(band.x*fingerprintWidth), num(top), num(float64(band.width*fingerprintWidth)+.05), num(h), fingerprintColorID(slice.color, muted))
		}
	}
	envelopes := fingerprintEnvelopes(p.bands, height)
	hatch := ""
	if envelopes != "" {
		hatch = fmt.Sprintf(`<rect width="320" height="%s" fill="url(#branch)" clip-path="url(#branch-envelope)"/>`, num(height))
	}
	for _, a := range g.arcs {
		dash := ""
		if a.weak {
			dash = ` stroke-dasharray="2 2"`
		}
		fmt.Fprintf(&arcs, `<path d="%s" fill="none" stroke="%s" stroke-width="0.75" opacity=".72"%s/><path d="M%s %sv4" stroke="%s" stroke-width="0.9"/>`, fingerprintArcPath(a, g.axis), ink, dash, num(a.x0), num(fingerprintArcY(a, g.axis, a.x0)-2), ink)
	}
	byID := map[string]fingerprintCandidate{}
	for _, c := range result.selected {
		byID[c.event.ID] = c
	}
	for _, m := range g.marks {
		c := byID[m.id]
		group := fingerprintGroup(c.event.Group)
		if group == 4 {
			group = 0
		}
		fill, stroke := "none", palette[group]
		if c.target != "" {
			fill, stroke = palette[group], "none"
		}
		fmt.Fprintf(&markers, `<path d="%s" fill="%s" stroke="%s" stroke-width="0.9"/>`, fingerprintGlyph(group, m.x, m.y, m.r), fill, stroke)
	}
	tailCount := 0
	for _, t := range result.tools {
		color := fingerprintToolColors[t.tool.Category]
		if color == "" {
			color = fingerprintToolColors["other"]
		}
		if t.drawEndX > t.drawX {
			tailCount++
			stroke := color
			if t.failure {
				stroke = palette[0]
			}
			fmt.Fprintf(&tails, `<path d="M%s %sH%s" fill="none" stroke="%s" stroke-width="1.2" stroke-linecap="round" opacity=".8"/>`, num(t.drawX), num(t.y), num(t.drawEndX), stroke)
		}
		failure := ""
		if t.failure {
			failure = fmt.Sprintf(` stroke="%s" stroke-width="1"`, palette[0])
		}
		fmt.Fprintf(&dots, `<circle cx="%s" cy="%s" r="%s" fill="%s"%s/>`, num(t.drawX), num(t.y), num(fingerprintToolRadius), color, failure)
	}
	summary := fmt.Sprintf("%d messages, %d branches; showing %d of %d events across %d kinds and %d phases, %d relationships; %d representative calls of %d tools, %d duration tails", p.messages, p.branches, len(result.selected), p.events, result.kinds, result.phases, len(g.arcs), len(result.tools), p.toolCount, tailCount)
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="%s" viewBox="0 0 320 %s" role="img"><title>%s</title><defs><pattern id="branch" width="4" height="4" patternUnits="userSpaceOnUse" patternTransform="rotate(40)"><path d="M0 0V4" stroke="%s" stroke-opacity=".42" stroke-width="1"/></pattern><clipPath id="branch-envelope">%s</clipPath></defs><rect width="320" height="%s" fill="%s" opacity=".12"/>%s%s%s%s%s%s</svg>`, num(1024*height/fingerprintWidth), num(height), summary, ink, envelopes, num(height), muted, bands.String(), hatch, arcs.String(), tails.String(), markers.String(), dots.String())
}

func fingerprintArcPath(a fingerprintArc, axis float64) string {
	bend := math.Min(a.rise, a.x1-a.x0)
	xs := []float64{a.x0}
	for k := 0; k <= 16; k++ {
		x := a.x0 + float64(bend*float64(k)/16)
		if a.returning {
			x = a.x1 - bend + float64(bend*float64(k)/16)
		}
		xs = append(xs, x)
	}
	xs = append(xs, a.x1)
	seen := map[float64]bool{}
	var path strings.Builder
	for _, x := range xs {
		if seen[x] {
			continue
		}
		seen[x] = true
		if path.Len() == 0 {
			path.WriteByte('M')
		} else {
			path.WriteByte('L')
		}
		path.WriteString(fingerprintN(x))
		path.WriteByte(' ')
		path.WriteString(fingerprintN(fingerprintArcY(a, axis, x)))
	}
	return path.String()
}
