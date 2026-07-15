package provider

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
)

func TestBuildRsyncTreeCommandUsesStrictFullCopyProfile(t *testing.T) {
	command, err := BuildRsyncTreeCommand(RsyncTreeCommandInput{
		Mode:           backupasset.PublicationVersionedFullCopy,
		Source:         RsyncTreeCommandSource{LocalPath: "/private/source"},
		StagingTree:    "/private/managed/staging/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/tree",
		CaptureACLs:    true,
		CaptureXattrs:  true,
		BandwidthKibps: 768,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--archive", "--checksum", "--hard-links", "--numeric-ids", "--fsync", "--protect-args", "--info=progress2", "--no-devices", "--no-specials",
		"--acls", "--xattrs", "--bwlimit=768k", "--", "/private/source/", "/private/managed/staging/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/tree/",
	}
	if command.Binary != "rsync" || !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("strict full-copy argv=%q binary=%q, want=%q", command.Args, command.Binary, want)
	}
}

func TestBuildRsyncTreeCommandUsesStrictHardlinkRemoteProfile(t *testing.T) {
	command, err := BuildRsyncTreeCommand(RsyncTreeCommandInput{
		Mode: backupasset.PublicationVersionedHardlink,
		Source: RsyncTreeCommandSource{Remote: &RsyncTreeRemoteSource{
			User: "backup",
			Host: "node.example",
			Path: "/data/app",
			Transport: RsyncTreeSSHTransport{
				Port: 2222, HostKeyMode: RsyncTreeHostKeyStrict,
				KnownHostsFile: "/private/known_hosts", IdentityFile: "/private/key",
			},
			UseSudoRsync: true,
		}},
		StagingTree: "/private/managed/staging/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/tree",
		ParentTree:  "/private/managed/points/cccccccccccccccccccccccccccccccc/tree",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--archive", "--checksum", "--hard-links", "--numeric-ids", "--fsync", "--protect-args", "--info=progress2", "--no-devices", "--no-specials",
		"-e", "ssh -p 2222 -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/private/known_hosts -i /private/key",
		"--rsync-path", "sudo rsync", "--link-dest=/private/managed/points/cccccccccccccccccccccccccccccccc/tree", "--",
		"backup@node.example:/data/app/", "/private/managed/staging/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/tree/",
	}
	if command.Binary != "rsync" || !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("strict hardlink argv=%q binary=%q, want=%q", command.Args, command.Binary, want)
	}
}

func TestRsyncTreeCommandScrubsTransferAffectingEnvironment(t *testing.T) {
	got := SanitizeRsyncTreeEnvironment([]string{
		"PATH=/usr/bin",
		"LANG=C",
		"RSYNC_OLD_ARGS=1",
		"RSYNC_PROTECT_ARGS=0",
		"RSYNC_RSH=evil-shell",
		"RSYNC_PROXY=proxy.example",
		"TMPDIR=/unsafe/tmp",
		"KEEP=value",
	})
	want := []string{"PATH=/usr/bin", "LANG=C", "KEEP=value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitized environment=%q, want=%q", got, want)
	}
}

func TestBuildRsyncTreeCommandKeepsRootSourceAsSingleSlash(t *testing.T) {
	command, err := BuildRsyncTreeCommand(RsyncTreeCommandInput{
		Mode:        backupasset.PublicationVersionedFullCopy,
		Source:      RsyncTreeCommandSource{LocalPath: "/"},
		StagingTree: "/private/managed/staging/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/tree",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := command.Args[len(command.Args)-2]; got != "/" {
		t.Fatalf("root source operand=%q, want /", got)
	}
}

func TestBuildRsyncTreeCommandRejectsUnsafeOrAmbiguousInput(t *testing.T) {
	base := RsyncTreeCommandInput{
		Mode:        backupasset.PublicationVersionedFullCopy,
		Source:      RsyncTreeCommandSource{LocalPath: "/private/source"},
		StagingTree: "/private/managed/staging/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/tree",
	}
	tests := []struct {
		name string
		edit func(*RsyncTreeCommandInput)
	}{
		{"legacy mode", func(input *RsyncTreeCommandInput) { input.Mode = backupasset.PublicationLegacyMutable }},
		{"full copy parent", func(input *RsyncTreeCommandInput) { input.ParentTree = "/private/parent" }},
		{"relative source", func(input *RsyncTreeCommandInput) { input.Source.LocalPath = "relative" }},
		{"both source kinds", func(input *RsyncTreeCommandInput) {
			input.Source.Remote = &RsyncTreeRemoteSource{User: "backup", Host: "node.example", Path: "/data", Transport: RsyncTreeSSHTransport{HostKeyMode: RsyncTreeHostKeyStrict, KnownHostsFile: "/private/known_hosts"}}
		}},
		{"remote shell host", func(input *RsyncTreeCommandInput) {
			input.Source = RsyncTreeCommandSource{Remote: &RsyncTreeRemoteSource{User: "backup", Host: "node;touch", Path: "/data", Transport: RsyncTreeSSHTransport{HostKeyMode: RsyncTreeHostKeyStrict, KnownHostsFile: "/private/known_hosts"}}}
		}},
		{"host key disabled", func(input *RsyncTreeCommandInput) {
			input.Source = RsyncTreeCommandSource{Remote: &RsyncTreeRemoteSource{User: "backup", Host: "node.example", Path: "/data", Transport: RsyncTreeSSHTransport{HostKeyMode: "no", KnownHostsFile: "/private/known_hosts"}}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			if _, err := BuildRsyncTreeCommand(input); !errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("unsafe input error=%v, want invalid state", err)
			}
		})
	}
}

func TestBuildRsyncTreeCommandExcludesForbiddenFlags(t *testing.T) {
	command, err := BuildRsyncTreeCommand(RsyncTreeCommandInput{
		Mode:        backupasset.PublicationVersionedHardlink,
		Source:      RsyncTreeCommandSource{LocalPath: "/private/source"},
		StagingTree: "/private/managed/staging/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb/tree",
		ParentTree:  "/private/managed/points/cccccccccccccccccccccccccccccccc/tree",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(command.Args, "\x00")
	for _, forbidden := range []string{
		"--inplace", "--append", "--partial", "--ignore-existing", "--ignore-missing-args", "--temp-dir", "--delete", "--remove-source-files",
		"--backup", "--backup-dir", "--compare-dest", "--copy-dest", "--copy-links", "--copy-dirlinks", "--ignore-errors", "--checksum-choice=none",
		"--dry-run", "--list-only", "--out-format", "--log-file", "--files-from", "--include-from", "--exclude-from",
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("forbidden Rsync flag %q entered strict argv=%q", forbidden, command.Args)
		}
	}
	if strings.Count(joined, "--link-dest=") != 1 {
		t.Fatalf("hardlink profile missing exactly one internally derived link-dest: %q", command.Args)
	}
}

func TestLocalRsyncTreeProcessRunnerScrubsInheritedEnvironment(t *testing.T) {
	script := filepath.Join(t.TempDir(), "check-env")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n[ -z \"$RSYNC_RSH\" ] && [ -z \"$TMPDIR\" ] && [ \"$KEEP\" = value ]\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner, err := newLocalRsyncTreeProcessRunner(func() []string {
		return []string{"PATH=/usr/bin:/bin", "RSYNC_RSH=unsafe", "TMPDIR=/unsafe", "KEEP=value"}
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), RsyncTreeCommand{Binary: script}, 1<<10)
	if err != nil || !result.ExitCodeKnown || result.ExitCode != 0 {
		t.Fatalf("sanitized process result=%+v err=%v", result, err)
	}
}

func TestLocalRsyncTreeProcessRunnerDistinguishesOutputLimitAndKnownNonzero(t *testing.T) {
	runner, err := newLocalRsyncTreeProcessRunner(func() []string { return []string{"PATH=/usr/bin:/bin"} })
	if err != nil {
		t.Fatal(err)
	}
	t.Run("output limit", func(t *testing.T) {
		script := filepath.Join(t.TempDir(), "emit")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 12345\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), RsyncTreeCommand{Binary: script}, 4)
		if !errors.Is(err, backupasset.ErrCapabilityUnavailable) || result.ExitCodeKnown || result.ExitCode != UnknownProviderExitCode {
			t.Fatalf("output limit result=%+v err=%v", result, err)
		}
	})
	t.Run("partial exit", func(t *testing.T) {
		script := filepath.Join(t.TempDir(), "partial")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 23\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(t.Context(), RsyncTreeCommand{Binary: script}, 1<<10)
		if err != nil || !result.ExitCodeKnown || result.ExitCode != 23 {
			t.Fatalf("partial exit result=%+v err=%v", result, err)
		}
	})
}
