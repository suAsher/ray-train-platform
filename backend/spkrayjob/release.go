package spkrayjob

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ray-train-platform-backend/domain"
)

const releasePath = "/downloads/spk-rayjob/"
const maxReleaseArtifactBytes int64 = 128 << 20

type ReleaseArtifact struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

type ReleaseManifest struct {
	SchemaVersion  int               `json:"schemaVersion"`
	LatestVersion  string            `json:"latestVersion"`
	MinimumVersion string            `json:"minimumVersion"`
	ReleaseNotes   string            `json:"releaseNotes"`
	Artifacts      []ReleaseArtifact `json:"artifacts"`
}

func artifactFilename(osName, arch string) string {
	switch osName + "/" + arch {
	case "linux/amd64":
		return "spk-rayjob-linux-amd64"
	case "darwin/arm64":
		return "spk-rayjob-darwin-arm64"
	case "windows/amd64":
		return "spk-rayjob-windows-amd64.exe"
	default:
		return ""
	}
}

func (m ReleaseManifest) Validate() error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("unsupported release manifest schema")
	}
	if _, err := domain.CompareCLIReleaseVersions(m.LatestVersion, m.LatestVersion); err != nil {
		return err
	}
	if m.MinimumVersion != "" {
		if cmp, err := domain.CompareCLIReleaseVersions(m.MinimumVersion, m.LatestVersion); err != nil || cmp > 0 {
			return fmt.Errorf("invalid minimum release version")
		}
	}
	if len(m.ReleaseNotes) > 8000 {
		return fmt.Errorf("release notes are too large")
	}
	if len(m.Artifacts) == 0 || len(m.Artifacts) > 3 {
		return fmt.Errorf("invalid release artifacts")
	}
	seen := map[string]bool{}
	for _, a := range m.Artifacts {
		expected := artifactFilename(a.OS, a.Arch)
		digest, err := hex.DecodeString(a.SHA256)
		if expected == "" || a.Filename != expected || seen[expected] || err != nil || len(digest) != 32 || a.Size <= 0 || a.Size > maxReleaseArtifactBytes {
			return fmt.Errorf("invalid release artifact")
		}
		seen[expected] = true
	}
	return nil
}

// releaseGET uses a separate credential-free client and disallows redirects,
// including redirects within the origin, to keep executable URLs fixed.
func (client *Client) releaseGET(ctx context.Context, filename string) (*http.Response, error) {
	endpoint := *client.server
	endpoint.Path = releasePath + filename
	endpoint.RawPath = ""
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	download := &http.Client{Transport: client.httpClient.Transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("release redirects are forbidden") }}
	response, err := download.Do(request)
	if err != nil {
		return nil, fmt.Errorf("release request failed")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("release request returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func (client *Client) releaseManifest(ctx context.Context) (ReleaseManifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	response, err := client.releaseGET(ctx, "release.json")
	if err != nil {
		return ReleaseManifest{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return ReleaseManifest{}, fmt.Errorf("invalid release manifest size")
	}
	var manifest ReleaseManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("invalid release manifest JSON")
	}
	return manifest, manifest.Validate()
}

func (client *Client) checkRelease(ctx context.Context, stderr io.Writer, submitting bool) error {
	// Local/unknown builds have no comparable release identity.
	if _, err := domain.CompareCLIReleaseVersions(Version, Version); err != nil {
		return nil
	}
	m, err := client.releaseManifest(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "版本检查暂不可用；继续使用当前客户端。")
		return nil
	}
	cmp, err := domain.CompareCLIReleaseVersions(Version, m.LatestVersion)
	if err != nil {
		return nil
	}
	if m.MinimumVersion != "" {
		minimum, _ := domain.CompareCLIReleaseVersions(Version, m.MinimumVersion)
		if minimum < 0 && submitting {
			return fmt.Errorf("当前客户端 %s 低于最低版本 %s，请先运行 spk-rayjob upgrade", Version, m.MinimumVersion)
		}
	}
	if cmp < 0 {
		fmt.Fprintf(stderr, "发现新版本 %s（当前 %s），运行 spk-rayjob upgrade 升级。\n", m.LatestVersion, Version)
		if m.ReleaseNotes != "" {
			fmt.Fprintln(stderr, strings.Map(func(r rune) rune {
				if r < 32 || r == 127 {
					return ' '
				}
				return r
			}, m.ReleaseNotes))
		}
	}
	return nil
}
