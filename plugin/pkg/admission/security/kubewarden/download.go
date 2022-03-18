package kubewarden

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"k8s.io/klog/v2"
	orascnt "oras.land/oras-go/pkg/content"
	"oras.land/oras-go/pkg/oras"
)

const (
	ContentLayerMediaType = "application/vnd.wasm.content.layer.v1+wasm"
)

func buildDownloadDestination(ref, destDir string) string {
	path := strings.ReplaceAll(ref, "/", string(os.PathSeparator))
	path = fmt.Sprintf("%s.wasm", path)

	return filepath.Join(destDir, path)
}

// Download a Wasm module which is stored as an OCI artifact inside of a
// container registry.
// * `ref`: OCI reference (e.g.: "ghcr.io/kubewarden/tests/pod-privileged:v0.1.9")
// * `destDir`: directory where the wasm file is going to be saved

func DownloadWasmFromRegistry(ctx context.Context, ref, destDir string) (string, error) {
	dest := buildDownloadDestination(ref, destDir)
	klog.Infof("Downloading Wasm policy %s to %s", ref, dest)

	if _, err := os.Stat(dest); err == nil {
		klog.Infof("Wasm policy %s found on disk", ref)
		return dest, nil
	}

	store := orascnt.NewMemory()

	registry, err := orascnt.NewRegistry(orascnt.RegistryOptions{
		PlainHTTP: false,
		Insecure:  false})
	if err != nil {
		return "", err
	}

	allowedMediaTypes := []string{ContentLayerMediaType}

	desc, err := oras.Copy(ctx, registry, ref, store, "", oras.WithAllowedMediaTypes(allowedMediaTypes))
	if err != nil {
		return "", err
	}

	_, content, found := store.Get(desc)
	if !found {
		return "", errors.New("Cannot fetch manifest")
	}

	var manifest ocispec.Manifest
	if err = json.Unmarshal(content, &manifest); err != nil {
		return "", err
	}
	if len(manifest.Layers) != 1 {
		return "", fmt.Errorf(
			"OCI artifact is expected to have just one layer, found %d layers instead",
			len(manifest.Layers))
	}

	_, content, found = store.GetByName(manifest.Layers[0].Digest.String())
	if !found {
		return "", errors.New("Cannot find Wasm layer data")
	}

	path, _ := filepath.Split(dest)
	if err = os.MkdirAll(path, 0750); err != nil {
		return "", err
	}

	if err = os.WriteFile(dest, content, 0644); err != nil {
		return "", err
	}

	klog.Infof("Wasm policy %s downloaded", ref)
	return dest, nil
}
