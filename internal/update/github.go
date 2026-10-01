package update

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/version"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

// DesktopAsset returns the exact updater payload for a supported desktop target.
func DesktopAsset(platform, arch string) (string, error) {
	switch platform + "/" + arch {
	case "darwin/arm64":
		return "agents-dashboard-macos-arm64.zip", nil
	case "windows/amd64":
		return "agents-dashboard-windows-amd64.exe", nil
	case "linux/amd64":
		return "agents-dashboard-linux-amd64.tar.gz", nil
	case "linux/arm64":
		return "agents-dashboard-linux-arm64.tar.gz", nil
	default:
		return "", fmt.Errorf("unsupported desktop update target %s/%s", platform, arch)
	}
}

type githubProvider struct {
	*github.Provider
}

type releaseStatusKey struct{}

// Wails treats empty tags as no-update. Observe only the API status so malformed
// HTTP 200 metadata cannot masquerade as 404; the delegate still reads the body.
type releaseStatusTransport struct{ next http.RoundTripper }

func (t releaseStatusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(req)
	if status, ok := req.Context().Value(releaseStatusKey{}).(*int); ok && response != nil &&
		strings.HasSuffix(req.URL.Path, "/repos/vietlubu/agents-dashboard/releases/latest") {
		*status = response.StatusCode
	}
	return response, err
}

// NewGitHubProvider uses Wails for GitHub requests and downloads, while enforcing
// the application's CalVer and exact desktop asset/checksum contracts.
func NewGitHubProvider(client *http.Client, baseURL string) (updater.Provider, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	wrapped := *client
	transport := wrapped.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	wrapped.Transport = releaseStatusTransport{next: transport}
	delegate, err := github.New(github.Config{
		Repository:    "vietlubu/agents-dashboard",
		ChecksumAsset: "SHA256SUMS",
		Prerelease:    false,
		HTTPClient:    &wrapped,
		BaseURL:       baseURL,
		AssetMatcher: func(req updater.CheckRequest, assets []github.ReleaseAsset) int {
			name, err := DesktopAsset(req.Platform, req.Arch)
			if err != nil {
				return -1
			}
			for i, asset := range assets {
				if asset.Name == name {
					return i
				}
			}
			return -1
		},
	})
	if err != nil {
		return nil, err
	}
	return &githubProvider{Provider: delegate}, nil
}

func (p *githubProvider) Check(ctx context.Context, req updater.CheckRequest) (*updater.Release, error) {
	if _, err := version.Compare(req.CurrentVersion, req.CurrentVersion); err != nil {
		return nil, fmt.Errorf("current update version: %w", err)
	}
	if _, err := DesktopAsset(req.Platform, req.Arch); err != nil {
		return nil, err
	}
	delegateReq := req
	// Wails treats a nonempty release with an empty current version as newer;
	// the real comparison below must use CalVer rather than Wails' SemVer.
	delegateReq.CurrentVersion = ""
	var status int
	ctx = context.WithValue(ctx, releaseStatusKey{}, &status)
	release, err := p.Provider.Check(ctx, delegateReq)
	if err != nil {
		return nil, err
	}
	if release == nil {
		if status == http.StatusOK {
			return nil, errors.New("read github latest release: missing or malformed release metadata")
		}
		return nil, nil
	}
	// Wails strips both v and V. Validate the original tag as well so that
	// normalization cannot turn a malformed tag into a valid release version.
	if tag, ok := release.Metadata["github.release.tag"].(string); ok {
		if _, err := version.Compare(tag, req.CurrentVersion); err != nil {
			return nil, fmt.Errorf("release update tag: %w", err)
		}
	}
	comparison, err := version.Compare(release.Version, req.CurrentVersion)
	if err != nil {
		return nil, fmt.Errorf("release update version: %w", err)
	}
	if comparison <= 0 {
		return nil, nil
	}
	if release.Verification == nil || release.Verification.DigestAlgo != "sha256" || len(release.Verification.Digest) != sha256.Size {
		return nil, fmt.Errorf("release %s requires a valid SHA256SUMS entry for %s", release.Version, release.Artifact.Filename)
	}
	return release, nil
}
