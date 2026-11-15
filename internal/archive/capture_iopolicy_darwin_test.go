//go:build darwin

package archive

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestCaptureIOPolicyIsScopedToTheCaptureThread(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	before, err := currentIOPolicy()
	if err != nil {
		t.Fatal(err)
	}
	var during int32
	withCaptureIOPolicy(func() { during, err = currentIOPolicy() })
	if err != nil || during != ioPolicyUtility {
		t.Fatalf("policy during capture = %d, %v", during, err)
	}
	if after, _ := currentIOPolicy(); after != before {
		t.Fatalf("policy was not restored: %d, was %d", after, before)
	}

	// Delivery discovery reads original source bytes even on idle ticks. It
	// must use utility IO without needing to register a delivery operation.
	a, b := lifecycleLibraries(t)
	file := filepath.Join(t.TempDir(), "pending.json")
	canonicalExport(t, file, "Pending", "unchanged")
	a.Sources = []SourceConfig{{Name: "pending", Kind: "canonical", Account: "local", Path: file, Enabled: true}}
	destination, err := readLibraryDescriptor(b)
	if err != nil {
		t.Fatal(err)
	}
	a.DeliveryTargetInstance = destination.Instance
	if err := capturePendingLibrary(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	reads := 0
	override(t, &captureFault, func(stage, _ string) error {
		if stage == "delivery-read" {
			reads++
			if policy, err := currentIOPolicy(); err != nil || policy != ioPolicyUtility {
				t.Fatalf("delivery preflight IO policy = %d, %v", policy, err)
			}
		}
		return nil
	})
	for range 2 {
		if plan, err := planPendingDelivery(t.Context(), a); err != nil || plan.Capture {
			t.Fatalf("unchanged preflight: %+v %v", plan, err)
		}
		if after, err := currentIOPolicy(); err != nil || after != before {
			t.Fatalf("preflight policy was not restored: %d, %v; was %d", after, err, before)
		}
	}
	if reads == 0 {
		t.Fatal("delivery preflight did not read source fingerprints")
	}
}
