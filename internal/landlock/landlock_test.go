package landlock

import (
	"net"
	"os"
	"os/exec"
	"runtime"
	"testing"
)

const childEnv = "INNER_LANDLOCK_TEST_CHILD"

// TestMain doubles as the restricted child: Landlock cannot be undone, so the
// scope is applied in a separate process, never in the test binary itself.
func TestMain(m *testing.M) {
	if addr := os.Getenv(childEnv); addr != "" {
		runtime.LockOSThread()
		if err := ScopeAbstractUnix(); err != nil {
			os.Stderr.WriteString("scope: " + err.Error() + "\n")
			os.Exit(2)
		}
		// Created inside the domain: must stay reachable.
		own, err := net.Listen("unix", "@inner-landlock-own-"+addr)
		if err != nil {
			os.Exit(3)
		}
		defer own.Close()
		if c, err := net.Dial("unix", own.Addr().String()); err != nil {
			os.Stderr.WriteString("own socket refused: " + err.Error() + "\n")
			os.Exit(4)
		} else {
			c.Close()
		}
		// Created outside (by the parent): must be refused.
		if c, err := net.Dial("unix", addr); err == nil {
			c.Close()
			os.Stderr.WriteString("host abstract socket reachable\n")
			os.Exit(5)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestScopeAbstractUnix(t *testing.T) {
	if ABI() < ScopeABI {
		t.Skipf("kernel Landlock ABI %d < %d", ABI(), ScopeABI)
	}
	l, err := net.Listen("unix", "@inner-landlock-test")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"="+l.Addr().String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("restricted child: %v\n%s", err, out)
	}
}
