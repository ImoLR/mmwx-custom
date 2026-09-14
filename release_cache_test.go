package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network unavailable")
}

type releaseRoundTripper struct{}

func (releaseRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	body := ""
	switch {
	case strings.Contains(request.URL.Path, "/releases/latest"):
		body = `{"tag_name":"v1.3.2","assets":[{"name":"component-versions.json","browser_download_url":"https://github.com/ImoLR/mmwx-custom/releases/download/v1.3.2/component-versions.json"},{"name":"checksums.txt","browser_download_url":"https://github.com/ImoLR/mmwx-custom/releases/download/v1.3.2/checksums.txt"},{"name":"mmwxc-helper-linux-amd64","browser_download_url":"https://github.com/ImoLR/mmwx-custom/releases/download/v1.3.2/mmwxc-helper-linux-amd64"},{"name":"mmwxc-helper-linux-arm64","browser_download_url":"https://github.com/ImoLR/mmwx-custom/releases/download/v1.3.2/mmwxc-helper-linux-arm64"},{"name":"mmwxc-core-linux-amd64","browser_download_url":"https://github.com/ImoLR/mmwx-custom/releases/download/v1.3.2/mmwxc-core-linux-amd64"},{"name":"mmwxc-core-linux-arm64","browser_download_url":"https://github.com/ImoLR/mmwx-custom/releases/download/v1.3.2/mmwxc-core-linux-arm64"}]}`
	case strings.HasSuffix(request.URL.Path, "/component-versions.json"):
		body = `{"custom_version":"v1.3.2","helper_version":"v0.5.2","core_version":"6e8f098"}`
	case strings.HasSuffix(request.URL.Path, "/checksums.txt"):
		body = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  mmwxc-helper-linux-amd64\n" +
			"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb  mmwxc-helper-linux-arm64\n" +
			"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc  mmwxc-core-linux-amd64\n" +
			"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd  mmwxc-core-linux-arm64\n"
	default:
		return nil, errors.New("unexpected release request")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func TestParseReleaseChecksums(t *testing.T) {
	data := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  mmwxc-helper-linux-amd64\n" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb *mmwxc-core-linux-amd64\n")
	checksums, err := parseReleaseChecksums(data)
	if err != nil {
		t.Fatal(err)
	}
	if checksums["mmwxc-helper-linux-amd64"] == "" || checksums["mmwxc-core-linux-amd64"] == "" {
		t.Fatalf("checksums=%#v", checksums)
	}
}

func TestReleaseRefreshFailurePreservesLastGoodCache(t *testing.T) {
	previous := cachedReleaseInfo{Tag: "v1.2.22", CustomVersion: "v1.2.22", HelperVersion: "v0.4.7", CoreVersion: "6e8f098", FetchedAt: time.Now().Add(-time.Hour)}
	cache := &releaseCache{path: filepath.Join(t.TempDir(), "release.json"), client: &http.Client{Transport: failingRoundTripper{}}, info: previous}
	cache.refreshAndRecord(context.Background())
	current, lastError := cache.snapshot()
	if current.Tag != previous.Tag || current.HelperVersion != previous.HelperVersion {
		t.Fatalf("last good cache was cleared: %#v", current)
	}
	if lastError == "" {
		t.Fatal("refresh failure was not exposed")
	}
}

func TestReleaseFetchBuildsVerifiedComponentArtifacts(t *testing.T) {
	cache := &releaseCache{client: &http.Client{Transport: releaseRoundTripper{}}}
	info, err := cache.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Tag != "v1.3.2" || info.HelperVersion != "v0.5.2" || info.CoreVersion != "6e8f098" {
		t.Fatalf("release info=%#v", info)
	}
	if info.HelperArtifacts["amd64"].SHA256 != strings.Repeat("a", 64) || info.CoreArtifacts["arm64"].SHA256 != strings.Repeat("d", 64) {
		t.Fatalf("release artifacts=%#v %#v", info.HelperArtifacts, info.CoreArtifacts)
	}
}
