package archive

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Brand discovery is bounded, local and passive: never execute repository
// code, fetch a remote icon, follow an SVG reference, or walk a checkout.
// OpenRoot confines symlinks to the chosen checkout. Missing/inaccessible
// checkouts are normal (a library can be opened on a different machine).
func projectBrandHint(row map[string]any) (string, string) {
	var locations []string
	_ = json.Unmarshal([]byte(firstString(row["locations"])), &locations)
	if row["kind"] != "repository" {
		return "", "Automatic palette"
	}
	sort.Strings(locations)
	for _, location := range locations {
		if !filepath.IsAbs(location) {
			continue
		}
		root, err := os.OpenRoot(location)
		if err != nil {
			continue
		}
		hint, source := projectBrandInRoot(root)
		_ = root.Close()
		if hint != "" {
			return hint, source
		}
	}
	return "", "Automatic palette"
}

func projectBrandRead(root *os.Root, path string) []byte {
	// Stat first so a pipe/device cannot block the reader. OpenRoot also
	// rejects paths and symlinks that escape this checkout.
	info, err := root.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512<<10 {
		return nil
	}
	file, err := root.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	value, _ := io.ReadAll(io.LimitReader(file, (512<<10)+1))
	if len(value) > 512<<10 {
		return nil
	}
	return value
}

var projectSVGColor = regexp.MustCompile(`(?i)(?:fill|stroke)\s*[:=]\s*["']?\s*(#[0-9a-f]{6}|#[0-9a-f]{3})\b`)

func projectBrandInRoot(root *os.Root) (string, string) {
	for _, path := range []string{"site.webmanifest", "manifest.webmanifest", "manifest.json", "public/site.webmanifest", "public/manifest.webmanifest", "public/manifest.json"} {
		var manifest struct {
			Theme string `json:"theme_color"`
		}
		if json.Unmarshal(projectBrandRead(root, path), &manifest) == nil {
			if color := projectBrandColor(manifest.Theme); color != "" {
				return color, path + " theme color"
			}
		}
	}
	for _, path := range []string{"favicon.svg", "public/favicon.svg", "app/icon.svg", "src/app/icon.svg", "favicon.png", "public/favicon.png", "app/icon.png", "src/app/icon.png", "public/apple-touch-icon.png", "favicon.ico", "public/favicon.ico", "app/favicon.ico", "src/app/favicon.ico"} {
		data := projectBrandRead(root, path)
		if strings.HasSuffix(path, ".svg") {
			for _, match := range projectSVGColor.FindAllSubmatch(data, 32) {
				if color := projectBrandColor(string(match[1])); color != "" {
					return color, path
				}
			}
		} else if color := projectIconColor(data); color != "" {
			return color, path
		}
	}
	return "", ""
}

func projectBrandColor(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) == 4 && value[0] == '#' {
		value = "#" + strings.Repeat(value[1:2], 2) + strings.Repeat(value[2:3], 2) + strings.Repeat(value[3:4], 2)
	}
	if !validProjectColor(value) {
		return ""
	}
	lab := projectColorLab(value)
	if math.Hypot(lab[1], lab[2]) < .045 {
		return ""
	} // neutral backgrounds are weak brand signals
	return value
}

func projectIconColor(data []byte) string {
	// Modern ICO files often contain PNG frames. Read only those frames;
	// unsupported bitmap encodings simply leave discovery to the fallback.
	if len(data) >= 6 && bytes.Equal(data[:4], []byte{0, 0, 1, 0}) {
		count := min(int(binary.LittleEndian.Uint16(data[4:6])), 32)
		for i := 0; i < count && 6+(i+1)*16 <= len(data); i++ {
			entry := data[6+i*16 : 6+(i+1)*16]
			size, offset := uint64(binary.LittleEndian.Uint32(entry[8:12])), uint64(binary.LittleEndian.Uint32(entry[12:16]))
			if size > 0 && offset >= uint64(6+count*16) && offset+size <= uint64(len(data)) && bytes.HasPrefix(data[offset:offset+size], []byte("\x89PNG\r\n\x1a\n")) {
				if color := projectIconColor(data[offset : offset+size]); color != "" {
					return color
				}
			}
		}
		return ""
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 1024 || config.Height > 1024 {
		return ""
	}
	icon, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	// Quantized, alpha-weighted pixels ignore transparent padding and
	// near-neutral foreground/background. Sample at most about 4096 pixels.
	counts := map[string]uint64{}
	bounds := icon.Bounds()
	step := max(1, max(bounds.Dx(), bounds.Dy())/64)
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, a := icon.At(x, y).RGBA()
			if a < 0x8000 {
				continue
			}
			// Clamp rather than wrap a quantized channel near white.
			channel := func(v uint32) byte { return byte(min(255, (v*255/a+8)/16*16)) }
			const hex = "0123456789abcdef"
			value := []byte{'#', 0, 0, 0, 0, 0, 0}
			for i, c := range []byte{channel(r), channel(g), channel(b)} {
				value[1+i*2] = hex[c>>4]
				value[2+i*2] = hex[c&15]
			}
			if color := projectBrandColor(string(value)); color != "" {
				counts[color] += uint64(a)
			}
		}
	}
	best := ""
	var weight uint64
	for color, count := range counts {
		if count > weight || count == weight && color < best {
			best, weight = color, count
		}
	}
	return best
}
