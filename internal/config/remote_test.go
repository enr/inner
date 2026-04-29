package config

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsURL(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"https://example.com/profile.toml", true},
		{"http://example.com/profile.toml", false},
		{"default", false},
		{"my-profile", false},
		{"/home/user/.inner/profiles/foo.toml", false},
		{"./local.toml", false},
		{"ftp://example.com/foo", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsURL(tt.input); got != tt.want {
			t.Errorf("IsURL(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestFetchURL(t *testing.T) {
	fetch := func(t *testing.T, srv *httptest.Server, rawURL string) ([]byte, error) {
		t.Helper()
		client := srv.Client()
		client.CheckRedirect = rejectHTTPSDowngrade
		return fetchURL(rawURL, client)
	}

	t.Run("returns body on 200", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`name = "test"`))
		}))
		defer srv.Close()

		data, err := fetch(t, srv, srv.URL)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != `name = "test"` {
			t.Errorf("unexpected body: %q", data)
		}
	})

	t.Run("error on non-200", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		_, err := fetch(t, srv, srv.URL)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "HTTP 404") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("error on non-https scheme", func(t *testing.T) {
		_, err := FetchURL("ftp://example.com/foo")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("error on plain http scheme", func(t *testing.T) {
		_, err := FetchURL("http://example.com/foo")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "only https allowed") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("error on empty string", func(t *testing.T) {
		_, err := FetchURL("")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("error when body exceeds 1 MiB", func(t *testing.T) {
		bigBody := strings.Repeat("x", (1<<20)+1)
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(bigBody))
		}))
		defer srv.Close()

		_, err := fetch(t, srv, srv.URL)
		if err == nil {
			t.Fatal("expected error for oversized body, got nil")
		}
		if !strings.Contains(err.Error(), "1 MiB") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("error on https to http redirect", func(t *testing.T) {
		dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`name = "downgraded"`))
		}))
		defer dst.Close()

		src := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, dst.URL+"/profile.toml", http.StatusFound)
		}))
		defer src.Close()

		_, err := fetch(t, src, src.URL+"/profile.toml")
		if err == nil {
			t.Fatal("expected redirect downgrade error, got nil")
		}
		if !strings.Contains(err.Error(), "https to http") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
