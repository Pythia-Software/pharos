package archive

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image"
	"image/png"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/fogleman/gg"
)

// fingerprintVersion names the images the renderer draws, not its source, so
// a refactor that draws the same pixels keeps every stored PNG. It is the
// digest of the parity corpus's expected PNGs (testdata/fingerprint-rendering.json);
// TestFingerprintRendererVersion fails until it matches them, so a change to
// the pixels requeues the cache. A change the corpus cannot see needs a case.
const fingerprintVersion = "f7ea49c741bc794714a5e576351d6b2155373b0deff22630dae1e939b042277a"

// fingerprintSameImages lists, for a renderer version, earlier renderer IDs
// whose stored PNGs it draws byte for byte. Opening a library with one of them
// restamps its images rather than rendering every entity again. Keyed by the
// current version, so a new version starts with none.
var fingerprintSameImages = map[string][]string{
	// 0.8.9's Goja renderer, and 0.9.0's hash of the native sources. Checked
	// against 12,000 stored images of 3,000 entities in a production library.
	"f7ea49c741bc794714a5e576351d6b2155373b0deff22630dae1e939b042277a": {
		"8329d65b2cc0ad8cab8947c710244f131bb5e823c29a3a1cd44e0ffc8518803f",
		"b1cdc0a362d785aa637946f89e877f4071f611f0b34421289b985e3881bad62e",
	},
}

func fingerprintRendererVersion() string { return fingerprintVersion }

type fingerprintRenderer struct {
	// One worker owns this renderer. Reuse bounded raster/encoder scratch across
	// the backfill instead of allocating megabytes for each of its four PNGs.
	raster        map[int]gg.Context
	pngPool       fingerprintPNGPool
	output        bytes.Buffer
	pngCache      map[[32]byte]*list.Element
	pngLRU        list.List
	pngCacheBytes int
}

type fingerprintPNGCacheEntry struct {
	key  [32]byte
	data []byte
}

var fingerprintPNGCacheLimit = 64

// Titles carry counts, not pixels. A small LRU avoids rasterizing the same
// simple ribbon repeatedly across conversations. Returned PNGs are immutable;
// encoder output is copied before caching, so scratch reuse cannot alter them.
func (renderer *fingerprintRenderer) cachedRasterize(ctx context.Context, source string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest := sha256.New()
	start := strings.Index(source, "<title>")
	if start >= 0 {
		end := strings.Index(source[start:], "</title>")
		if end >= 0 {
			digest.Write([]byte(source[:start]))
			digest.Write([]byte(source[start+end+len("</title>"):]))
		} else {
			digest.Write([]byte(source))
		}
	} else {
		digest.Write([]byte(source))
	}
	key := [32]byte(digest.Sum(nil))
	if found := renderer.pngCache[key]; found != nil {
		renderer.pngLRU.MoveToFront(found)
		return found.Value.(fingerprintPNGCacheEntry).data, nil
	}
	data, err := renderer.rasterize(ctx, source)
	if err != nil {
		return nil, err
	}
	if renderer.pngCache == nil {
		renderer.pngCache = map[[32]byte]*list.Element{}
	}
	renderer.pngCache[key] = renderer.pngLRU.PushFront(fingerprintPNGCacheEntry{key, data})
	renderer.pngCacheBytes += len(data)
	for renderer.pngLRU.Len() > fingerprintPNGCacheLimit || renderer.pngCacheBytes > 4<<20 {
		oldest := renderer.pngLRU.Back()
		entry := oldest.Value.(fingerprintPNGCacheEntry)
		delete(renderer.pngCache, entry.key)
		renderer.pngCacheBytes -= len(entry.data)
		renderer.pngLRU.Remove(oldest)
	}
	return data, nil
}

type fingerprintPNGPool struct{ buffer *png.EncoderBuffer }

func (p *fingerprintPNGPool) Get() *png.EncoderBuffer       { return p.buffer }
func (p *fingerprintPNGPool) Put(buffer *png.EncoderBuffer) { p.buffer = buffer }

// Height at the standard 1024-pixel output width. The shared TypeScript
// profile registry supplies geometry; renderer tests check these together.
var fingerprintProfiles = map[string]int{"wide": 128, "card": 256}
var fingerprintThemes = []string{"light", "dark"}

func fingerprintImagesPerAppearance() int64 {
	return int64(len(fingerprintProfiles) * len(fingerprintThemes))
}

type fingerprintRenderedImage struct {
	Profile string `json:"profile"`
	Theme   string `json:"theme"`
	Events  bool   `json:"events"`
	SVG     string `json:"svg"`
	PNG     []byte `json:"-"`
	Hash    string `json:"-"`
}
type fingerprintRendered struct {
	Images   []fingerprintRenderedImage `json:"images"`
	Messages int                        `json:"messages"`
	Events   int                        `json:"events"`
	Tools    int                        `json:"tools"`
	Branches int                        `json:"branches"`
}

func newFingerprintRenderer() (*fingerprintRenderer, error) { return &fingerprintRenderer{}, nil }

func renderFingerprintSVG(ctx context.Context, input any, eventsMask int) (fingerprintRendered, error) {
	var result fingerprintRendered
	if err := ctx.Err(); err != nil {
		return result, err
	}
	data, err := json.Marshal(input)
	if err != nil {
		return result, err
	}
	var source fingerprintInput
	if err := json.Unmarshal(data, &source); err != nil {
		return result, err
	}
	prepared, err := prepareFingerprint(ctx, &source)
	if err != nil {
		return result, err
	}
	result.Messages, result.Events, result.Tools, result.Branches = prepared.messages, prepared.events, prepared.toolCount, prepared.branches
	for _, events := range []bool{true, false} {
		bit := 2
		if events {
			bit = 1
		}
		if eventsMask&bit == 0 {
			continue
		}
		for _, profile := range []string{"wide", "card"} {
			layout, err := layoutFingerprint(ctx, prepared, profile, events)
			if err != nil {
				return result, err
			}
			for _, theme := range fingerprintThemes {
				result.Images = append(result.Images, fingerprintRenderedImage{Profile: profile, Theme: theme, Events: events, SVG: fingerprintSVGSource(layout, theme)})
			}
		}
	}
	return result, ctx.Err()
}

// One worker owns the reusable raster/encoder scratch and bounded PNG cache.
// Preparation and selection cooperate with shutdown/eject cancellation too.
func (renderer *fingerprintRenderer) Render(ctx context.Context, input any, eventsMask int) (fingerprintRendered, error) {
	result, err := renderFingerprintSVG(ctx, input, eventsMask)
	if err != nil {
		return result, err
	}
	for i := range result.Images {
		image := &result.Images[i]
		image.PNG, err = renderer.cachedRasterize(ctx, image.SVG)
		if err != nil {
			return result, err
		}
		sum := sha256.Sum256(image.PNG)
		image.Hash = hex.EncodeToString(sum[:])
		image.SVG = ""
	}
	return result, ctx.Err()
}

// This is deliberately the small SVG vocabulary emitted by our own embedded
// renderer, not a general SVG/file loader. No external assets, fonts, browser,
// scripts or URLs are interpreted. Unsupported drawing commands fail closed.
type fingerprintSVG struct {
	XMLName xml.Name
	Attrs   []xml.Attr       `xml:",any,attr"`
	Nodes   []fingerprintSVG `xml:",any"`
}

func (n fingerprintSVG) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
func (n fingerprintSVG) number(name string, fallback float64) float64 {
	if n.attr(name) == "" {
		return fallback
	}
	v, _ := strconv.ParseFloat(n.attr(name), 64)
	return v
}

func (renderer *fingerprintRenderer) rasterize(ctx context.Context, source string) ([]byte, error) {
	var root fingerprintSVG
	if err := xml.Unmarshal([]byte(source), &root); err != nil {
		return nil, err
	}
	width, height := int(root.number("width", 0)), int(root.number("height", 0))
	validHeight := false
	for _, h := range fingerprintProfiles {
		validHeight = validHeight || height == h
	}
	if root.XMLName.Local != "svg" || width != 1024 || !validHeight {
		return nil, fmt.Errorf("invalid fingerprint image dimensions")
	}
	view := strings.Fields(root.attr("viewBox"))
	if len(view) != 4 {
		return nil, fmt.Errorf("invalid fingerprint viewBox")
	}
	vw, _ := strconv.ParseFloat(view[2], 64)
	if vw <= 0 {
		return nil, fmt.Errorf("invalid fingerprint scale")
	}
	scale := float64(width) / vw
	if renderer.raster == nil {
		renderer.raster = map[int]gg.Context{}
	}
	template, ok := renderer.raster[height]
	if !ok {
		template = *gg.NewContext(width, height)
		renderer.raster[height] = template
	}
	// Copy pristine drawing state; the image and rasterizer scratch are reused.
	// Each fill/stroke resets gg's rasterizer; none of the path/clip state leaks.
	dc := &template
	clear(dc.Image().(*image.RGBA).Pix)
	dc.Scale(scale, scale)
	var pattern, clip fingerprintSVG
	for _, n := range root.Nodes {
		if n.XMLName.Local == "defs" {
			for _, d := range n.Nodes {
				if d.XMLName.Local == "pattern" {
					pattern = d
				}
				if d.XMLName.Local == "clipPath" {
					clip = d
				}
			}
		}
	}
	for _, node := range root.Nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch node.XMLName.Local {
		case "title", "defs":
			continue
		case "rect":
			if node.attr("fill") == "url(#branch)" {
				dc.Push()
				for _, path := range clip.Nodes {
					dc.Push()
					if err := fingerprintTransform(dc, path.attr("transform")); err != nil {
						return nil, err
					}
					if err := fingerprintPath(dc, path.attr("d")); err != nil {
						return nil, err
					}
					dc.Pop()
				}
				dc.Clip()
				if len(pattern.Nodes) != 1 {
					return nil, fmt.Errorf("invalid fingerprint stripe pattern")
				}
				line := pattern.Nodes[0]
				fingerprintColor(dc, line.attr("stroke"), line.number("stroke-opacity", 1))
				dc.SetLineWidth(line.number("stroke-width", 1) * scale)
				dc.Rotate(40 * math.Pi / 180)
				span := math.Hypot(vw, float64(height)/scale)
				for x := -span; x <= span; x += pattern.number("width", 4) {
					dc.DrawLine(x, -span, x, span)
				}
				dc.Stroke()
				dc.Pop()
				dc.ResetClip()
				continue
			}
			dc.DrawRectangle(node.number("x", 0), node.number("y", 0), node.number("width", 0), node.number("height", 0))
		case "circle":
			dc.DrawCircle(node.number("cx", 0), node.number("cy", 0), node.number("r", 0))
		case "path":
			if err := fingerprintPath(dc, node.attr("d")); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported fingerprint element %s", node.XMLName.Local)
		}
		opacity := node.number("opacity", 1)
		fill := node.attr("fill")
		if fill != "none" && fill != "transparent" && fill != "" {
			fingerprintColor(dc, fill, opacity)
			dc.FillPreserve()
		}
		stroke := node.attr("stroke")
		if stroke != "" && stroke != "none" {
			fingerprintColor(dc, stroke, opacity)
			dc.SetLineWidth(node.number("stroke-width", 1) * scale)
			dc.SetDash()
			if node.attr("stroke-dasharray") != "" {
				var dash []float64
				for _, v := range strings.Fields(node.attr("stroke-dasharray")) {
					d, _ := strconv.ParseFloat(v, 64)
					dash = append(dash, d*scale)
				}
				dc.SetDash(dash...)
			}
			dc.SetLineCap(gg.LineCapButt)
			if node.attr("stroke-linecap") == "round" {
				dc.SetLineCap(gg.LineCapRound)
			}
			dc.StrokePreserve()
		}
		dc.ClearPath()
	}
	renderer.output.Reset()
	encoder := png.Encoder{BufferPool: &renderer.pngPool}
	if err := encoder.Encode(&renderer.output, dc.Image()); err != nil {
		return nil, err
	}
	return bytes.Clone(renderer.output.Bytes()), ctx.Err()
}

func fingerprintColor(dc *gg.Context, hexColor string, opacity float64) {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hexColor, "#"), 16, 32)
	dc.SetRGBA(float64(v>>16&255)/255, float64(v>>8&255)/255, float64(v&255)/255, opacity)
}

var fingerprintPathToken = regexp.MustCompile(`[A-Za-z]|[-+]?(?:\d*\.)?\d+(?:[eE][-+]?\d+)?`)
var fingerprintTransformToken = regexp.MustCompile(`(translate|scale)\(([^)]*)\)`)

func fingerprintTransform(dc *gg.Context, transform string) error {
	for _, m := range fingerprintTransformToken.FindAllStringSubmatch(transform, -1) {
		values := strings.Fields(strings.ReplaceAll(m[2], ",", " "))
		if len(values) != 2 {
			return fmt.Errorf("invalid fingerprint transform")
		}
		x, err := strconv.ParseFloat(values[0], 64)
		if err != nil {
			return err
		}
		y, err := strconv.ParseFloat(values[1], 64)
		if err != nil {
			return err
		}
		if m[1] == "translate" {
			dc.Translate(x, y)
		} else {
			dc.Scale(x, y)
		}
	}
	return nil
}

func fingerprintPath(dc *gg.Context, path string) error {
	tokens := fingerprintPathToken.FindAllString(path, -1)
	var command string
	x, y, sx, sy := 0.0, 0.0, 0.0, 0.0
	for i := 0; i < len(tokens); {
		if len(tokens[i]) == 1 && strings.ContainsAny(tokens[i], "MmLlHhVvCcQqZz") {
			command = tokens[i]
			i++
			if command == "Z" || command == "z" {
				dc.ClosePath()
				x, y = sx, sy
				continue
			}
		}
		count := map[string]int{"M": 2, "L": 2, "H": 1, "V": 1, "C": 6, "Q": 4}[strings.ToUpper(command)]
		if count == 0 || i+count > len(tokens) {
			return fmt.Errorf("unsupported fingerprint path %q", command)
		}
		v := make([]float64, count)
		for j := range v {
			value, err := strconv.ParseFloat(tokens[i+j], 64)
			if err != nil {
				return err
			}
			v[j] = value
		}
		i += count
		if command == strings.ToLower(command) {
			for j := range v {
				if command == "v" || j%2 == 1 {
					v[j] += y
				} else {
					v[j] += x
				}
			}
		}
		switch strings.ToUpper(command) {
		case "M":
			x, y = v[0], v[1]
			sx, sy = x, y
			dc.MoveTo(x, y)
			command = "L"
		case "L":
			x, y = v[0], v[1]
			dc.LineTo(x, y)
		case "H":
			x = v[0]
			dc.LineTo(x, y)
		case "V":
			y = v[0]
			dc.LineTo(x, y)
		case "C":
			dc.CubicTo(v[0], v[1], v[2], v[3], v[4], v[5])
			x, y = v[4], v[5]
		case "Q":
			dc.QuadraticTo(v[0], v[1], v[2], v[3])
			x, y = v[2], v[3]
		}
	}
	return nil
}
