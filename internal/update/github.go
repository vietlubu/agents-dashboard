package update

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"

	"github.com/vietlubu/agents-dashboard/internal/version"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

// DesktopAsset returns the exact updater payload for a supported desktop target.
func DesktopAsset(platform, arch string) (string, error) {
	switch platform + "/" + arch {
	case "darwin/amd64":
		return "agents-dashboard-darwin-amd64.zip", nil
	case "darwin/arm64":
		return "agents-dashboard-darwin-arm64.zip", nil
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

// NewGitHubProvider uses Wails for GitHub requests and downloads, while enforcing
// the application's CalVer and exact desktop asset/checksum contracts.
func NewGitHubProvider(client *http.Client, baseURL string) (updater.Provider, error) {
	delegate, err := github.New(github.Config{
		Repository:    "vietlubu/agents-dashboard",
		ChecksumAsset: "SHA256SUMS",
		Prerelease:    false,
		HTTPClient:    client,
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
	release, err := p.Provider.Check(ctx, delegateReq)
	if err != nil || release == nil {
		return nil, err
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
