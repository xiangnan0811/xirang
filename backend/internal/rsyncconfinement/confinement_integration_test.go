//go:build linux

package rsyncconfinement

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireLandlockRsync(t *testing.T) string {
	t.Helper()
	if _, err := landlockABI(); err != nil {
		t.Skipf("Landlock unavailable: %v", err)
	}
	rsync, err := exec.LookPath("rsync")
	if err != nil {
		t.Skipf("rsync unavailable: %v", err)
	}
	helper := os.Getenv(HelperPathEnv)
	if helper == "" {
		helper = DefaultHelperPath
	}
	if !filepath.IsAbs(helper) {
		if _, err := exec.LookPath(helper); err != nil {
			t.Skipf("confinement helper unavailable: %v", err)
		}
	}
	return rsync
}

func runConfinedRsync(t *testing.T, binary, source, target string) error {
	t.Helper()
	cmd, cleanup, err := NewCommand(context.Background(), CommandRequest{
		Binary:      binary,
		Args:        []string{"-a", "--", source + string(filepath.Separator), target + string(filepath.Separator)},
		LocalSource: source,
		LocalTarget: target,
	})
	if err != nil {
		return err
	}
	defer cleanup()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err != nil && stderr.Len() > 0 {
		return errors.Join(err, errors.New(strings.TrimSpace(stderr.String())))
	}
	return err
}

func TestConfinementLocalBoundaryAndDestinationCreation(t *testing.T) {
	rsync := requireLandlockRsync(t)
	helper := os.Getenv(HelperPathEnv)
	if helper == "" {
		helper = DefaultHelperPath
	}
	if !filepath.IsAbs(helper) {
		var err error
		helper, err = exec.LookPath(helper)
		if err != nil {
			t.Skipf("confinement helper unavailable: %v", err)
		}
	}
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	source := filepath.Join(sourceRoot, "source")
	target := filepath.Join(targetRoot, "nested", "target")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(source, "inside")
	if err := os.WriteFile(inside, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside", filepath.Join(source, "internal-link")); err != nil {
		t.Fatal(err)
	}
	sourceAlias := filepath.Join(sourceRoot, "source-alias")
	if err := os.Symlink(source, sourceAlias); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AllowedSourceRootsEnv, sourceRoot)
	t.Setenv(AllowedTargetRootsEnv, targetRoot)
	t.Setenv(HelperPathEnv, helper)
	if err := runConfinedRsync(t, rsync, source, target); err != nil {
		t.Fatalf("confined local transfer failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(target, "payload"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Fatalf("payload=%q", got)
	}
	aliasTarget := filepath.Join(targetRoot, "alias-target")
	if err := runConfinedRsync(t, rsync, sourceAlias, aliasTarget); err != nil {
		t.Fatalf("confined internal symlink transfer failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(aliasTarget, "payload")); err != nil || string(got) != "payload" {
		t.Fatalf("internal symlink payload=%q err=%v", got, err)
	}
}

func TestConfinementRejectsSiblingParentAndExternalLink(t *testing.T) {
	rsync := requireLandlockRsync(t)
	helper := os.Getenv(HelperPathEnv)
	if helper == "" {
		helper = DefaultHelperPath
	}
	if !filepath.IsAbs(helper) {
		var err error
		helper, err = exec.LookPath(helper)
		if err != nil {
			t.Skipf("confinement helper unavailable: %v", err)
		}
	}
	root := t.TempDir()
	outside := t.TempDir()
	source := filepath.Join(root, "source")
	sibling := root + "-sibling"
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "outside")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "external-link")
	if err := os.Symlink(outsideFile, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AllowedSourceRootsEnv, root)
	t.Setenv(AllowedTargetRootsEnv, root)
	t.Setenv(HelperPathEnv, helper)
	if err := runConfinedRsync(t, rsync, sibling, target); err == nil {
		t.Fatal("sibling-prefix source unexpectedly accepted")
	}
	if err := runConfinedRsync(t, rsync, filepath.Join(root, "..", filepath.Base(outside)), target); err == nil {
		t.Fatal("parent-escaping source unexpectedly accepted")
	}
	if err := runConfinedRsync(t, rsync, link, target); err == nil {
		t.Fatal("external symlink source unexpectedly accepted")
	}
}

func TestConfinementPinsReplacementAfterPin(t *testing.T) {
	rsync := requireLandlockRsync(t)
	helper := os.Getenv(HelperPathEnv)
	if helper == "" {
		helper = DefaultHelperPath
	}
	if !filepath.IsAbs(helper) {
		var err error
		helper, err = exec.LookPath(helper)
		if err != nil {
			t.Skipf("confinement helper unavailable: %v", err)
		}
	}
	root := t.TempDir()
	outside := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "payload"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AllowedSourceRootsEnv, root)
	t.Setenv(AllowedTargetRootsEnv, root)
	t.Setenv(HelperPathEnv, helper)
	cmd, cleanup, err := NewCommand(context.Background(), CommandRequest{
		Binary:      rsync,
		Args:        []string{"-a", "--", source + string(filepath.Separator), target + string(filepath.Separator)},
		LocalSource: source,
		LocalTarget: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	moved := source + ".moved"
	if err := os.Rename(source, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, source); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("pinned source transfer failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "payload")); err != nil || string(got) != "payload" {
		t.Fatalf("replacement source payload=%q err=%v", got, err)
	}
}

func TestConfinementPinsNoTrailingDirectoryReplacement(t *testing.T) {
	rsync := requireLandlockRsync(t)
	helper := os.Getenv(HelperPathEnv)
	if helper == "" {
		helper = DefaultHelperPath
	}
	if !filepath.IsAbs(helper) {
		var err error
		helper, err = exec.LookPath(helper)
		if err != nil {
			t.Skipf("confinement helper unavailable: %v", err)
		}
	}
	root := t.TempDir()
	outside := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	outsideFile := filepath.Join(outside, "runtime.txt")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AllowedSourceRootsEnv, root)
	t.Setenv(AllowedTargetRootsEnv, root)
	t.Setenv(HelperPathEnv, helper)
	t.Setenv(helperNamespaceReadyEnv, "1")
	t.Setenv(helperNamespaceUserFD, "999999")
	t.Setenv(helperNamespaceMountFD, "999998")
	cmd, cleanup, err := NewCommand(context.Background(), CommandRequest{
		Binary:           rsync,
		Args:             []string{"-a", "--", source, target + string(filepath.Separator)},
		LocalSource:      source,
		LocalTarget:      target,
		RuntimeReadPaths: []string{outsideFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := os.Rename(source, source+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, source); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("pinned no-trailing directory transfer failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "source", "payload")); err != nil || string(got) != "original" {
		t.Fatalf("replacement directory payload=%q err=%v", got, err)
	}
}

func TestConfinementPinsNoTrailingRegularFileReplacement(t *testing.T) {
	rsync := requireLandlockRsync(t)
	helper := os.Getenv(HelperPathEnv)
	if helper == "" {
		helper = DefaultHelperPath
	}
	if !filepath.IsAbs(helper) {
		var err error
		helper, err = exec.LookPath(helper)
		if err != nil {
			t.Skipf("confinement helper unavailable: %v", err)
		}
	}
	root := t.TempDir()
	outside := t.TempDir()
	source := filepath.Join(root, "source.txt")
	target := filepath.Join(root, "target")
	outsideFile := filepath.Join(outside, "runtime.txt")
	if err := os.WriteFile(source, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AllowedSourceRootsEnv, root)
	t.Setenv(AllowedTargetRootsEnv, root)
	t.Setenv(HelperPathEnv, helper)
	cmd, cleanup, err := NewCommand(context.Background(), CommandRequest{
		Binary:           rsync,
		Args:             []string{"-a", "--", source, target + string(filepath.Separator)},
		LocalSource:      source,
		LocalTarget:      target,
		RuntimeReadPaths: []string{outsideFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := os.Rename(source, source+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, source); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("pinned no-trailing file transfer failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(target, "source.txt")); err != nil || string(got) != "original" {
		t.Fatalf("replacement file payload=%q err=%v", got, err)
	}
}

func TestConfinementRejectsRuntimeReadDirectory(t *testing.T) {
	rsync := requireLandlockRsync(t)
	helper := os.Getenv(HelperPathEnv)
	if helper == "" {
		helper = DefaultHelperPath
	}
	if !filepath.IsAbs(helper) {
		var err error
		helper, err = exec.LookPath(helper)
		if err != nil {
			t.Skipf("confinement helper unavailable: %v", err)
		}
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	runtimeDir := t.TempDir()
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AllowedSourceRootsEnv, root)
	t.Setenv(AllowedTargetRootsEnv, root)
	t.Setenv(HelperPathEnv, helper)
	_, cleanup, err := NewCommand(context.Background(), CommandRequest{
		Binary:           rsync,
		Args:             []string{"-a", "--", source + string(filepath.Separator), target + string(filepath.Separator)},
		LocalSource:      source,
		LocalTarget:      target,
		RuntimeReadPaths: []string{runtimeDir},
	})
	cleanup()
	if !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("runtime directory error=%v, want ErrCapabilityUnavailable", err)
	}
}

func TestConfinementRejectsRsyncBoundaryEscapeOptions(t *testing.T) {
	rsync := requireLandlockRsync(t)
	helper := os.Getenv(HelperPathEnv)
	if helper == "" {
		helper = DefaultHelperPath
	}
	if !filepath.IsAbs(helper) {
		var err error
		helper, err = exec.LookPath(helper)
		if err != nil {
			t.Skipf("confinement helper unavailable: %v", err)
		}
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AllowedSourceRootsEnv, root)
	t.Setenv(AllowedTargetRootsEnv, root)
	t.Setenv(HelperPathEnv, helper)
	for _, option := range []string{"-aL", "-ak", "-af", "-aF", "--insecure-links"} {
		t.Run(option, func(t *testing.T) {
			_, cleanup, err := NewCommand(context.Background(), CommandRequest{
				Binary:      rsync,
				Args:        []string{option, "--", source + string(filepath.Separator), target + string(filepath.Separator)},
				LocalSource: source,
				LocalTarget: target,
			})
			cleanup()
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Rsync option %q error=%v, want ErrInvalidRequest", option, err)
			}
		})
	}
	_, cleanup, err := NewCommand(context.Background(), CommandRequest{
		Binary:      rsync,
		Args:        []string{"-a", "--exclude", "-L", "--", source + string(filepath.Separator), target + string(filepath.Separator)},
		LocalSource: source,
		LocalTarget: target,
	})
	cleanup()
	if err != nil {
		t.Fatalf("exclude value was parsed as an option: %v", err)
	}
	for _, option := range []string{"--remote-option=--copy-links", "-M--copy-links"} {
		_, cleanup, err := NewCommand(context.Background(), CommandRequest{
			Binary:      rsync,
			Args:        []string{option, "--", source + string(filepath.Separator), target + string(filepath.Separator)},
			LocalSource: source,
			LocalTarget: target,
		})
		cleanup()
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("unknown Rsync option %q error=%v, want ErrInvalidRequest", option, err)
		}
	}
}

func TestConfinementRequiresInstalledHelperWhenRestricted(t *testing.T) {
	if _, err := landlockABI(); err != nil {
		t.Skipf("Landlock unavailable: %v", err)
	}
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skipf("rsync unavailable: %v", err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AllowedSourceRootsEnv, root)
	t.Setenv(AllowedTargetRootsEnv, root)
	t.Setenv(HelperPathEnv, filepath.Join(root, "missing-helper"))
	_, cleanup, err := NewCommand(context.Background(), CommandRequest{Binary: "rsync", Args: []string{"-a", "--", source, filepath.Join(root, "target")}, LocalSource: source})
	cleanup()
	if err == nil || !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("missing helper error=%v", err)
	}
}

func TestConfinementRejectsPublicCleanupCommand(t *testing.T) {
	helper := os.Getenv(HelperPathEnv)
	if helper == "" {
		helper = DefaultHelperPath
	}
	if !filepath.IsAbs(helper) {
		resolved, err := exec.LookPath(helper)
		if err != nil {
			t.Skipf("confinement helper unavailable: %v", err)
		}
		helper = resolved
	}
	root := t.TempDir()
	sentinel := filepath.Join(root, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(name, "XIRANG_RSYNC_") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"XIRANG_RSYNC_NAMESPACE_READY=1",
		"XIRANG_RSYNC_NAMESPACE_USER_FD=3",
		"XIRANG_RSYNC_NAMESPACE_MOUNT_FD=4",
		"XIRANG_RSYNC_CLEANUP_TOKEN=forged",
	)
	for name, args := range map[string][]string{
		"without channel":      {helperCleanupCommand},
		"with forged pathname": {helperCleanupCommand, root},
	} {
		t.Run(name, func(t *testing.T) {
			command := exec.Command(helper, args...)
			command.Env = env
			if err := command.Run(); err == nil {
				t.Fatal("public cleanup command unexpectedly succeeded")
			}
			if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep" {
				t.Fatalf("public cleanup command changed sentinel=%q err=%v", got, err)
			}
		})
	}
}

func TestConfinementCleanupSurvivesForcedHelperKill(t *testing.T) {
	rsync := requireLandlockRsync(t)
	helper := os.Getenv(HelperPathEnv)
	if helper == "" {
		helper = DefaultHelperPath
	}
	if !filepath.IsAbs(helper) {
		if _, err := exec.LookPath(helper); err != nil {
			t.Skipf("confinement helper unavailable: %v", err)
		}
	}
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	source := filepath.Join(sourceRoot, "source")
	target := filepath.Join(targetRoot, "target")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), bytes.Repeat([]byte("x"), 8<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AllowedSourceRootsEnv, sourceRoot)
	t.Setenv(AllowedTargetRootsEnv, targetRoot)
	before := make(map[string]struct{})
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".xirang-rsync-confined-") {
			before[entry.Name()] = struct{}{}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, cleanup, err := NewCommand(ctx, CommandRequest{
		Binary:      rsync,
		Args:        []string{"-a", "--bwlimit=1", "--", source, target + string(filepath.Separator)},
		LocalSource: source,
		LocalTarget: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	findNewAlias := func() string {
		entries, readErr := os.ReadDir(os.TempDir())
		if readErr != nil {
			return ""
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".xirang-rsync-confined-") {
				if _, existed := before[entry.Name()]; !existed {
					return filepath.Join(os.TempDir(), entry.Name())
				}
			}
		}
		return ""
	}
	var aliasRoot string
	setupDeadline := time.NewTimer(10 * time.Second)
	defer setupDeadline.Stop()
	for aliasRoot == "" {
		select {
		case err := <-done:
			t.Fatalf("confined helper exited before alias setup: %v", err)
		case <-setupDeadline.C:
			t.Fatal("confined helper did not create an alias root")
		default:
			aliasRoot = findNewAlias()
			if aliasRoot == "" {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("forced helper cancellation did not join")
	}
	cleanupDeadline := time.NewTimer(5 * time.Second)
	defer cleanupDeadline.Stop()
	for {
		if _, statErr := os.Lstat(aliasRoot); os.IsNotExist(statErr) {
			return
		}
		select {
		case <-cleanupDeadline.C:
			t.Fatalf("alias root survived forced helper cancellation: %s", aliasRoot)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
