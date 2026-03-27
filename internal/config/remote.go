package config

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// IsURL returns true if s has an http:// or https:// scheme.
func IsURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// FetchURL downloads content from a http or https URL and returns the body.
// Returns an error if the scheme is not http/https, the response status is
// not 200 OK, or the body exceeds 1 MiB.
func FetchURL(rawURL string) ([]byte, error) {
	if !IsURL(rawURL) {
		return nil, fmt.Errorf("unsupported URL scheme (only http/https allowed): %s", rawURL)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(rawURL) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: HTTP %d", rawURL, resp.StatusCode)
	}
	const maxSize = 1 << 20 // 1 MiB
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rawURL, err)
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("profile at %s exceeds 1 MiB limit", rawURL)
	}
	return data, nil
}
