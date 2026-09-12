package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestLegacyRcloneExecutorCancellationDuringWaitIsBounded(t *testing.T) {
	node := startRcloneSSHTestServer(t)
	binary := writeRcloneScript(t, "printf 'Transferred: 1 MiB / 1 MiB, 100%%, 1 MiB/s, ETA 0s\\n'\nexec 1>&- 2>&-\ntrap '' TERM\nsleep 60\n")
	executor := &RcloneExecutor{binary: binary}
	taskEntity := model.Task{ExecutorType: "rclone", RsyncSource: "/srv/source", RsyncTarget: "legacy:bucket/path", Node: node}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := executor.Run(ctx, taskEntity, func(_, message string) {
			if strings.Contains(message, "Transferred:") {
				cancel()
			}
		}, nil)
		result <- err
	}()
	select {
	case err := <-result:
		var unknown *RemoteExecutionUnknownError
		if !errors.As(err, &unknown) {
			t.Fatalf("wait cancellation error=%v, want RemoteExecutionUnknownError", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rclone wait cancellation exceeded bounded lifecycle")
	}
}
