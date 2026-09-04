package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type dockerConfig struct {
	Auths map[string]struct {
		Auth string `json:"auth"`
	} `json:"auths"`
}

// ResolveImageID returns a sha256 digest when available, otherwise the image reference.
func ResolveImageID(ctx context.Context, image string, dockerConfigJSON []byte) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	if digest := digestFromReference(image); digest != "" {
		return digest
	}
	digest, err := fetchDigest(ctx, image, dockerConfigJSON)
	if err == nil && digest != "" {
		return digest
	}
	return image
}

func digestFromReference(image string) string {
	if idx := strings.Index(image, "@sha256:"); idx >= 0 {
		return image[idx+1:]
	}
	return ""
}

func fetchDigest(ctx context.Context, image string, dockerConfigJSON []byte) (string, error) {
	registry, repository, tag, err := parseImageReference(image)
	if err != nil {
		return "", err
	}
	if tag == "" {
		tag = "latest"
	}
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repository, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")
	if auth := basicAuthForRegistry(registry, dockerConfigJSON); auth != "" {
		req.Header.Set("Authorization", "Basic "+auth)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("registry unauthorized for %s", image)
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("registry manifest %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	digest := strings.TrimPrefix(resp.Header.Get("Docker-Content-Digest"), "sha256:")
	if digest == resp.Header.Get("Docker-Content-Digest") {
		return resp.Header.Get("Docker-Content-Digest"), nil
	}
	if digest != "" {
		return "sha256:" + digest, nil
	}
	return "", fmt.Errorf("registry did not return digest for %s", image)
}

func parseImageReference(image string) (registry, repository, tag string, err error) {
	ref := image
	if strings.HasPrefix(ref, "docker://") {
		ref = strings.TrimPrefix(ref, "docker://")
	}
	slash := strings.Index(ref, "/")
	if slash < 0 {
		return "", "", "", fmt.Errorf("invalid image reference %q", image)
	}
	registry = ref[:slash]
	repository = ref[slash+1:]
	if at := strings.Index(repository, "@"); at >= 0 {
		repository = repository[:at]
		return registry, repository, "", nil
	}
	if colon := strings.LastIndex(repository, ":"); colon >= 0 {
		tag = repository[colon+1:]
		repository = repository[:colon]
	}
	if !strings.Contains(registry, ".") && !strings.Contains(registry, ":") && registry != "localhost" {
		registry = "docker.io"
		if !strings.Contains(repository, "/") {
			repository = "library/" + repository
		}
	}
	return registry, repository, tag, nil
}

func basicAuthForRegistry(registry string, dockerConfigJSON []byte) string {
	if len(dockerConfigJSON) == 0 {
		return ""
	}
	cfg := dockerConfig{}
	if err := json.Unmarshal(dockerConfigJSON, &cfg); err != nil {
		return ""
	}
	candidates := []string{registry, "https://" + registry, "https://" + registry + "/v1/", "https://" + registry + "/v2/"}
	for host, entry := range cfg.Auths {
		for _, candidate := range candidates {
			if host == candidate || strings.TrimSuffix(host, "/") == strings.TrimSuffix(candidate, "/") {
				return entry.Auth
			}
		}
	}
	if entry, ok := cfg.Auths[registry]; ok {
		return entry.Auth
	}
	for host, entry := range cfg.Auths {
		if strings.Contains(registry, strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")) {
			return entry.Auth
		}
	}
	_ = base64.StdEncoding
	return ""
}
