package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"unicode/utf8"
)

// The smoke model tools/labeling_smoke.py writes: a hashed character
// n-gram featurizer and one logistic regression per label. Go loads it and
// scores text with the identical featurizer, so the export's representation
// is shown to behave the same on Go as in Python (testdata/labeling-parity.json).
// It is not used by the service.
const labelingSmokeModelFormat = "pharos-labeling-smoke-model-v1"

// labelingSmokeFeaturizer is the only featurizer this code implements.
var labelingSmokeFeaturizer = map[string]any{
	"name": "hashed-char-ngrams-v1", "input": "text", "units": "code_points", "lowercase": "ascii",
	"ngram_min": float64(2), "ngram_max": float64(4), "hash": "fnv1a64-utf8", "buckets": float64(1 << 18), "values": "l2-normalized-counts",
}

type labelingSmokeModel struct {
	Format     string         `json:"format"`
	Featurizer map[string]any `json:"featurizer"`
	Threshold  float64        `json:"threshold"`
	Labels     []struct {
		Label   string       `json:"label"`
		Bias    float64      `json:"bias"`
		Weights [][2]float64 `json:"weights"`
	} `json:"labels"`
	weights []map[int]float64
}

type labelingFeature struct {
	Bucket int
	Value  float64
}

// loadLabelingSmokeModel parses a model and refuses a featurizer other
// than the one implemented here.
func loadLabelingSmokeModel(data []byte) (*labelingSmokeModel, error) {
	var model labelingSmokeModel
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, err
	}
	if model.Format != labelingSmokeModelFormat {
		return nil, fmt.Errorf("model format %q, want %s", model.Format, labelingSmokeModelFormat)
	}
	for key, want := range labelingSmokeFeaturizer {
		if model.Featurizer[key] != want {
			return nil, fmt.Errorf("featurizer %s is %v, this code implements %v", key, model.Featurizer[key], want)
		}
	}
	model.weights = make([]map[int]float64, len(model.Labels))
	for index, label := range model.Labels {
		model.weights[index] = map[int]float64{}
		for _, pair := range label.Weights {
			model.weights[index][int(pair[0])] = pair[1]
		}
	}
	return &model, nil
}

// labelingSmokeFeatures are the sorted (bucket, value) pairs of text: code
// point n-grams of length 2 to 4 of the text lowercased in A-Z only, each
// hashed as its UTF-8 bytes with FNV-1a 64 into 2^18 buckets, counted and
// L2-normalized.
func labelingSmokeFeatures(text string) ([]labelingFeature, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("text is not valid UTF-8")
	}
	points := []rune(text)
	for index, r := range points {
		if r >= 'A' && r <= 'Z' {
			points[index] = r + 32
		}
	}
	counts := map[int]int{}
	var buffer []byte
	for n := 2; n <= 4; n++ {
		for start := 0; start+n <= len(points); start++ {
			buffer = buffer[:0]
			for _, r := range points[start : start+n] {
				buffer = utf8.AppendRune(buffer, r)
			}
			counts[int(fnv1a64(buffer)%(1<<18))]++
		}
	}
	buckets := make([]int, 0, len(counts))
	for bucket := range counts {
		buckets = append(buckets, bucket)
	}
	sort.Ints(buckets)
	total := 0.0
	for _, bucket := range buckets {
		total += float64(counts[bucket]) * float64(counts[bucket])
	}
	norm := math.Sqrt(total)
	features := make([]labelingFeature, len(buckets))
	for index, bucket := range buckets {
		features[index] = labelingFeature{bucket, float64(counts[bucket]) / norm}
	}
	return features, nil
}

func fnv1a64(data []byte) uint64 {
	h := uint64(0xcbf29ce484222325)
	for _, b := range data {
		h ^= uint64(b)
		h *= 0x100000001b3
	}
	return h
}

func labelingSigmoid(z float64) float64 {
	if z >= 0 {
		return 1 / (1 + math.Exp(-z))
	}
	e := math.Exp(z)
	return e / (1 + e)
}

// score returns each label's probability for text. The sum runs in
// ascending bucket order, as in Python.
func (m *labelingSmokeModel) score(text string) (map[string]float64, error) {
	features, err := labelingSmokeFeatures(text)
	if err != nil {
		return nil, err
	}
	scores := map[string]float64{}
	for index, label := range m.Labels {
		z := label.Bias
		for _, feature := range features {
			if w, ok := m.weights[index][feature.Bucket]; ok {
				z += w * feature.Value
			}
		}
		scores[label.Label] = labelingSigmoid(z)
	}
	return scores, nil
}
