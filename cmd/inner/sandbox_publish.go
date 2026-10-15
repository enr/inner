package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/enr/inner/internal/config"
	"github.com/enr/inner/internal/netproxy"
)

// applyPublish starts the host side of [sandbox] publish: one TCP listener per
// entry, each forwarding to a unix socket that the in-sandbox relay creates in
// a directory bind-mounted at netproxy.SandboxPublishDir. Records that
// directory on rc; applyNetworkProxy then adds the matching --publish pairs to
// the relay.
//
// A no-op without entries, and under "full": there the sandbox shares the host
// network namespace, so the server is already reachable and a host listener
// on the same port would only collide with it.
//
// The listeners are opened here, before the sandbox starts, so a port that is
// already taken stops the run with an error naming the entry instead of
// leaving a sandbox nobody can reach.
func applyPublish(rc *config.RunConfig) (func(), error) {
	noop := func() {}
	if len(rc.Publish) == 0 || rc.EffectiveNetworkMode() == config.NetworkFull {
		return noop, nil
	}

	// Its own 0700 directory, like the proxy socket: whoever can connect to
	// these sockets could already run inner as this user.
	dir, err := os.MkdirTemp("", "inner-pub-*")
	if err != nil {
		return nil, fmt.Errorf("publish: creating socket directory: %w", err)
	}

	var listeners []net.Listener
	var conns sync.Map
	var wg sync.WaitGroup
	cleanup := func() {
		for _, l := range listeners {
			_ = l.Close()
		}
		conns.Range(func(c, _ any) bool {
			_ = c.(net.Conn).Close()
			return true
		})
		wg.Wait()
		_ = os.RemoveAll(dir)
	}

	for i, p := range rc.Publish {
		l, err := net.Listen("tcp", p.HostAddr())
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("publish %s: cannot listen on the host: %w", p, err)
		}
		listeners = append(listeners, l)
		sock := filepath.Join(dir, netproxy.PublishSocketName(i))
		// The accept loop counts too, so no connection can be added to wg
		// after cleanup's Wait has seen it at zero.
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				conn, err := l.Accept()
				if err != nil {
					return // listener closed: the run is over
				}
				wg.Add(1)
				conns.Store(conn, struct{}{})
				go func() {
					defer wg.Done()
					defer conns.Delete(conn)
					forwardToSandbox(conn, sock)
				}()
			}
		}()
	}

	rc.PublishSocketDir = dir
	return cleanup, nil
}

// forwardToSandbox pipes one host connection to the relay's socket. When the
// socket is not there yet (the sandbox is still starting) the client sees its
// connection closed, the same answer as a port nobody listens on.
func forwardToSandbox(client net.Conn, sock string) {
	defer client.Close()
	upstream, err := net.Dial("unix", sock)
	if err != nil {
		return
	}
	defer upstream.Close()
	pipeConns(client, upstream)
}

// relayPublishArgs returns the --publish flags that make the relay serve the
// sockets applyPublish connects to. Empty when nothing is published.
func relayPublishArgs(rc *config.RunConfig) []string {
	if rc.PublishSocketDir == "" {
		return nil
	}
	var args []string
	for i, p := range rc.Publish {
		sock := filepath.Join(netproxy.SandboxPublishDir, netproxy.PublishSocketName(i))
		args = append(args, "--publish", sock+"="+p.SandboxAddr())
	}
	return args
}
