package scaler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryHost(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/kossoy/rk-gpu-runner:latest":           "ghcr.io",
		"pytorch/pytorch:2.2.0-cuda12.1-cudnn8-runtime": "https://index.docker.io/v1/",
		"ubuntu":           "https://index.docker.io/v1/",
		"localhost:5000/x": "localhost:5000",
		"registry.example.com:8443/team/img@sha256:abcd": "registry.example.com:8443",
	} {
		if got := RegistryHost(in); got != want {
			t.Errorf("RegistryHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoginFromDockerConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// "kossoy:ghp_secret" base64.
	if err := os.WriteFile(path, []byte(`{"auths":{"ghcr.io":{"auth":"a29zc295OmdocF9zZWNyZXQ="}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoginFromDockerConfig(path, "ghcr.io/kossoy/rk-gpu-runner:latest")
	if err != nil || got != "-u kossoy -p ghp_secret ghcr.io" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := LoginFromDockerConfig(path, "docker.io/library/ubuntu"); err == nil || !strings.Contains(err.Error(), "no auth entry") {
		t.Fatalf("missing registry: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"auths":{"ghcr.io":{}},"credsStore":"desktop"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoginFromDockerConfig(path, "ghcr.io/x/y"); err == nil || !strings.Contains(err.Error(), "credential helper") {
		t.Fatalf("creds store: %v", err)
	}
	if _, err := LoginFromDockerConfig(filepath.Join(dir, "missing.json"), "ghcr.io/x/y"); err == nil {
		t.Fatal("missing file accepted")
	}
}
