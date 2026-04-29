package config

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// IsURL returns true if s has an https:// scheme.
func IsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// FetchURL downloads content from an https URL and returns the body.
// Returns an error if the scheme is not https, the response status is
// not 200 OK, or the body exceeds 1 MiB.
func FetchURL(rawURL string) ([]byte, error) {
	return fetchURL(rawURL, newHTTPSClient())
}

func newHTTPSClient() *http.Client {
	return &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: rejectHTTPSDowngrade,
	}
}

func rejectHTTPSDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("refusing redirect from https to http: %s", req.URL)
	}
	return nil
}

func fetchURL(rawURL string, client *http.Client) ([]byte, error) {
	if !IsURL(rawURL) {
		return nil, fmt.Errorf("unsupported URL scheme (only https allowed): %s", rawURL)
	}
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
