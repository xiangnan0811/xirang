package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xirang/backend/internal/model"
	"xirang/backend/internal/task/testutil"
)

func TestDecodeRsyncDisplayPathEscapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "newline", raw: `line\#012name`, want: "line\nname"},
		{name: "backslash", raw: `literal\#134#012`, want: `literal\#012`},
		{name: "utf8", raw: "中文.txt", want: "中文.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeRsyncDisplayPath(tc.raw)
			if err != nil {
				t.Fatalf("decode path: %v", err)
			}
			if got != tc.want {
				t.Fatalf("decoded path=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestCaptureRsyncManifestPreservesEscapedNamesAndLinks(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	source := t.TempDir()
	target := t.TempDir()
	files := map[string]string{
		"中文.txt":        "utf8 payload",
		"line\nname":    "newline payload",
		`literal\#012`:  "literal escape payload",
		`back\slash`:    "backslash payload",
		"arrow -> name": "arrow payload",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(source, name), []byte(contents), 0o640); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
	}
	linkName := `link -> name\#012`
	linkTarget := "target -> part\nwith newline\n"
	if err := os.Symlink(linkTarget, filepath.Join(source, linkName)); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	multipleLinkName := "link-multiple-newlines"
	multipleLinkTarget := "target -> multiple\n\n"
	if err := os.Symlink(multipleLinkTarget, filepath.Join(source, multipleLinkName)); err != nil {
		t.Fatalf("create multiple-newline symlink: %v", err)
	}
	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  source + string(filepath.Separator),
		RsyncTarget:  target,
		RsyncBinary:  rsyncBinary,
	}
	raw, err := CaptureRsyncManifest(context.Background(), task)
	if err != nil {
		t.Fatalf("capture manifest: %v", err)
	}
	manifest, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	seen := make(map[string]model.RsyncCaptureManifestEntry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		seen[entry.Path] = entry
	}
	if len(seen) != len(files)+3 {
		t.Fatalf("manifest entries=%d, want %d: %+v", len(seen), len(files)+3, manifest.Entries)
	}
	for name, contents := range files {
		entry, ok := seen[name]
		if !ok || entry.Kind != "file" {
			t.Fatalf("manifest missing file %q: %+v", name, entry)
		}
		sum := sha256.Sum256([]byte(contents))
		if entry.SHA256 != hex.EncodeToString(sum[:]) || entry.Size != int64(len(contents)) {
			t.Fatalf("manifest evidence for %q=%+v", name, entry)
		}
	}
	entry, ok := seen[linkName]
	if !ok || entry.Kind != "symlink" || entry.LinkTarget != linkTarget {
		t.Fatalf("manifest symlink=%+v, want target %q", entry, linkTarget)
	}
	entry, ok = seen[multipleLinkName]
	if !ok || entry.Kind != "symlink" || entry.LinkTarget != multipleLinkTarget {
		t.Fatalf("manifest multiple-newline symlink=%+v, want target %q", entry, multipleLinkTarget)
	}
}

func TestCaptureRsyncManifestRemotePreservesEscapedNamesAndLinks(t *testing.T) {
	rsyncBinary, err := exec.LookPath("rsync")
	if err != nil {
		t.Skip("rsync is not installed")
	}
	sshdBinary, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd is not installed")
	}
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "false")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "false")
	node := testutil.StartRsyncSSHServer(t, sshdBinary)
	source := t.TempDir()
	target := t.TempDir()
	name := `remote\#012 -> name`
	contents := []byte("remote payload")
	if err := os.WriteFile(filepath.Join(source, name), contents, 0o640); err != nil {
		t.Fatalf("write remote edge file: %v", err)
	}
	linkName := "remote-link -> name"
	linkTarget := "remote-target\\#012 -> part\n"
	if err := os.Symlink(linkTarget, filepath.Join(source, linkName)); err != nil {
		t.Fatalf("create remote symlink: %v", err)
	}
	multipleLinkName := "remote-link-multiple-newlines"
	multipleLinkTarget := "remote-target-multiple\n\n"
	if err := os.Symlink(multipleLinkTarget, filepath.Join(source, multipleLinkName)); err != nil {
		t.Fatalf("create remote multiple-newline symlink: %v", err)
	}
	task := model.Task{
		ExecutorType: "rsync",
		RsyncSource:  source + string(filepath.Separator),
		RsyncTarget:  target,
		RsyncBinary:  rsyncBinary,
		Node:         node,
	}
	raw, err := CaptureRsyncManifest(context.Background(), task)
	if err != nil {
		t.Fatalf("capture remote manifest: %v", err)
	}
	manifest, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil {
		t.Fatalf("decode remote manifest: %v", err)
	}
	var fileEntry, linkEntry, multipleLinkEntry model.RsyncCaptureManifestEntry
	for _, entry := range manifest.Entries {
		switch entry.Path {
		case name:
			fileEntry = entry
		case linkName:
			linkEntry = entry
		case multipleLinkName:
			multipleLinkEntry = entry
		}
	}
	sum := sha256.Sum256(contents)
	if fileEntry.Kind != "file" || fileEntry.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("remote file evidence=%+v", fileEntry)
	}
	if linkEntry.Kind != "symlink" || linkEntry.LinkTarget != linkTarget {
		t.Fatalf("remote symlink evidence=%+v, want target %q", linkEntry, linkTarget)
	}
	if multipleLinkEntry.Kind != "symlink" || multipleLinkEntry.LinkTarget != multipleLinkTarget {
		t.Fatalf("remote multiple-newline symlink evidence=%+v, want target %q", multipleLinkEntry, multipleLinkTarget)
	}
}
