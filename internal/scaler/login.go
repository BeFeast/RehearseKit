package scaler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RegistryHost returns the registry part of an image reference
// ("ghcr.io/kossoy/rk-gpu-runner:latest" → "ghcr.io"); Docker Hub images
// map to "https://index.docker.io/v1/", the key docker uses in config.json.
func RegistryHost(image string) string {
	first, _, hasSlash := strings.Cut(image, "/")
	if !hasSlash || (!strings.Contains(first, ".") && !strings.Contains(first, ":") && first != "localhost") {
		return "https://index.docker.io/v1/"
	}
	return first
}

// LoginFromDockerConfig builds the `vastai create instance --login`
// argument ("-u USER -p PASSWORD HOST") from a docker config.json that has
// an inline base64 auth entry for the image's registry. Credential helpers
// are not supported (errors). path empty → $DOCKER_CONFIG/config.json or
// ~/.docker/config.json.
func LoginFromDockerConfig(path, image string) (string, error) {
	if path == "" {
		dir := os.Getenv("DOCKER_CONFIG")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			dir = filepath.Join(home, ".docker")
		}
		path = filepath.Join(dir, "config.json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
		CredsStore  string            `json:"credsStore"`
		CredHelpers map[string]string `json:"credHelpers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	host := RegistryHost(image)
	entry, ok := cfg.Auths[host]
	if !ok || entry.Auth == "" {
		if cfg.CredsStore != "" || cfg.CredHelpers[host] != "" {
			return "", fmt.Errorf("%s: %s uses a credential helper; set RK_SCALER_DOCKER_LOGIN instead", path, host)
		}
		return "", fmt.Errorf("%s: no auth entry for %s", path, host)
	}
	raw, err := base64.StdEncoding.DecodeString(entry.Auth)
	if err != nil {
		return "", fmt.Errorf("%s: auth for %s: %w", path, host, err)
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok || user == "" || pass == "" {
		return "", errors.New(path + ": malformed auth entry for " + host)
	}
	return fmt.Sprintf("-u %s -p %s %s", user, pass, host), nil
}
