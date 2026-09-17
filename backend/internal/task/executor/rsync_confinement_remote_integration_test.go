//go:build linux

package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/rsyncconfinement"
	"xirang/backend/internal/task/testutil"
)

func TestRsyncConfinementRemoteSSHTransfer(t *testing.T) {
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skipf("sshd unavailable: %v", err)
	}
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skipf("rsync unavailable: %v", err)
	}
	helper := os.Getenv(rsyncconfinement.HelperPathEnv)
	if helper == "" {
		t.Skipf("%s is required for remote confinement evidence", rsyncconfinement.HelperPathEnv)
	}
	if info, statErr := os.Stat(helper); statErr != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		t.Skipf("remote helper unavailable: %v", statErr)
	}
	node := testutil.StartRsyncSSHServer(t, sshdBinary)
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv(rsyncconfinement.RemoteHelperPathEnv, helper)
	remoteRoot := t.TempDir()
	targetRoot := t.TempDir()
	source := filepath.Join(remoteRoot, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("remote-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	internalAlias := filepath.Join(remoteRoot, "source-alias")
	if err := os.Symlink(source, internalAlias); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideSource := filepath.Join(outside, "outside")
	if err := os.MkdirAll(outsideSource, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideSource, "payload"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalAlias := filepath.Join(remoteRoot, "external-alias")
	if err := os.Symlink(outsideSource, externalAlias); err != nil {
		t.Fatal(err)
	}
	t.Setenv(rsyncconfinement.AllowedSourceRootsEnv, remoteRoot)
	t.Setenv(rsyncconfinement.AllowedTargetRootsEnv, targetRoot)
	runner := &RsyncExecutor{binary: rsyncBinary}
	run := func(sourcePath, targetPath string) error {
		var logs []string
		code, runErr := runner.Run(context.Background(), model.Task{
			ExecutorType: "rsync",
			RsyncSource:  sourcePath,
			RsyncTarget:  targetPath,
			RsyncBinary:  rsyncBinary,
			Node:         node,
		}, func(level, message string) { logs = append(logs, level+": "+message) }, nil)
		if runErr != nil {
			return fmt.Errorf("remote rsync exit code %d: %w (logs=%v)", code, runErr, logs)
		}
		if code != 0 {
			return fmt.Errorf("remote rsync exit code %d (logs=%v)", code, logs)
		}
		return nil
	}
	firstTarget := filepath.Join(targetRoot, "nested", "first")
	if err := run(source+string(os.PathSeparator), firstTarget); err != nil {
		t.Fatalf("remote confined transfer failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(firstTarget, "payload")); err != nil || string(got) != "remote-payload" {
		t.Fatalf("remote payload=%q err=%v", got, err)
	}
	aliasTarget := filepath.Join(targetRoot, "nested", "alias")
	if err := run(internalAlias+string(os.PathSeparator), aliasTarget); err != nil {
		t.Fatalf("remote internal symlink transfer failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(aliasTarget, "payload")); err != nil || string(got) != "remote-payload" {
		t.Fatalf("remote internal alias payload=%q err=%v", got, err)
	}
	if err := run(externalAlias, filepath.Join(targetRoot, "nested", "external")); err == nil {
		t.Fatal("remote external symlink source unexpectedly transferred")
	}
}

func TestCaptureRsyncManifestTargetRoleUsesTargetRoots(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skipf("rsync unavailable: %v", err)
	}
	helper := os.Getenv(rsyncconfinement.HelperPathEnv)
	if helper == "" {
		t.Skipf("%s is required for target-role confinement evidence", rsyncconfinement.HelperPathEnv)
	}
	if info, statErr := os.Stat(helper); statErr != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		t.Skipf("target-role helper unavailable: %v", statErr)
	}
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	source := filepath.Join(targetRoot, "restored")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("target-role-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		sourceRoots string
	}{
		{name: "disjoint-source-and-target-roots", sourceRoots: sourceRoot},
		{name: "target-only-policy", sourceRoots: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(rsyncconfinement.AllowedSourceRootsEnv, tc.sourceRoots)
			t.Setenv(rsyncconfinement.AllowedTargetRootsEnv, targetRoot)
			t.Setenv(rsyncconfinement.HelperPathEnv, helper)
			raw, err := CaptureRsyncManifest(context.Background(), model.Task{
				ExecutorType: "rsync",
				RsyncSource:  source + string(os.PathSeparator),
				RsyncTarget:  filepath.Join(t.TempDir(), "capture"),
				RsyncBinary:  rsyncBinary,
			}, RsyncCaptureTargetRole)
			if err != nil {
				t.Fatalf("target-role capture failed: %v", err)
			}
			manifest, err := model.DecodeRsyncCaptureManifest(raw)
			if err != nil {
				t.Fatalf("decode target-role manifest: %v", err)
			}
			for _, entry := range manifest.Entries {
				if entry.Path == "payload" && entry.Kind == "file" {
					return
				}
			}
			t.Fatalf("target-role manifest omitted payload: %+v", manifest.Entries)
		})
	}
}

func TestCaptureRsyncManifestTargetRoleRemoteUsesTargetRoots(t *testing.T) {
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skipf("sshd unavailable: %v", err)
	}
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skipf("rsync unavailable: %v", err)
	}
	helper := os.Getenv(rsyncconfinement.HelperPathEnv)
	if helper == "" {
		t.Skipf("%s is required for remote target-role evidence", rsyncconfinement.HelperPathEnv)
	}
	if info, statErr := os.Stat(helper); statErr != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		t.Skipf("remote target-role helper unavailable: %v", statErr)
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	t.Setenv(rsyncconfinement.HelperPathEnv, helper)
	t.Setenv(rsyncconfinement.RemoteHelperPathEnv, helper)
	node := testutil.StartRsyncSSHServer(t, sshdBinary)
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	target := filepath.Join(targetRoot, "restored")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "payload"), []byte("remote-target-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideTarget := filepath.Join(outside, "outside")
	if err := os.MkdirAll(outsideTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideTarget, "payload"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalAlias := filepath.Join(targetRoot, "external-alias")
	if err := os.Symlink(outsideTarget, externalAlias); err != nil {
		t.Fatal(err)
	}
	t.Setenv(rsyncconfinement.AllowedSourceRootsEnv, sourceRoot)
	t.Setenv(rsyncconfinement.AllowedTargetRootsEnv, targetRoot)
	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  target + string(os.PathSeparator),
		RsyncTarget:  filepath.Join(t.TempDir(), "capture"),
		RsyncBinary:  rsyncBinary,
		Node:         node,
	}
	raw, err := CaptureRsyncManifest(context.Background(), task, RsyncCaptureTargetRole)
	if err != nil {
		t.Fatalf("remote target-role capture failed: %v", err)
	}
	manifest, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil {
		t.Fatalf("decode remote target-role manifest: %v", err)
	}
	found := false
	for _, entry := range manifest.Entries {
		if entry.Path == "payload" && entry.Kind == "file" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("remote target-role manifest omitted payload: %+v", manifest.Entries)
	}
	task.RsyncSource = externalAlias + string(os.PathSeparator)
	if _, err := CaptureRsyncManifest(context.Background(), task, RsyncCaptureTargetRole); err == nil {
		t.Fatal("remote external target alias unexpectedly accepted")
	}
}

func TestCaptureRsyncManifestTargetRolePreservesDirectoryLayouts(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skipf("rsync unavailable: %v", err)
	}
	helper := os.Getenv(rsyncconfinement.HelperPathEnv)
	if helper == "" {
		t.Skipf("%s is required for target-role confinement evidence", rsyncconfinement.HelperPathEnv)
	}
	if info, statErr := os.Stat(helper); statErr != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		t.Skipf("target-role helper unavailable: %v", statErr)
	}
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	source := filepath.Join(targetRoot, "restored")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("target-role-layout-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(rsyncconfinement.AllowedSourceRootsEnv, sourceRoot)
	t.Setenv(rsyncconfinement.AllowedTargetRootsEnv, targetRoot)
	t.Setenv(rsyncconfinement.HelperPathEnv, helper)
	for _, tc := range []struct {
		name   string
		source string
		layout string
		root   string
	}{
		{
			name:   "directory-root-no-trailing",
			source: source,
			layout: model.TaskRunCaptureLayoutDirectoryRoot,
			root:   filepath.Base(source),
		},
		{
			name:   "directory-contents-trailing",
			source: source + string(filepath.Separator),
			layout: model.TaskRunCaptureLayoutDirectoryContents,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, captureErr := CaptureRsyncManifest(context.Background(), model.Task{
				ExecutorType: "rsync",
				RsyncSource:  tc.source,
				RsyncTarget:  filepath.Join(t.TempDir(), "capture"),
				RsyncBinary:  rsyncBinary,
			}, RsyncCaptureTargetRole)
			if captureErr != nil {
				t.Fatalf("target-role capture failed: %v", captureErr)
			}
			manifest, decodeErr := model.DecodeRsyncCaptureManifest(raw)
			if decodeErr != nil {
				t.Fatalf("decode target-role manifest: %v", decodeErr)
			}
			if manifest.Layout != tc.layout || manifest.Root != tc.root {
				t.Fatalf("target-role layout=%q root=%q, want layout=%q root=%q", manifest.Layout, manifest.Root, tc.layout, tc.root)
			}
			for _, entry := range manifest.Entries {
				if entry.Path == "payload" && entry.Kind == "file" {
					return
				}
			}
			t.Fatalf("target-role manifest omitted payload: %+v", manifest.Entries)
		})
	}
}

func TestCaptureRsyncManifestRemoteRejectsEscapingIntermediateSymlink(t *testing.T) {
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skipf("sshd unavailable: %v", err)
	}
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skipf("rsync unavailable: %v", err)
	}
	helper := os.Getenv(rsyncconfinement.HelperPathEnv)
	if helper == "" {
		t.Skipf("%s is required for remote confinement evidence", rsyncconfinement.HelperPathEnv)
	}
	if info, statErr := os.Stat(helper); statErr != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		t.Skipf("remote helper unavailable: %v", statErr)
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	node := testutil.StartRsyncSSHServer(t, sshdBinary)
	remoteRoot := t.TempDir()
	outsideRoot := t.TempDir()
	targetRoot := t.TempDir()
	outsideSource := filepath.Join(outsideRoot, "source")
	if err := os.MkdirAll(outsideSource, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideSource, "payload"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSource, filepath.Join(remoteRoot, "pivot")); err != nil {
		t.Fatal(err)
	}
	t.Setenv(rsyncconfinement.AllowedSourceRootsEnv, remoteRoot)
	t.Setenv(rsyncconfinement.AllowedTargetRootsEnv, targetRoot)
	t.Setenv(rsyncconfinement.RemoteHelperPathEnv, helper)
	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  filepath.Join(remoteRoot, "pivot", "payload"),
		RsyncTarget:  filepath.Join(targetRoot, "target"),
		RsyncBinary:  rsyncBinary,
		Node:         node,
	}
	if _, err := CaptureRsyncManifest(context.Background(), task, RsyncCaptureSourceRole); err == nil {
		t.Fatal("remote source kind inspection followed intermediate symlink outside configured root")
	}
}
