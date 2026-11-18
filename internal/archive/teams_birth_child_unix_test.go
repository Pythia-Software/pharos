//go:build darwin || linux

package archive

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTeamsManagedChildHelper(t *testing.T) {
	config := os.Getenv("PHAROS_BIRTH_TEST_CONFIG")
	if config == "" {
		return
	}
	if err := Run([]string{"--config", config, "serve", "--dynamic", "--endpoint", os.Getenv("PHAROS_BIRTH_TEST_ENDPOINT"), "--application", os.Getenv("PHAROS_BIRTH_TEST_APP")}); err != nil {
		t.Fatal(err)
	}
}

func TestTeamsManagedCreateAndChildRestart(t *testing.T) {
	slowTest(t)
	useHost(t, "host_pharos_test")
	t.Setenv("PHAROS_EXPERIMENTAL_MANAGED_BIRTH", "1")
	support := t.TempDir()
	t.Setenv("PHAROS_SUPPORT_DIR", support)
	appPath := filepath.Join(t.TempDir(), "Synthetic.app")
	manager, err := newApplicationManager(t.Context(), appPath)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "Fresh")
	response := httptest.NewRecorder()
	manager.api(response, httptest.NewRequest(http.MethodPost, "/api/app/create", strings.NewReader(jsonText(map[string]any{"name": "Fresh", "path": target, "location": "mac"}))))
	if response.Code != http.StatusOK || len(manager.registry.Libraries) != 1 {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	endpoint := filepath.Join(t.TempDir(), "endpoint.json")
	for attempt := 0; attempt < 2; attempt++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTeamsManagedChildHelper$")
		cmd.Env = append(os.Environ(), "PHAROS_TEST_SUPPORT_DIR="+support, "PHAROS_BIRTH_TEST_CONFIG="+filepath.Join(target, libraryConfigName), "PHAROS_BIRTH_TEST_APP="+appPath, "PHAROS_BIRTH_TEST_ENDPOINT="+endpoint)
		cmd.Stderr = os.Stderr
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		deadline := time.NewTimer(20 * time.Second)
		tick := time.NewTicker(20 * time.Millisecond)
		var ep applicationEndpoint
		for ep.URL == "" {
			if b, e := os.ReadFile(endpoint); e == nil {
				if e = json.Unmarshal(b, &ep); e != nil {
					t.Fatal(e)
				}
				break
			}
			select {
			case e := <-done:
				t.Fatalf("child exited before serving: %v", e)
			case <-deadline.C:
				_ = cmd.Process.Kill()
				t.Fatal("child did not serve")
			case <-tick.C:
			}
		}
		deadline.Stop()
		tick.Stop()
		request, _ := http.NewRequest(http.MethodGet, ep.URL+"/api/ready", nil)
		request.Header.Set("Authorization", "Bearer "+ep.Token)
		client := http.Client{Timeout: 2 * time.Second}
		result, e := client.Do(request)
		if e != nil {
			_ = cmd.Process.Kill()
			t.Fatal(e)
		}
		result.Body.Close()
		if result.StatusCode != http.StatusOK {
			_ = cmd.Process.Kill()
			t.Fatalf("child readiness: %d", result.StatusCode)
		}
		if e = cmd.Process.Signal(syscall.SIGTERM); e != nil {
			t.Fatal(e)
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("child did not stop")
		}
	}
}
