package api

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"

	"gorm.io/gorm"
)

// rcloneHTTPTraceTransport delegates every operation to the real SSH
// transport. It tees only publication-time recursive listings to a private
// diagnostic file; command results and errors are returned unchanged.
type rcloneHTTPTraceTransport struct {
	t         *testing.T
	delegate  *provider.SSHCommandTransport
	directory string
	mu        sync.Mutex
	phase     string
	sequence  int
}

func newRcloneHTTPTraceTransport(t *testing.T, db *gorm.DB, setting *settings.Service, binaries provider.ToolBinaries, directory string) *rcloneHTTPTraceTransport {
	t.Helper()
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		t.Fatal("private Rclone trace directory is unavailable or not private")
	}
	foundation := backupasset.NewFoundationService(setting)
	transport, err := provider.NewSSHCommandTransportWithConcurrencySource(sshutil.NewNodeDialer(db), func() (int, error) {
		config, err := foundation.ProviderConfig()
		return config.MaxConcurrency, err
	}, binaries)
	if err != nil {
		t.Fatal("construct real SSH observation transport")
	}
	return &rcloneHTTPTraceTransport{t: t, delegate: transport, directory: directory}
}

func (trace *rcloneHTTPTraceTransport) setPhase(phase string) {
	trace.mu.Lock()
	trace.phase = phase
	trace.mu.Unlock()
}

func (trace *rcloneHTTPTraceTransport) Run(ctx context.Context, invocation provider.CommandInvocation, limits provider.OperationLimits) (provider.CommandOutput, error) {
	return trace.delegate.Run(ctx, invocation, limits)
}

func (trace *rcloneHTTPTraceTransport) Open(ctx context.Context, invocation provider.CommandInvocation, limits provider.OperationLimits, maxBytes int64) (provider.ReadHandle, error) {
	handle, err := trace.delegate.Open(ctx, invocation, limits, maxBytes)
	if err != nil || invocation.Operation != provider.OperationRcloneManagedRecursiveList {
		return handle, err
	}
	trace.mu.Lock()
	phase := trace.phase
	if phase == "" {
		trace.mu.Unlock()
		return handle, nil
	}
	trace.sequence++
	sequence := trace.sequence
	trace.mu.Unlock()
	file, err := os.CreateTemp(trace.directory, fmt.Sprintf("rclone-%03d-%s-observation-*.json", sequence, phase))
	if err != nil {
		trace.t.Error("could not create private Rclone observation trace")
		return handle, nil
	}
	return &rcloneHTTPTraceReadHandle{ReadHandle: handle, file: file, t: trace.t}, nil
}

func (trace *rcloneHTTPTraceTransport) OpenExecution(ctx context.Context, invocation provider.CommandInvocation, limits provider.OperationLimits, maxBytes int64) (provider.CommandExecution, error) {
	return trace.delegate.OpenExecution(ctx, invocation, limits, maxBytes)
}

type rcloneHTTPTraceReadHandle struct {
	provider.ReadHandle
	file     *os.File
	t        *testing.T
	writeErr error
	close    sync.Once
}

func (handle *rcloneHTTPTraceReadHandle) Read(buffer []byte) (int, error) {
	n, err := handle.ReadHandle.Read(buffer)
	if n > 0 && handle.writeErr == nil {
		written, writeErr := handle.file.Write(buffer[:n])
		if writeErr != nil {
			handle.writeErr = writeErr
		} else if written != n {
			handle.writeErr = io.ErrShortWrite
		}
	}
	return n, err
}

func (handle *rcloneHTTPTraceReadHandle) Close() error {
	err := handle.ReadHandle.Close()
	handle.close.Do(func() {
		closeErr := handle.file.Close()
		if handle.writeErr != nil || closeErr != nil {
			handle.t.Error("could not write private Rclone observation trace")
		}
	})
	return err
}

var _ provider.CommandTransport = (*rcloneHTTPTraceTransport)(nil)
var _ provider.CommandStreamTransport = (*rcloneHTTPTraceTransport)(nil)
