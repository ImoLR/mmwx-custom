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
		body = `{"tag_name":"v1.3.3","assets":[{"name":"component-versions.json","browser_download_url":"https://github.com/ImoLR/mmwx-custom/releases/download/v1.3.3/component-versions.json"},{"name":"checksums.txt","browser_download_url":"https://github.com/ImoLR/mmwx-custom/releases/download/v1.3.3/checksums.txt"},{"name":"mmwxc-helper-linux-amd64","browser_download_url":"https://github.com/ImoLR/mmwx-custom/releases/download/v1.3.3/mmwxc-helper-linux-amd64"},{"name":"mmwxc-helper-linux-arm64","browser_download_url":"https://github.com/ImoLR/mmwx-custom/releases/download/v1.3.3/mmwxc-helper-linux-arm64"},{"name":"mmwxc-core-linux-amd64","browser_download_url":"https://github.com/ImoLR/mmwx-custom/releases/download/v1.3.3/mmwxc-core-linux-amd64"},{"name":"mmwxc-core-linux-arm64","browser_download_url":"https://github.com/ImoLR/mmwx-custom/releases/download/v1.3.3/mmwxc-core-linux-arm64"}]}`
	case strings.HasSuffix(request.URL.Path, "/component-versions.json"):
		body = `{"custom_version":"v1.3.3","helper_version":"v0.5.3","core_version":"47b0a95"}`
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
	if info.Tag != "v1.3.3" || info.HelperVersion != "v0.5.3" || info.CoreVersion != "47b0a95" {
		t.Fatalf("release info=%#v", info)
	}
	if info.HelperArtifacts["amd64"].SHA256 != strings.Repeat("a", 64) || info.CoreArtifacts["arm64"].SHA256 != strings.Repeat("d", 64) {
		t.Fatalf("release artifacts=%#v %#v", info.HelperArtifacts, info.CoreArtifacts)
	}
}

// flvxReleaseRoundTripper serves a release whose FLVX shared Core differs
// from the Core it offers External servers (landing-scan-guard-v1 §9).
type flvxReleaseRoundTripper struct{ withAssets bool }

func (f flvxReleaseRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	base := "https://github.com/ImoLR/mmwx-custom/releases/download/v1.4.84/"
	body := ""
	switch {
	case strings.Contains(request.URL.Path, "/releases/latest"):
		names := []string{"component-versions.json", "checksums.txt", "mmwxc-helper-linux-amd64", "mmwxc-helper-linux-arm64", "mmwxc-core-linux-amd64", "mmwxc-core-linux-arm64"}
		if f.withAssets {
			names = append(names, "mmwxc-flvx-core-linux-amd64", "mmwxc-flvx-core-linux-arm64")
		}
		assets := []string{}
		for _, name := range names {
			assets = append(assets, `{"name":"`+name+`","browser_download_url":"`+base+name+`"}`)
		}
		body = `{"tag_name":"v1.4.84","assets":[` + strings.Join(assets, ",") + `]}`
	case strings.HasSuffix(request.URL.Path, "/component-versions.json"):
		body = `{"custom_version":"v1.4.84","helper_version":"v0.7.18","core_version":"2bf03c9","flvx_core_version":"f6f5844"}`
	case strings.HasSuffix(request.URL.Path, "/checksums.txt"):
		for i, name := range []string{"mmwxc-helper-linux-amd64", "mmwxc-helper-linux-arm64", "mmwxc-core-linux-amd64", "mmwxc-core-linux-arm64", "mmwxc-flvx-core-linux-amd64", "mmwxc-flvx-core-linux-arm64"} {
			body += strings.Repeat(string(rune('a'+i)), 64) + "  " + name + "\n"
		}
	default:
		return nil, errors.New("unexpected release request")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

// External servers keep being offered the release's Core; the FLVX shared
// Core channel (「升级 FLVX Core」, new shares) moves on its own.
func TestReleaseFetchFLVXCoreChannel(t *testing.T) {
	cache := &releaseCache{client: &http.Client{Transport: flvxReleaseRoundTripper{withAssets: true}}}
	info, err := cache.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.CoreVersion != "2bf03c9" || info.CoreArtifacts["amd64"].SHA256 != strings.Repeat("c", 64) || !strings.HasSuffix(info.CoreArtifacts["amd64"].URL, "/mmwxc-core-linux-amd64") {
		t.Fatalf("External Core moved: %#v", info)
	}
	version, assets := info.flvxCore()
	if version != "f6f5844" || assets["amd64"].SHA256 != strings.Repeat("e", 64) || assets["arm64"].SHA256 != strings.Repeat("f", 64) || !strings.HasSuffix(assets["arm64"].URL, "/mmwxc-flvx-core-linux-arm64") {
		t.Fatalf("FLVX Core channel %s %#v", version, assets)
	}
	if pin := flvxReleaseCore(info); pin == nil || pin.Version != "f6f5844" || pin.Artifacts["amd64"].SHA256 != strings.Repeat("e", 64) {
		t.Fatalf("FLVX pin %#v", pin)
	}
	// A declared channel without its assets is an incomplete release.
	cache = &releaseCache{client: &http.Client{Transport: flvxReleaseRoundTripper{}}}
	if _, err = cache.fetch(context.Background()); err == nil {
		t.Fatal("FLVX Core version without assets accepted")
	}
	// Releases (and caches written by older Customs) without the channel
	// pin the release's Core, as before.
	old, err := (&releaseCache{client: &http.Client{Transport: releaseRoundTripper{}}}).fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version, assets = old.flvxCore(); version != "47b0a95" || assets["amd64"].SHA256 != strings.Repeat("c", 64) || old.FLVXCoreArtifacts != nil {
		t.Fatalf("fallback %s %#v", version, assets)
	}
}
