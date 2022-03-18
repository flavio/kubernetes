package kubewarden

import (
	"context"
	"os"
	"testing"
)

func TestBuildDownloadDestination(t *testing.T) {
	tests := []struct {
		ref         string
		downloadDir string
		expectDest  string
	}{
		{
			"ghcr.io/kubewarden/tests/pod-privileged:v0.1.9",
			"/tmp",
			"/tmp/ghcr.io/kubewarden/tests/pod-privileged:v0.1.9.wasm",
		},
		{
			"ghcr.io/kubewarden/tests/pod-privileged:v0.1.9",
			"/data/policies/",
			"/data/policies/ghcr.io/kubewarden/tests/pod-privileged:v0.1.9.wasm",
		},
	}

	for _, test := range tests {
		dest := buildDownloadDestination(test.ref, test.downloadDir)
		if dest != test.expectDest {
			t.Errorf("Test %q: expected %s, got %s", test, test.expectDest, dest)
		}
	}
}

func TestDownload(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		ref         string
		downloadDir string
		succcess    bool
	}{
		{
			"ghcr.io/kubewarden/tests/pod-privileged:v0.1.9",
			t.TempDir(),
			true,
		},
		{
			"ghcr.io/kubewarden/foo:latest",
			t.TempDir(),
			false,
		},
		{
			"k8s.gcr.io/pause:latest",
			t.TempDir(),
			false,
		},
	}

	for _, test := range tests {
		wasmFile, err := DownloadWasmFromRegistry(ctx, test.ref, test.downloadDir)

		if test.succcess {
			if err != nil {
				t.Errorf("Test %+v: got an unexpected error: %v", test, err)
				continue
			}

			_, err = os.Stat(wasmFile)
			if err != nil {
				t.Errorf("Cannot find wasm module: %v", err)
			}
		} else {
			if err == nil {
				t.Errorf("Test %+v: was expecting an error", test)
			}
		}
	}

}
