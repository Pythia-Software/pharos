// Command pharos-labeler-ort is Pharos's local inference worker, using ONNX
// Runtime on CPU or the optional native MLX engine on Apple silicon GPU. Pharos starts it, writes pharos-labeler/1 requests to its
// stdin and reads replies from its stdout; stderr carries diagnostics. It
// loads the ONNX Runtime shared library at run time, so it has no link-time
// dependency on it:
//
//	pharos-labeler-ort [--runtime PATH]
//
// PATH is the runtime library (libonnxruntime.dylib on macOS). Without the
// flag the worker uses $PHAROS_ORT_LIBRARY, then the library beside its own
// executable, then the dynamic loader's search path. macos/fetch-onnxruntime.sh
// downloads the pinned release. See docs/local-labeler.md.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	workerName = "pharos-labeler-ort"
	// workerVersion changes with any change to this worker's behaviour.
	workerVersion = "4"
)

// runtimeLibraryName is the runtime library's file name on this system.
var runtimeLibraryName = map[string]string{
	"darwin":  "libonnxruntime.dylib",
	"windows": "onnxruntime.dll",
}[runtime.GOOS]

func init() {
	if runtimeLibraryName == "" {
		runtimeLibraryName = "libonnxruntime.so"
	}
}

func main() {
	flags := flag.NewFlagSet(workerName, flag.ContinueOnError)
	runtimePath := flags.String("runtime", "", "the ONNX Runtime shared library (default $PHAROS_ORT_LIBRARY, then "+runtimeLibraryName+" beside this executable, then the loader's search path)")
	backend := flags.String("backend", "auto", "inference backend: auto, cpu, or mlx (Apple silicon GPU)")
	mlxRuntime := flags.String("mlx-runtime", "", "native MLX engine library (default $PHAROS_MLX_LIBRARY, then mlx/libpharos_mlx.dylib beside this executable)")
	version := flags.Bool("version", false, "print the version and exit")
	probe := flags.Bool("probe-mlx", false, "report GPU availability as JSON without loading a model")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "usage: %s [--runtime PATH] [--backend auto|cpu|mlx] [--mlx-runtime PATH]\n\nSpeaks pharos-labeler/1 on stdin and stdout.\n\n", workerName)
		flags.PrintDefaults()
	}
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() > 0 {
		flags.Usage()
		os.Exit(2)
	}
	if *version {
		fmt.Printf("%s %s (protocol pharos-labeler/1)\n", workerName, workerVersion)
		return
	}
	if *probe {
		// Runtime diagnostics must not corrupt the JSON reply on stdout.
		reply := reserveStdout()
		available, err := probeMLX(resolveMLXRuntime(*mlxRuntime))
		result := labelerProbe{Available: available}
		if err != nil {
			result.Reason = err.Error()
		}
		json.NewEncoder(reply).Encode(result)
		return
	}
	if *backend != "auto" && *backend != "cpu" && *backend != "mlx" {
		fmt.Fprintln(os.Stderr, "--backend must be auto, cpu, or mlx")
		os.Exit(2)
	}
	library := resolveRuntime(*runtimePath)
	// Only frames may reach Pharos on stdout, so the runtime's own writes to
	// stdout, if any, go to stderr instead.
	replies := reserveStdout()
	open := backendOpener(*backend, library, resolveMLXRuntime(*mlxRuntime), os.Stderr)
	// SIGTERM keeps its default action, which stops the worker at once even
	// inside a run.
	os.Exit(serve(os.Stdin, replies, os.Stderr, open))
}

// resolveRuntime picks the runtime library: the flag, the environment, the
// library beside the executable, or a bare name for the loader to search.
func resolveRuntime(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("PHAROS_ORT_LIBRARY"); env != "" {
		return env
	}
	if exe, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(exe), runtimeLibraryName)
		if _, err := os.Stat(beside); err == nil {
			return beside
		}
	}
	return runtimeLibraryName
}
