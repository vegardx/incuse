// Package runner builds the cloud-init payload that turns a fresh
// Ubuntu cloud image into a one-shot GitHub Actions runner: download
// actions/runner, write a systemd unit that runs it once with a JIT
// config, then power off when the unit exits.
package runner

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Release is the resolved actions/runner release that cloud-init will
// install. Populated by LatestResolver from the GitHub releases API.
type Release struct {
	// Version is the runner version with the leading "v" stripped
	// (e.g. "2.328.0"). Used for log lines and the unpacked directory
	// path inside the VM.
	Version string

	// DownloadURL is the selected Linux tarball URL — the value from
	// the GitHub release asset's browser_download_url field.
	DownloadURL string

	// SHA256 is the digest GitHub publishes for the selected asset.
	// cloud-init verifies this before extracting the archive.
	SHA256 string
}

// LatestResolver resolves the latest actions/runner release for the
// architecture incuse hands out. It caches the result so a burst of
// runner spawns hits api.github.com once, not once per runner.
type LatestResolver struct {
	httpClient *http.Client
	endpoint   string
	ttl        time.Duration

	mu     sync.Mutex
	cached map[string]cachedRelease
}

type cachedRelease struct {
	release Release
	at      time.Time
}

// NewLatestResolver returns a resolver pointing at the public GitHub
// API. ttl is how long a successful resolution stays in the cache
// before the next call refreshes; zero means "never cache" (used in
// tests).
func NewLatestResolver(ttl time.Duration) *LatestResolver {
	return &LatestResolver{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		endpoint:   "https://api.github.com/repos/actions/runner/releases/latest",
		ttl:        ttl,
		cached:     make(map[string]cachedRelease),
	}
}

// Resolve returns the latest release, fetching from GitHub if the
// cache is empty or expired. Concurrent callers serialise on the
// network fetch — the second-arrival fast-path returns the cached
// value once the first arrival populates it.
func (r *LatestResolver) Resolve(ctx context.Context, arch string) (Release, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if cached, ok := r.cached[arch]; ok &&
		r.ttl > 0 && time.Since(cached.at) < r.ttl {
		return cached.release, nil
	}

	rel, err := r.fetch(ctx, arch)
	if err != nil {
		if cached, ok := r.cached[arch]; ok && r.ttl > 0 {
			return cached.release, nil
		}
		return Release{}, err
	}
	if r.ttl > 0 {
		r.cached[arch] = cachedRelease{release: rel, at: time.Now()}
	}
	return rel, nil
}

type githubReleaseAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Digest      string `json:"digest"`
}

type githubReleaseResponse struct {
	TagName string               `json:"tag_name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

func (r *LatestResolver) fetch(ctx context.Context, arch string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint, nil)
	if err != nil {
		return Release{}, fmt.Errorf("building releases request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// The endpoint is unauthenticated for public repos; an Authorization
	// header would just consume rate-limit budget on the wrong account.

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("calling github releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("github releases returned %s", resp.Status)
	}

	var body githubReleaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("decoding releases response: %w", err)
	}
	if body.TagName == "" {
		return Release{}, fmt.Errorf("github releases response missing tag_name")
	}
	version := strings.TrimPrefix(body.TagName, "v")

	runnerArch, err := releaseArch(arch)
	if err != nil {
		return Release{}, err
	}
	wantSuffix := fmt.Sprintf("linux-%s-%s.tar.gz", runnerArch, version)
	for _, a := range body.Assets {
		if strings.HasSuffix(a.Name, wantSuffix) && a.DownloadURL != "" {
			digest, ok := strings.CutPrefix(a.Digest, "sha256:")
			if !ok || len(digest) != 64 {
				return Release{}, fmt.Errorf(
					"asset %s is missing a valid sha256 digest", a.Name)
			}
			if _, err := hex.DecodeString(digest); err != nil {
				return Release{}, fmt.Errorf(
					"asset %s has an invalid sha256 digest: %w", a.Name, err)
			}
			return Release{
				Version:     version,
				DownloadURL: a.DownloadURL,
				SHA256:      digest,
			}, nil
		}
	}
	return Release{}, fmt.Errorf(
		"no linux-%s asset found for tag %s", runnerArch, body.TagName)
}

func releaseArch(arch string) (string, error) {
	switch strings.ToLower(arch) {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported runner architecture %q", arch)
	}
}
