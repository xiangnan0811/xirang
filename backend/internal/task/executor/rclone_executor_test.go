package executor

import (
	"context"
	"errors"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

func TestLegacyRcloneExecutorRejectsManagedModeBeforeSSH(t *testing.T) {
	executor := &RcloneExecutor{binary: "rclone"}
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix,
		backupasset.PublicationNativeObjectVersions,
	} {
		t.Run(string(mode), func(t *testing.T) {
			taskEntity := model.Task{
				ID: 7, ExecutorType: "rclone", RsyncSource: "/srv/source", RsyncTarget: "legacy:bucket/path",
				ExecutorConfig: `{"version":1,"publication_mode":"` + string(mode) + `","transfers":4}`,
				Node:           model.Node{ID: 9, Host: "127.0.0.1", Port: 1, Username: "reader", AuthType: "password"},
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if exitCode, err := executor.Run(ctx, taskEntity, func(string, string) {}, nil); exitCode != -1 || !errors.Is(err, backupasset.ErrForbidden) {
				t.Fatalf("legacy managed backup exit=%d err=%v", exitCode, err)
			}
			if exitCode, err := executor.RunRestore(ctx, taskEntity, func(string, string) {}, nil); exitCode != -1 || !errors.Is(err, backupasset.ErrForbidden) {
				t.Fatalf("legacy managed restore exit=%d err=%v", exitCode, err)
			}
		})
	}
}
