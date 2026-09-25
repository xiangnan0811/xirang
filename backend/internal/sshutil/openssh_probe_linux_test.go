//go:build linux

package sshutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// probeHelperAlive reports whether pid is still a live (non-zombie) process.
func probeHelperAlive(pid int) bool {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z" && fields[0] != "X"
}

func TestRegisterNewHostKeyWithOpenSSHReapsProxyHelpers(t *testing.T) {
	sshBinary := requireOpenSSHClient(t)
	for _, tc := range []struct {
		name string
		// proxy writes the pid of a long-lived helper to %s.
		proxy  string
		cancel bool
	}{
		// ssh blocks on a proxy that never speaks; the probe is cancelled.
		{name: "cancelled", proxy: `sh -c 'echo $$ > %s; exec sleep 60'`, cancel: true},
		// The proxy leaves a background helper and exits, so ssh exits normally.
		{name: "normal exit", proxy: `sh -c 'sleep 60 </dev/null >/dev/null 2>&1 & echo $! > %s; exit 0'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupStrictKnownHosts(t)
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "helper.pid")
			configPath := filepath.Join(dir, "ssh_config")
			config := "Host xirang-probe-helper\n  HostName 127.0.0.1\n  ProxyCommand " + strings.Replace(tc.proxy, "%s", pidFile, 1) + "\n"
			if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- RegisterNewHostKeyWithOpenSSH(ctx, []string{sshBinary, "-F", configPath}, "tester", "xirang-probe-helper")
			}()

			var pid int
			for deadline := time.Now().Add(10 * time.Second); pid == 0; {
				if raw, err := os.ReadFile(pidFile); err == nil {
					pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
				}
				if time.Now().After(deadline) {
					t.Fatal("proxy helper did not start")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if tc.cancel {
				cancel()
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("probe must not fail on an unregistered host: %v", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("probe did not return")
			}
			for deadline := time.Now().Add(5 * time.Second); probeHelperAlive(pid); {
				if time.Now().After(deadline) {
					t.Fatalf("proxy helper %d outlived the probe", pid)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}

func TestOpenSSHProbeNeverSignalsGroupAfterFinalReap(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true binary not available")
	}
	original := signalOpenSSHProbeGroup
	t.Cleanup(func() { signalOpenSSHProbeGroup = original })
	// Repeat: exec's context watcher races Wait's reap nondeterministically.
	for range 50 {
		var mu sync.Mutex
		var calls []int
		ctx, cancel := context.WithCancel(context.Background())
		signalOpenSSHProbeGroup = func(pid int) error {
			mu.Lock()
			calls = append(calls, pid)
			mu.Unlock()
			// Cancellation arrives right after the final kill, while exec is
			// about to reap the group leader and release its pid.
			cancel()
			time.Sleep(time.Millisecond)
			return original(pid)
		}
		runOpenSSHProbe(ctx, truePath, nil)
		cancel()
		mu.Lock()
		got := len(calls)
		mu.Unlock()
		if got != 1 {
			t.Fatalf("process group signalled %d times; after the final kill no signal may follow the reap", got)
		}
	}
}
