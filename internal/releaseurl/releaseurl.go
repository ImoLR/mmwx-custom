package releaseurl

import (
	"errors"
	"net/url"
	"strings"
)

const DefaultAccelerator = "https://ghfast.top/"

// NormalizeAccelerator validates the global Release accelerator prefix. An
// empty value intentionally disables acceleration.
func NormalizeAccelerator(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return "", errors.New("GitHub accelerator must be an HTTPS URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("GitHub accelerator must not contain a query or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/"
	parsed.RawPath = ""
	return parsed.String(), nil
}

// IsPublicAsset limits acceleration to this project's public GitHub Release
// assets. API, metadata discovery, web and git URLs are deliberately excluded.
func IsPublicAsset(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	path := parsed.EscapedPath()
	return strings.HasPrefix(path, "/ImoLR/mmwx-custom/releases/download/") ||
		strings.HasPrefix(path, "/ImoLR/mmwx-custom/releases/latest/download/")
}

// Candidates returns the accelerator URL first and the official URL second.
// The official URL is the sole candidate when acceleration is disabled.
func Candidates(accelerator, original string) ([]string, error) {
	if !IsPublicAsset(original) {
		return nil, errors.New("URL is not a public mmwx-custom Release asset")
	}
	normalized, err := NormalizeAccelerator(accelerator)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return []string{original}, nil
	}
	return []string{normalized + original, original}, nil
}
