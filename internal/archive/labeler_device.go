package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

type labelerDevice struct {
	Device    string `json:"device"`
	Status    string `json:"status"`
	Detail    string `json:"detail"`
	Confirmed bool   `json:"confirmed"`
}

type labelerProbeResult struct {
	Available *bool  `json:"available"`
	Reason    string `json:"reason"`
}

type labelerProbeCache struct {
	mu     sync.Mutex
	key    string
	until  time.Time
	result labelerProbeResult
}

// File identities invalidate both a probe and a worker observation when a model,
// worker or runtime is replaced. Settings reads metadata only, never model bytes.
func labelerFileKey(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return path + ":missing"
	}
	return fmt.Sprintf("%s:%d:%d", path, info.Size(), info.ModTime().UnixNano())
}

func labelerMLXRuntime(settings LabelerSettings) string {
	if settings.MLXRuntime != "" {
		return settings.MLXRuntime
	}
	if path := os.Getenv("PHAROS_MLX_LIBRARY"); path != "" {
		return path
	}
	return filepath.Join(filepath.Dir(settings.Worker), "mlx", "libpharos_mlx.dylib")
}

func labelerDeviceKey(settings LabelerSettings, model string) string {
	companion := ""
	var manifest labelrt.Manifest
	if raw, err := os.ReadFile(filepath.Join(filepath.Dir(model), "manifest.json")); err == nil && json.Unmarshal(raw, &manifest) == nil && manifest.MLX != nil && filepath.Base(manifest.MLX.Weights) == manifest.MLX.Weights {
		companion = labelerFileKey(filepath.Join(filepath.Dir(model), manifest.MLX.Weights))
	}
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s", settings.Backend, labelerFileKey(model),
		labelerFileKey(filepath.Join(filepath.Dir(model), "manifest.json")), labelerFileKey(settings.Worker),
		labelerFileKey(labelerMLXRuntime(settings)), labelerFileKey(settings.Runtime), companion)
}

// Probe the worker's own OS/build/runtime checks without starting inference.
// Older/custom workers that lack this command remain unknown, never guessed CPU.
func (cache *labelerProbeCache) probe(ctx context.Context, settings LabelerSettings) labelerProbeResult {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	key := labelerFileKey(settings.Worker) + "|" + labelerFileKey(labelerMLXRuntime(settings))
	if cache.key == key && time.Now().Before(cache.until) {
		return cache.result
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	args := append(runtimeArgs(settings), "--probe-mlx")
	raw, err := exec.CommandContext(ctx, settings.Worker, args...).Output()
	result := labelerProbeResult{}
	if err != nil || json.Unmarshal(raw, &result) != nil {
		result = labelerProbeResult{Reason: "The worker cannot report GPU availability. The device will be confirmed when labeling starts."}
	}
	if result.Available == nil && result.Reason == "" {
		result.Reason = "The device will be confirmed when labeling starts."
	}
	cache.key, cache.until, cache.result = key, time.Now().Add(time.Minute), result
	return result
}

type labelerWorkerObservation struct {
	key   string
	ready func() labelrt.WorkerInfo
}

// All inference clients record the actual Ready handshake, including auto's CPU
// fallback. Keep that observation after an idle worker exits.
func (c *Catalog) labelerRunner(ctx context.Context, settings LabelerSettings, model *labelrt.Model, cfg labelrt.RunnerConfig) (*labelrt.Runner, func(), error) {
	runner, release, err := c.labelerManager(settings).Runner(ctx, model, cfg)
	if err == nil {
		pool := runner.Pool()
		c.labelerWorkers.Store(model.Path, labelerWorkerObservation{labelerDeviceKey(settings, model.Path), func() labelrt.WorkerInfo { return pool.Ready().Worker }})
	}
	return runner, release, err
}

func (s *Server) labelerDevice(settings LabelerSettings) labelerDevice {
	unknown := func(detail string) labelerDevice {
		return labelerDevice{Device: "unknown", Status: "Device not yet confirmed", Detail: detail}
	}
	if problem := settings.labelerProblem(); problem != "" {
		return labelerDevice{Device: "unavailable", Status: "Labeling unavailable", Detail: problem}
	}
	if settings.ModelDir == "" {
		return unknown("The device will be checked after the model download.")
	}
	raw, err := os.ReadFile(filepath.Join(settings.ModelDir, "manifest.json"))
	var manifest labelrt.Manifest
	if err != nil || json.Unmarshal(raw, &manifest) != nil {
		return unknown("The model manifest is missing or unreadable.")
	}
	variant := settings.Variant
	if variant == "" {
		variant = "int8"
	}
	file := manifest.Files["onnx_"+variant]
	if file == "" || filepath.Base(file) != file {
		return unknown("The selected model variant is unavailable.")
	}
	model := filepath.Join(settings.ModelDir, file)
	if !fileExists(model) {
		return unknown("The selected model has not finished downloading.")
	}
	if stored, ok := s.Catalog.labelerWorkers.Load(model); ok {
		observed := stored.(labelerWorkerObservation)
		if observed.key == labelerDeviceKey(settings, model) {
			switch observed.ready().Device {
			case "gpu":
				return labelerDevice{"gpu", "GPU · Automatic", "Confirmed by the last worker. GPU capacity is managed automatically; CPU thread settings do not apply.", true}
			case "cpu":
				detail := "Confirmed by the last worker. CPU threads control inference parallelism."
				status := "CPU"
				if settings.Backend != "cpu" && manifest.MLX != nil && variant == "fp32" {
					detail += " Automatic GPU selection fell back to CPU."
					status = "CPU · GPU unavailable"
				}
				return labelerDevice{"cpu", status, detail, true}
			}
		}
	}
	cpu := func(reason string) labelerDevice {
		return labelerDevice{"cpu", "CPU expected", reason, false}
	}
	if settings.Backend == "cpu" {
		return cpu("CPU inference is selected in this library's configuration.")
	}
	if variant != "fp32" || manifest.MLX == nil {
		if settings.Backend == "mlx" {
			return labelerDevice{"unavailable", "GPU unavailable", "GPU inference requires an FP32 model with an MLX companion.", false}
		}
		return cpu("This model variant has no GPU companion.")
	}
	probe := s.labelerProbe.probe(s.life.ctx, settings)
	if probe.Available == nil {
		return unknown(probe.Reason)
	}
	if !*probe.Available {
		if settings.Backend == "mlx" {
			return labelerDevice{"unavailable", "GPU unavailable", probe.Reason, false}
		}
		return labelerDevice{"cpu", "CPU expected · GPU unavailable", "GPU unavailable; automatic selection will use CPU. " + probe.Reason, false}
	}
	detail := "GPU capacity is managed automatically; CPU thread settings do not apply. The worker confirms the device when it starts."
	if settings.Backend != "mlx" {
		detail += " CPU fallback is possible if GPU startup fails."
	}
	return labelerDevice{"gpu", "GPU expected · Automatic", detail, false}
}

func (s *Server) addLabelerDevice(document map[string]any, settings LabelerSettings, canChange bool) {
	device := s.labelerDevice(settings)
	document["execution"] = device
	document["threads_can_change"] = canChange && device.Device == "cpu"
	if device.Device == "cpu" {
		document["thread_choices"] = labelerThreadChoices()
	}
}
