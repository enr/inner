package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePublish(t *testing.T) {
	cases := []struct {
		in   string
		want PortPublish
	}{
		{"8080", PortPublish{"127.0.0.1", 8080, 8080}},
		{"8080:3000", PortPublish{"127.0.0.1", 8080, 3000}},
		{"127.0.0.1:18789", PortPublish{"127.0.0.1", 18789, 18789}},
		{"0.0.0.0:8080:3000", PortPublish{"0.0.0.0", 8080, 3000}},
		{"[::1]:8080", PortPublish{"::1", 8080, 8080}},
		{"[::1]:8080:3000", PortPublish{"::1", 8080, 3000}},
		{" 9000 ", PortPublish{"127.0.0.1", 9000, 9000}},
	}
	for _, c := range cases {
		got, err := ParsePublish(c.in)
		if err != nil {
			t.Errorf("ParsePublish(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParsePublish(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParsePublish_rejects(t *testing.T) {
	for _, in := range []string{
		"", "0", "65536", "http", "localhost:8080", // a name is not an IP literal
		"1.2.3.4:5:6:7", "::1:8080", "[::1]8080", "8080:abc", "1.2.3:8080",
	} {
		if _, err := ParsePublish(in); err == nil {
			t.Errorf("ParsePublish(%q) succeeded, want an error", in)
		}
	}
}

func TestPortPublish_loopback(t *testing.T) {
	for in, want := range map[string]bool{
		"8080": true, "127.0.0.1:8080": true, "[::1]:8080": true,
		"0.0.0.0:8080": false, "192.168.1.5:8080": false,
	} {
		p, err := ParsePublish(in)
		if err != nil {
			t.Fatal(err)
		}
		if p.Loopback() != want {
			t.Errorf("%q: Loopback() = %v, want %v", in, p.Loopback(), want)
		}
	}
}

func TestAppendPublish_duplicates(t *testing.T) {
	list, err := ParsePublishList([]string{"8080", "127.0.0.1:8080"})
	if err != nil {
		t.Fatalf("the same entry twice must be accepted once: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("len = %d, want 1 (identical entries collapse)", len(list))
	}
	if _, err := ParsePublishList([]string{"8080", "8080:3000"}); err == nil {
		t.Error("two entries binding the same host address must be refused")
	}
	// Same port, different host address: both can listen.
	if list, err := ParsePublishList([]string{"8080", "[::1]:8080"}); err != nil || len(list) != 2 {
		t.Errorf("different host addresses: list=%v err=%v", list, err)
	}
}

func TestBuild_publish(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profiles", "base.toml"), `
[sandbox]
publish = ["8080"]
`)
	writeFile(t, filepath.Join(dir, "profiles", "child.toml"), `
extends = "base"
[sandbox]
publish = ["9000:3000"]
`)
	writeFile(t, filepath.Join(dir, "profiles", "bad.toml"), `
[sandbox]
publish = ["localhost:80"]
`)
	l := NewLoader(dir)
	rc, err := l.Build("child")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []PortPublish{{"127.0.0.1", 8080, 8080}, {"127.0.0.1", 9000, 3000}}
	if len(rc.Publish) != 2 || rc.Publish[0] != want[0] || rc.Publish[1] != want[1] {
		t.Errorf("Publish = %+v, want %+v (union along extends)", rc.Publish, want)
	}

	if _, err := l.Build("bad"); err == nil || !strings.Contains(err.Error(), "publish") {
		t.Errorf("Build(bad) error = %v, want a publish error", err)
	}
}
