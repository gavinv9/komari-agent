package update

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/blang/semver"
	"github.com/komari-monitor/komari-agent/dnsresolver"
	"github.com/rhysd/go-github-selfupdate/selfupdate"
)

var (
	CurrentVersion string = "0.0.1"
	Repo           string = "komari-monitor/komari-agent"
)

// repoSlugRe validates GitHub repository slugs: owner/name
// Each part must start with alphanumeric, then allow alphanumeric, dot, hyphen, underscore.
var repoSlugRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*/[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// IsValidRepoSlug checks whether s is a valid "owner/repo" slug.
func IsValidRepoSlug(s string) bool {
	return repoSlugRe.MatchString(s)
}

// ApplyRepoOverride overrides the default Repo if the environment variable
// AGENT_UPDATE_REPO is set and valid. Must be called after flag parsing.
func ApplyRepoOverride() {
	if envRepo := os.Getenv("AGENT_UPDATE_REPO"); envRepo != "" {
		if IsValidRepoSlug(envRepo) {
			Repo = envRepo
		} else {
			log.Printf("[WARNING] Ignoring invalid AGENT_UPDATE_REPO=%q: must match owner/repo (alphanumeric, dot, hyphen, underscore)", envRepo)
		}
	}
}

const (
	snapshotVersionPrefix = "Snapshot-"
	containerMarkerPath   = "/.komari-agent-container"
	githubAPIBaseURL      = "https://api.github.com"
)

type buildTrack int

const (
	stableTrack buildTrack = iota
	snapshotTrack
)

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	Name        string               `json:"name"`
	Body        string               `json:"body"`
	Draft       bool                 `json:"draft"`
	Prerelease  bool                 `json:"prerelease"`
	HTMLURL     string               `json:"html_url"`
	PublishedAt time.Time            `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int    `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type snapshotReleaseCandidate struct {
	TagName     string
	Name        string
	Body        string
	HTMLURL     string
	PublishedAt time.Time
	Asset       githubReleaseAsset
}

// parseVersion 解析可能带有 v/V 前缀，以及预发布或构建元数据的版本字符串
func parseVersion(ver string) (semver.Version, error) {
	ver = strings.TrimPrefix(ver, "v")
	ver = strings.TrimPrefix(ver, "V")
	return semver.ParseTolerant(ver)
}

// needUpdate 判断是否需要更新
func needUpdate(current, latest semver.Version) bool {
	// 返回最新版本大于当前版本时需要更新
	return latest.Compare(current) > 0
}

func detectBuildTrack(version string) buildTrack {
	if strings.HasPrefix(version, snapshotVersionPrefix) {
		return snapshotTrack
	}
	return stableTrack
}

func expectedAssetName(goos, goarch string) string {
	name := fmt.Sprintf("komari-agent-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func findReleaseAsset(release githubRelease, assetName string) (githubReleaseAsset, bool) {
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			return asset, true
		}
	}
	return githubReleaseAsset{}, false
}

func selectLatestSnapshotRelease(releases []githubRelease, assetName string) (snapshotReleaseCandidate, bool) {
	var latest snapshotReleaseCandidate
	found := false

	for _, release := range releases {
		if release.Draft || !release.Prerelease || !strings.HasPrefix(release.TagName, snapshotVersionPrefix) {
			continue
		}

		asset, ok := findReleaseAsset(release, assetName)
		if !ok {
			continue
		}

		candidate := snapshotReleaseCandidate{
			TagName:     release.TagName,
			Name:        release.Name,
			Body:        release.Body,
			HTMLURL:     release.HTMLURL,
			PublishedAt: release.PublishedAt,
			Asset:       asset,
		}

		if !found ||
			candidate.PublishedAt.After(latest.PublishedAt) ||
			(candidate.PublishedAt.Equal(latest.PublishedAt) && candidate.TagName > latest.TagName) {
			latest = candidate
			found = true
		}
	}

	return latest, found
}

func snapshotNeedsUpdate(currentVersion string, latest snapshotReleaseCandidate) bool {
	return currentVersion != latest.TagName
}

func isContainerAgent() bool {
	_, err := os.Stat(containerMarkerPath)
	return err == nil
}

func splitRepoSlug(slug string) (string, string, error) {
	parts := strings.Split(slug, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo slug %q, expected owner/name", slug)
	}
	return parts[0], parts[1], nil
}

func listGitHubReleases(owner, repo string) ([]githubRelease, error) {
	var releases []githubRelease

	for page := 1; ; page++ {
		endpoint := fmt.Sprintf(
			"%s/repos/%s/%s/releases?per_page=100&page=%d",
			githubAPIBaseURL,
			url.PathEscape(owner),
			url.PathEscape(repo),
			page,
		)
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create GitHub releases request: %w", err)
		}

		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "komari-agent")
		if token := os.Getenv("GITHUB_TOKEN"); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to list GitHub releases: %w", err)
		}

		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			return nil, fmt.Errorf("GitHub releases API returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var pageReleases []githubRelease
		if err := json.NewDecoder(resp.Body).Decode(&pageReleases); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("failed to decode GitHub releases response: %w", err)
		}
		_ = resp.Body.Close()

		releases = append(releases, pageReleases...)
		if len(pageReleases) < 100 {
			return releases, nil
		}
	}
}

func currentExecutablePath() (string, error) {
	cmdPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(cmdPath, ".exe") {
		cmdPath += ".exe"
	}

	stat, err := os.Lstat(cmdPath)
	if err != nil {
		return "", fmt.Errorf("failed to stat %q: %w", cmdPath, err)
	}
	if stat.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(cmdPath)
		if err != nil {
			return "", fmt.Errorf("failed to resolve symlink %q for executable: %w", cmdPath, err)
		}
		cmdPath = resolved
	}

	return cmdPath, nil
}

func selfUpdateReleaseFromSnapshot(owner, repo string, candidate snapshotReleaseCandidate, checksumAssetID int64) *selfupdate.Release {
	publishedAt := candidate.PublishedAt
	return &selfupdate.Release{
		Version:           semver.Version{},
		AssetURL:          candidate.Asset.BrowserDownloadURL,
		AssetByteSize:     candidate.Asset.Size,
		AssetID:           candidate.Asset.ID,
		ValidationAssetID: checksumAssetID,
		URL:               candidate.HTMLURL,
		ReleaseNotes:      candidate.Body,
		Name:              candidate.Name,
		PublishedAt:       &publishedAt,
		RepoOwner:         owner,
		RepoName:          repo,
	}
}

func DoUpdateWorks() {
	ticker_ := time.NewTicker(time.Duration(6) * time.Hour)
	for range ticker_.C {
		CheckAndUpdate()
	}
}

func checkAndUpdateStable(currentSemVer semver.Version, updater *selfupdate.Updater) error {
	// 前置检查：确认最新稳定版 release 包含 checksums.txt，否则拒绝更新
	owner, repo, err := splitRepoSlug(Repo)
	if err != nil {
		return err
	}
	releases, err := listGitHubReleases(owner, repo)
	if err != nil {
		return fmt.Errorf("failed to list releases for checksum pre-check: %w", err)
	}
	checksumOK := false
	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		for _, asset := range release.Assets {
			if asset.Name == "checksums.txt" {
				checksumOK = true
				break
			}
		}
		if !checksumOK {
			log.Printf("WARNING: No checksums.txt found for release %s, skipping update for security", release.TagName)
			return nil
		}
		break
	}
	if !checksumOK && len(releases) == 0 {
		log.Println("No releases found, nothing to update")
		return nil
	}

	latest, err := updater.UpdateSelf(currentSemVer, Repo)
	if err != nil {
		return fmt.Errorf("failed to check for updates: %v", err)
	}

	if latest.Version.Equals(currentSemVer) {
		log.Println("Current version is the latest:", CurrentVersion)
		return nil
	}
	log.Printf("Successfully updated to version %s\n", latest.Version)
	os.Exit(42)
	return nil
}

func checkAndUpdateSnapshot(updater *selfupdate.Updater) error {
	if isContainerAgent() {
		log.Println("Snapshot agent is running in a container; skip binary self-update. Refresh the ghcr.io image tagged 'snapshot' instead.")
		return nil
	}

	owner, repo, err := splitRepoSlug(Repo)
	if err != nil {
		return err
	}

	releases, err := listGitHubReleases(owner, repo)
	if err != nil {
		return err
	}

	assetName := expectedAssetName(runtime.GOOS, runtime.GOARCH)
	latest, found := selectLatestSnapshotRelease(releases, assetName)
	if !found {
		log.Printf("No suitable snapshot release asset was found for %s. Current snapshot is considered up-to-date.", assetName)
		return nil
	}

	// 查找 checksums.txt 资产用于完整性校验
	checksumAssetID := findChecksumAssetID(releases, latest.TagName)
	if checksumAssetID == 0 {
		log.Printf("WARNING: No checksums.txt found for snapshot %s, skipping update for security", latest.TagName)
		return nil
	}

	if !snapshotNeedsUpdate(CurrentVersion, latest) {
		log.Println("Current snapshot version is the latest:", CurrentVersion)
		return nil
	}

	cmdPath, err := currentExecutablePath()
	if err != nil {
		return fmt.Errorf("failed to resolve current executable path: %w", err)
	}

	log.Printf("Will update %s from snapshot %s to %s\n", cmdPath, CurrentVersion, latest.TagName)
	if err := updater.UpdateTo(selfUpdateReleaseFromSnapshot(owner, repo, latest, checksumAssetID), cmdPath); err != nil {
		return fmt.Errorf("failed to update to snapshot %s: %w", latest.TagName, err)
	}

	log.Printf("Successfully updated to snapshot version %s\n", latest.TagName)
	os.Exit(42)
	return nil
}

// findChecksumAssetID 查找指定 tag 的 release 中的 checksums.txt 资产 ID
func findChecksumAssetID(releases []githubRelease, tagName string) int64 {
	for _, release := range releases {
		if release.TagName != tagName {
			continue
		}
		for _, asset := range release.Assets {
			if asset.Name == "checksums.txt" {
				return asset.ID
			}
		}
		break
	}
	return 0
}

// 检查更新并执行自动更新
func CheckAndUpdate() error {
	log.Println("Checking update...")

	http.DefaultClient = dnsresolver.GetHTTPClient(60 * time.Second)
	// 默认 Validator 行为：当 release 包含 checksums.txt 时自动校验完整性
	updater, err := selfupdate.NewUpdater(selfupdate.Config{})
	if err != nil {
		return fmt.Errorf("failed to create updater: %v", err)
	}

	if detectBuildTrack(CurrentVersion) == snapshotTrack {
		return checkAndUpdateSnapshot(updater)
	}

	currentSemVer, err := parseVersion(CurrentVersion)
	if err != nil {
		return fmt.Errorf("failed to parse current version: %v", err)
	}

	return checkAndUpdateStable(currentSemVer, updater)
}
