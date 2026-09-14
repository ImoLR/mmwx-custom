package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultReleaseCachePath       = "/etc/mmwx-custom/release-cache.json"
	defaultReleaseRefreshInterval = 15 * time.Minute
	maxReleaseMetadataBytes       = 256 << 10
)

var customVersion = "dev"

type componentVersions struct {
	CustomVersion string `json:"custom_version"`
	HelperVersion string `json:"helper_version"`
	CoreVersion   string `json:"core_version"`
}

type releaseArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type cachedReleaseInfo struct {
	Tag             string                     `json:"tag"`
	CustomVersion   string                     `json:"latest_custom_version"`
	HelperVersion   string                     `json:"latest_helper_version"`
	CoreVersion     string                     `json:"latest_core_version"`
	HelperArtifacts map[string]releaseArtifact `json:"helper_artifacts"`
	CoreArtifacts   map[string]releaseArtifact `json:"core_artifacts"`
	FetchedAt       time.Time                  `json:"fetched_at"`
}

type releaseCacheResponse struct {
	Success              bool              `json:"success"`
	CurrentCustomVersion string            `json:"current_custom_version"`
	Cached               bool              `json:"cached"`
	LastError            string            `json:"last_error,omitempty"`
	Info                 cachedReleaseInfo `json:"release"`
}

type releaseCache struct {
	path   string
	client *http.Client

	mu        sync.RWMutex
	info      cachedReleaseInfo
	lastError string
}

func newReleaseCache(path string) *releaseCache {
	if strings.TrimSpace(path) == "" {
		path = defaultReleaseCachePath
	}
	cache := &releaseCache{path: path, client: &http.Client{Timeout: 20 * time.Second}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &cache.info)
	}
	return cache
}

func (cache *releaseCache) snapshot() (cachedReleaseInfo, string) {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return cache.info, cache.lastError
}

func (cache *releaseCache) run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultReleaseRefreshInterval
	}
	cache.refreshAndRecord(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cache.refreshAndRecord(ctx)
		}
	}
}

func (cache *releaseCache) refreshAndRecord(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	info, err := cache.fetch(ctx)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if err != nil {
		cache.lastError = err.Error()
		return
	}
	cache.info = info
	cache.lastError = ""
	if err := saveReleaseCache(cache.path, info); err != nil {
		cache.lastError = err.Error()
		log.Printf("[mmwx-custom] release cache persistence failed: %v", err)
	}
}

func (cache *releaseCache) fetch(ctx context.Context) (cachedReleaseInfo, error) {
	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := cache.fetchJSON(ctx, "https://api.github.com/repos/ImoLR/mmwx-custom/releases/latest", &release); err != nil {
		return cachedReleaseInfo{}, err
	}
	assets := make(map[string]string, len(release.Assets))
	for _, asset := range release.Assets {
		assets[asset.Name] = asset.URL
	}
	versionsURL, versionsOK := assets["component-versions.json"]
	checksumsURL, checksumsOK := assets["checksums.txt"]
	if !versionsOK || !checksumsOK {
		return cachedReleaseInfo{}, errors.New("latest release has no component metadata")
	}
	var versions componentVersions
	if err := cache.fetchJSON(ctx, versionsURL, &versions); err != nil {
		return cachedReleaseInfo{}, err
	}
	checksumsBody, err := cache.fetchBytes(ctx, checksumsURL)
	if err != nil {
		return cachedReleaseInfo{}, err
	}
	checksums, err := parseReleaseChecksums(checksumsBody)
	if err != nil {
		return cachedReleaseInfo{}, err
	}
	info := cachedReleaseInfo{
		Tag: release.TagName, CustomVersion: versions.CustomVersion, HelperVersion: versions.HelperVersion, CoreVersion: versions.CoreVersion,
		HelperArtifacts: map[string]releaseArtifact{}, CoreArtifacts: map[string]releaseArtifact{}, FetchedAt: time.Now().UTC(),
	}
	for _, arch := range []string{"amd64", "arm64"} {
		helperName := "mmwxc-helper-linux-" + arch
		coreName := "mmwxc-core-linux-" + arch
		if assets[helperName] == "" || checksums[helperName] == "" || assets[coreName] == "" || checksums[coreName] == "" {
			return cachedReleaseInfo{}, fmt.Errorf("latest release metadata is incomplete for %s", arch)
		}
		info.HelperArtifacts[arch] = releaseArtifact{URL: assets[helperName], SHA256: checksums[helperName]}
		info.CoreArtifacts[arch] = releaseArtifact{URL: assets[coreName], SHA256: checksums[coreName]}
	}
	if info.Tag == "" || info.CustomVersion == "" || info.HelperVersion == "" || info.CoreVersion == "" {
		return cachedReleaseInfo{}, errors.New("latest release component versions are incomplete")
	}
	return info, nil
}

func (cache *releaseCache) fetchJSON(ctx context.Context, rawURL string, target any) error {
	data, err := cache.fetchBytes(ctx, rawURL)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	return decoder.Decode(target)
}

func (cache *releaseCache) fetchBytes(ctx context.Context, rawURL string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "mmwx-custom-release-cache/"+customVersion)
	response, err := cache.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("release metadata HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, maxReleaseMetadataBytes))
}

func parseReleaseChecksums(data []byte) (map[string]string, error) {
	result := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || len(fields[0]) != 64 {
			continue
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		result[name] = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("release checksums are empty")
	}
	return result, nil
}

func saveReleaseCache(path string, info cachedReleaseInfo) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func (a *app) releaseInfoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	info, lastError := a.releaseCache.snapshot()
	writeJSON(w, http.StatusOK, releaseCacheResponse{Success: true, CurrentCustomVersion: customVersion, Cached: !info.FetchedAt.IsZero(), LastError: lastError, Info: info})
}
