package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func backupCaptureBinary(t *testing.T) string {
	t.Helper()
	binary, err := exec.LookPath("rsync")
	if err != nil {
		t.Fatal(err)
	}
	return binary
}

func backupCaptureWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRsyncBackupCaptureUsesActualCopySelection(t *testing.T) {
	for _, mutation := range []string{"add", "remove"} {
		t.Run(mutation, func(t *testing.T) {
			source, target := t.TempDir(), t.TempDir()
			backupCaptureWrite(t, filepath.Join(source, "before.txt"), "before")
			change := fmt.Sprintf("printf added > %q", filepath.Join(source, "added.txt"))
			if mutation == "remove" {
				change = fmt.Sprintf("rm -f -- %q", filepath.Join(source, "before.txt"))
			}
			binary := filepath.Join(t.TempDir(), "rsync")
			// Mutate only when the capture copy starts, after layout probing and
			// never during the later transfer from the private tree.
			script := fmt.Sprintf("#!/bin/sh\nfor arg do\n if [ \"$arg\" = '--itemize-changes' ]; then %s; fi\ndone\nexec %q \"$@\"\n", change, backupCaptureBinary(t))
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			task := model.Task{ExecutorType: "rsync", RsyncSource: source + "/", RsyncTarget: target, RsyncBinary: binary}
			capture, err := PrepareRsyncBackupCapture(context.Background(), task, nil)
			if err != nil {
				t.Fatalf("copy selection should be authoritative: %v", err)
			}
			t.Cleanup(func() {
				if err := capture.Close(); err != nil {
					t.Error(err)
				}
			})
			manifest, err := model.DecodeRsyncCaptureManifest(capture.Manifest())
			if err != nil {
				t.Fatal(err)
			}
			want := 3
			if mutation == "remove" {
				want = 1
			}
			if len(manifest.Entries) != want {
				t.Fatalf("entries=%+v", manifest.Entries)
			}
			if code, err := capture.Run(context.Background(), task, func(string, string) {}, nil); code != 0 || err != nil {
				t.Fatalf("run=%d %v", code, err)
			}
			if err := VerifyRsyncCaptureManifestTarget(context.Background(), task, capture.Manifest()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRsyncBackupCaptureStableLayouts(t *testing.T) {
	for _, layout := range []string{"contents", "root", "file", "empty-root", "symlink"} {
		for _, absent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/absent=%t", layout, absent), func(t *testing.T) {
				ctx := context.Background()
				source := filepath.Join(t.TempDir(), "source")
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
				target := t.TempDir()
				if absent {
					target = filepath.Join(target, "new-target")
				}
				if layout != "empty-root" {
					backupCaptureWrite(t, filepath.Join(source, "payload"), "captured")
					backupCaptureWrite(t, filepath.Join(source, "excluded"), "excluded")
					if err := os.Mkdir(filepath.Join(source, "empty"), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("payload", filepath.Join(source, "link")); err != nil {
						t.Fatal(err)
					}
				}
				operand := source
				switch layout {
				case "contents":
					operand += "/"
				case "file":
					operand = filepath.Join(source, "payload")
				case "symlink":
					operand = filepath.Join(source, "link")
				}
				task := model.Task{ExecutorType: "rsync", RsyncSource: operand, RsyncTarget: target, RsyncBinary: backupCaptureBinary(t), Policy: &model.Policy{ExcludeRules: "excluded"}}
				capture, err := PrepareRsyncBackupCapture(ctx, task, nil)
				if err != nil {
					t.Fatal(err)
				}
				directory := capture.directory
				t.Cleanup(func() { _ = capture.Close() })
				// All later source changes, including disappearance, are isolated.
				if err := os.RemoveAll(source); err != nil {
					t.Fatal(err)
				}
				if code, err := capture.Run(ctx, task, func(string, string) {}, nil); code != 0 || err != nil {
					t.Fatalf("run=%d %v", code, err)
				}
				if err := VerifyRsyncCaptureManifestTarget(ctx, task, capture.Manifest()); err != nil {
					t.Fatal(err)
				}
				if err := capture.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(directory); !os.IsNotExist(err) {
					t.Fatalf("capture not cleaned: %v", err)
				}
				if code, err := capture.Run(ctx, task, nil, nil); code == 0 || err == nil {
					t.Fatal("closed capture executed")
				}
			})
		}
	}
}

func TestRsyncBackupCaptureBytePathsAndChecksum(t *testing.T) {
	ctx := context.Background()
	source, target := t.TempDir(), t.TempDir()
	names := []string{"中文", "line\nname", "back\\slash", "bad\xff", "literal\\#012", "payload"}
	for _, name := range names {
		backupCaptureWrite(t, filepath.Join(source, name), "good")
	}
	backupCaptureWrite(t, filepath.Join(source, "excluded"), "not selected")
	backupCaptureWrite(t, filepath.Join(target, "payload"), "evil")
	stamp := time.Unix(1700000000, 0)
	for _, base := range []string{source, target} {
		if err := os.Chtimes(filepath.Join(base, "payload"), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	task := model.Task{ExecutorType: "rsync", RsyncSource: source + "/", RsyncTarget: target, RsyncBinary: backupCaptureBinary(t), Policy: &model.Policy{ExcludeRules: "excluded"}}
	capture, err := PrepareRsyncBackupCapture(ctx, task, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := capture.Close(); err != nil {
			t.Error(err)
		}
	})
	backupCaptureWrite(t, filepath.Join(source, "after-capture"), "new")
	backupCaptureWrite(t, filepath.Join(source, "payload"), "new live version")
	if code, err := capture.Run(ctx, task, func(string, string) {}, nil); code != 0 || err != nil {
		t.Fatalf("run=%d %v", code, err)
	}
	if err := VerifyRsyncCaptureManifestTarget(ctx, task, capture.Manifest()); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		got, err := os.ReadFile(filepath.Join(target, name))
		if err != nil || string(got) != "good" {
			t.Fatalf("%q=%q err=%v", name, got, err)
		}
	}
	for _, name := range []string{"excluded", "after-capture"} {
		if _, err := os.Stat(filepath.Join(target, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected %s: %v", name, err)
		}
	}
}

// A later run reuses the previous backup as rsync basis. Entries satisfied
// from that basis must still be part of the capture evidence, and a changed
// file must carry the new source bytes.
func TestRsyncBackupCaptureWithPreviousBackupKeepsFullSelection(t *testing.T) {
	ctx := context.Background()
	source, target := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	backupCaptureWrite(t, filepath.Join(source, "sub", "unchanged"), "same")
	backupCaptureWrite(t, filepath.Join(source, "changed"), strings.Repeat("a", 64<<10))
	if err := os.Symlink("changed", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	task := model.Task{ExecutorType: "rsync", RsyncSource: source + "/", RsyncTarget: target, RsyncBinary: backupCaptureBinary(t)}
	runCapture := func() model.RsyncCaptureManifest {
		t.Helper()
		capture, err := PrepareRsyncBackupCapture(ctx, task, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := capture.Close(); err != nil {
				t.Error(err)
			}
		}()
		if code, err := capture.Run(ctx, task, func(string, string) {}, nil); code != 0 || err != nil {
			t.Fatalf("run=%d %v", code, err)
		}
		if err := VerifyRsyncCaptureManifestTarget(ctx, task, capture.Manifest()); err != nil {
			t.Fatal(err)
		}
		manifest, err := model.DecodeRsyncCaptureManifest(capture.Manifest())
		if err != nil {
			t.Fatal(err)
		}
		return manifest
	}
	first := runCapture()
	backupCaptureWrite(t, filepath.Join(source, "changed"), strings.Repeat("a", 32<<10)+"b"+strings.Repeat("a", 32<<10-1))
	second := runCapture()
	if len(second.Entries) != len(first.Entries) || len(first.Entries) != 5 {
		t.Fatalf("selection lost with basis: first=%+v second=%+v", first.Entries, second.Entries)
	}
	for index, entry := range second.Entries {
		if entry.Path != first.Entries[index].Path || entry.Kind != first.Entries[index].Kind {
			t.Fatalf("entry %d differs: first=%+v second=%+v", index, first.Entries[index], entry)
		}
		if entry.Path == "changed" && entry.SHA256 == first.Entries[index].SHA256 {
			t.Fatalf("changed file kept stale basis bytes")
		}
	}
	got, err := os.ReadFile(filepath.Join(target, "changed"))
	if err != nil || !strings.Contains(string(got), "b") {
		t.Fatalf("target not updated: err=%v", err)
	}
}

func TestRsyncBackupCaptureRejectsIncompleteOrUnreportedCopy(t *testing.T) {
	for _, mode := range []string{"exit24", "missing-record", "duplicate", "wrong-name", "symlink-descendant", "missing-directory"} {
		t.Run(mode, func(t *testing.T) {
			root, source, target := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("TMPDIR", root)
			backupCaptureWrite(t, filepath.Join(source, "payload"), "good")
			binary := filepath.Join(t.TempDir(), "rsync")
			script := fmt.Sprintf("#!/bin/sh\n%q \"$@\" >/dev/null || exit $?\n", backupCaptureBinary(t))
			switch mode {
			case "symlink-descendant":
				outside := t.TempDir()
				backupCaptureWrite(t, filepath.Join(outside, "payload"), "outside must not be read")
				if err := os.Symlink(outside, filepath.Join(source, "escape")); err != nil {
					t.Fatal(err)
				}
				script = fmt.Sprintf("#!/bin/sh\n%q \"$@\" || exit $?\nprintf '>f+++++++++ escape/payload\\n'\n", backupCaptureBinary(t))
			case "missing-directory":
				if err := os.Mkdir(filepath.Join(source, "nested"), 0700); err != nil {
					t.Fatal(err)
				}
				backupCaptureWrite(t, filepath.Join(source, "nested", "payload"), "good")
				script += "printf '>f+++++++++ payload\\n>f+++++++++ nested/payload\\n'\n"
			case "exit24":
				script += "printf 'file has vanished: payload\\n' >&2\nexit 24\n"
			case "duplicate":
				script += "printf '>f+++++++++ payload\\n>f+++++++++ payload\\n'\n"
			case "wrong-name":
				script += "printf '>f+++++++++ elsewhere\\n'\n"
			}
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			task := model.Task{ExecutorType: "rsync", RsyncSource: source + "/", RsyncTarget: target, RsyncBinary: binary}
			capture, err := PrepareRsyncBackupCapture(context.Background(), task, nil)
			if err == nil || capture != nil {
				t.Fatalf("invalid capture accepted: %v", err)
			}
			if mode == "exit24" && (!strings.Contains(err.Error(), "exit status 24") || !strings.Contains(err.Error(), "vanished")) {
				t.Fatal(err)
			}
			if mode == "symlink-descendant" && !strings.Contains(err.Error(), "selection is absent from tree") {
				t.Fatalf("must reject before reading payload: %v", err)
			}
			if mode == "missing-directory" && !strings.Contains(err.Error(), "unreported entry") {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(root, "xirang-rsync-backup-capture-*"))
			if err != nil || len(files) != 0 {
				t.Fatalf("leaked captures=%v err=%v", files, err)
			}
		})
	}
}

func TestRsyncBackupCapturePrivateOwnerAndReadonlyCleanup(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	if err := os.Chmod(source, 0755); err != nil {
		t.Fatal(err)
	}
	readonly := filepath.Join(source, "readonly")
	if err := os.Mkdir(readonly, 0700); err != nil {
		t.Fatal(err)
	}
	backupCaptureWrite(t, filepath.Join(readonly, "payload"), "good")
	if err := os.Chmod(readonly, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readonly, 0700) })
	if err := os.Symlink(t.TempDir(), filepath.Join(source, "external-link")); err != nil {
		t.Fatal(err)
	}
	task := model.Task{ExecutorType: "rsync", RsyncSource: source + "/", RsyncTarget: target, RsyncBinary: backupCaptureBinary(t)}
	capture, err := PrepareRsyncBackupCapture(context.Background(), task, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	directory := capture.directory
	info, err := os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("owner directory permissions: %v %v", info, err)
	}
	info, err = os.Stat(filepath.Join(directory, "tree", "readonly"))
	if err != nil || info.Mode().Perm() != 0555 {
		t.Fatalf("copied permissions: %v %v", info, err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("readonly capture leaked: %v", err)
	}
}
