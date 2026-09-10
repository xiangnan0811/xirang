package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"
)

const (
	maxRsyncCaptureEntries      = 100000
	maxRsyncCaptureManifestLen  = 8 << 20
	maxRsyncCapturePathLen      = 4096
	maxRsyncCaptureCommandBytes = 64 << 10
)

var rsyncListEntryPattern = regexp.MustCompile(`^([bcdlps-][rwxStTs-]{9})\s+([0-9]+)\s+[0-9]{4}/[0-9]{2}/[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2} (.*)$`)

type rsyncCaptureListEntry struct {
	kind string
	path string
}

type rsyncCaptureOutputBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *rsyncCaptureOutputBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		return 0, io.ErrShortWrite
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		return remaining, io.ErrShortWrite
	}
	return b.buf.Write(p)
}

func rsyncCommandBinary(task model.Task) string {
	if binary := strings.TrimSpace(task.RsyncBinary); binary != "" {
		return binary
	}
	return "rsync"
}

// CaptureRsyncManifest enumerates the exact Rsync selection and hashes the
// source bytes before a mutable compatibility transfer starts. The listing is
// produced by Rsync itself, so excludes are not approximated by a Go glob.
func CaptureRsyncManifest(ctx context.Context, task model.Task) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	source := strings.TrimSpace(task.RsyncSource)
	if source == "" {
		return "", fmt.Errorf("rsync capture source is empty")
	}
	rules, err := parseRsyncExcludeRules(task.Policy)
	if err != nil {
		return "", err
	}

	layout := strings.TrimSpace(task.RsyncCaptureLayout)
	root := strings.TrimSpace(task.RsyncCaptureRoot)
	if layout == "" {
		layout, root, err = inferRsyncCaptureLayout(ctx, task, source)
		if err != nil {
			return "", err
		}
	} else if err := validateRsyncCaptureLayout(layout, root); err != nil {
		return "", err
	}

	var entries []model.RsyncCaptureManifestEntry
	if rawSelection := strings.TrimSpace(task.RsyncCaptureManifest); rawSelection != "" {
		selection, decodeErr := model.DecodeRsyncCaptureManifest(rawSelection)
		if decodeErr != nil || len(selection.Entries) > maxRsyncCaptureEntries {
			return "", fmt.Errorf("rsync capture selection evidence is invalid")
		}
		entries = make([]model.RsyncCaptureManifestEntry, 0, len(selection.Entries))
		for _, selected := range selection.Entries {
			if selected.Kind != "directory" && selected.Kind != "file" && selected.Kind != "symlink" {
				return "", fmt.Errorf("rsync capture selection entry is invalid")
			}
			if _, pathErr := normalizeRsyncCapturePath(selected.Path, selected.Kind, model.TaskRunCaptureLayoutDirectoryContents, ""); pathErr != nil {
				return "", fmt.Errorf("rsync capture selection path is invalid")
			}
			entries = append(entries, model.RsyncCaptureManifestEntry{Path: selected.Path, Kind: selected.Kind})
		}
	} else {
		listed, listErr := listRsyncCaptureEntries(ctx, task, source, rules)
		if listErr != nil {
			return "", listErr
		}
		if len(listed) == 0 {
			return "", fmt.Errorf("rsync capture selection is empty or could not be enumerated")
		}
		entries = make([]model.RsyncCaptureManifestEntry, 0, len(listed))
		seen := make(map[string]struct{}, len(listed))
		sourceRoot := ""
		if layout == model.TaskRunCaptureLayoutDirectoryRoot {
			sourceRoot = filepath.Base(filepath.Clean(strings.TrimSuffix(source, string(filepath.Separator))))
		}
		for _, listedEntry := range listed {
			normalizeRoot := root
			if layout == model.TaskRunCaptureLayoutDirectoryRoot {
				normalizeRoot = sourceRoot
			}
			path, normalizeErr := normalizeRsyncCapturePath(listedEntry.path, listedEntry.kind, layout, normalizeRoot)
			if normalizeErr != nil {
				return "", normalizeErr
			}
			key := listedEntry.kind + "\x00" + path
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			entries = append(entries, model.RsyncCaptureManifestEntry{Path: path, Kind: listedEntry.kind})
			if len(entries) > maxRsyncCaptureEntries {
				return "", fmt.Errorf("rsync capture selection exceeds evidence limit")
			}
		}
		if layout == model.TaskRunCaptureLayoutDirectoryRoot && root != "" && len(listed) == 1 &&
			listed[0].kind == "directory" && !strings.HasSuffix(source, string(filepath.Separator)) &&
			!util.IsRemotePathSpec(task.RsyncTarget) && !strings.HasSuffix(strings.TrimSpace(task.RsyncTarget), string(filepath.Separator)) {
			if _, statErr := os.Lstat(strings.TrimSpace(task.RsyncTarget)); os.IsNotExist(statErr) {
				root = ""
			}
		}
	}
	if err := populateRsyncCaptureEvidence(ctx, task, source, layout, &entries); err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Path != entries[j].Path {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].Kind < entries[j].Kind
	})

	manifest := model.RsyncCaptureManifest{Version: 1, Layout: layout, Root: root, Entries: entries}
	raw, err := model.EncodeRsyncCaptureManifest(manifest)
	if err != nil {
		return "", err
	}
	if len(raw) > maxRsyncCaptureManifestLen {
		return "", fmt.Errorf("rsync capture evidence exceeds size limit")
	}
	return raw, nil
}

func validateRsyncCaptureLayout(layout, root string) error {
	switch layout {
	case model.TaskRunCaptureLayoutDirectoryRoot:
		if root != "" {
			if err := validateRsyncCaptureRoot(root); err != nil {
				return err
			}
		}
	case model.TaskRunCaptureLayoutDirectoryContents:
		if root != "" {
			return fmt.Errorf("directory-content capture root must be empty")
		}
	case model.TaskRunCaptureLayoutSingleFile:
		if root != "" {
			if err := validateRsyncCaptureRoot(root); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported rsync capture layout")
	}
	return nil
}

func validateRsyncCaptureRoot(root string) error {
	if root == "" || root == "." || root == ".." || filepath.IsAbs(root) || strings.ContainsAny(root, "\\/\x00\n\r") {
		return fmt.Errorf("invalid rsync capture root")
	}
	return nil
}

func inferRsyncCaptureLayout(ctx context.Context, task model.Task, source string) (string, string, error) {
	cleanSource := strings.TrimSuffix(source, string(filepath.Separator))
	if cleanSource == "" {
		cleanSource = source
	}
	kind, err := rsyncCaptureSourceKind(ctx, task, cleanSource)
	if err != nil {
		return "", "", err
	}
	if strings.HasSuffix(source, string(filepath.Separator)) {
		if kind != "directory" {
			return "", "", fmt.Errorf("rsync source with trailing slash is not a directory")
		}
		return model.TaskRunCaptureLayoutDirectoryContents, "", nil
	}
	switch kind {
	case "directory":
		root := filepath.Base(filepath.Clean(cleanSource))
		if err := validateRsyncCaptureRoot(root); err != nil {
			return "", "", err
		}
		return model.TaskRunCaptureLayoutDirectoryRoot, root, nil
	case "file", "symlink":
		root := ""
		target := strings.TrimSpace(task.RsyncTarget)
		targetIsDirectory := strings.HasSuffix(target, string(filepath.Separator))
		if !targetIsDirectory && !util.IsRemotePathSpec(target) {
			if info, statErr := os.Lstat(target); statErr == nil {
				targetIsDirectory = info.IsDir()
			}
		}
		if targetIsDirectory {
			root = filepath.Base(filepath.Clean(cleanSource))
			if err := validateRsyncCaptureRoot(root); err != nil {
				return "", "", err
			}
		}
		return model.TaskRunCaptureLayoutSingleFile, root, nil
	default:
		return "", "", fmt.Errorf("unsupported rsync source type")
	}
}

func rsyncCaptureSourceKind(ctx context.Context, task model.Task, source string) (string, error) {
	if strings.TrimSpace(task.Node.Host) == "" {
		info, err := os.Lstat(source)
		if err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("rsync capture source does not exist")
			}
			return "", fmt.Errorf("rsync capture source is unavailable")
		}
		switch {
		case info.IsDir():
			return "directory", nil
		case info.Mode().IsRegular():
			return "file", nil
		case info.Mode()&os.ModeSymlink != 0:
			return "symlink", nil
		default:
			return "", fmt.Errorf("unsupported rsync source type")
		}
	}
	client, err := DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeIntegrityCheck)
	if err != nil {
		return "", fmt.Errorf("rsync capture source inspection failed")
	}
	defer func() { _ = client.Close() }()
	command := fmt.Sprintf("if [ -L %s ]; then printf symlink; elif [ -d %s ]; then printf directory; elif [ -f %s ]; then printf file; else exit 1; fi", ShellEscape(source), ShellEscape(source), ShellEscape(source))
	if NeedsSudo(task.Node) {
		command = WrapWithSudoShell(command)
	}
	output, err := RunSSHCommandOutput(ctx, client, command)
	if err != nil {
		return "", fmt.Errorf("rsync capture source inspection failed")
	}

	kind := strings.TrimSpace(output)
	if kind != "directory" && kind != "file" && kind != "symlink" {
		return "", fmt.Errorf("unsupported rsync source type")
	}
	return kind, nil
}

func listRsyncCaptureEntries(ctx context.Context, task model.Task, source string, rules []string) ([]rsyncCaptureListEntry, error) {
	args := []string{"-a", "--list-only", "--dry-run", "--no-human-readable"}
	args = appendRsyncExcludeArgs(args, rules)
	cleanup := func() {}
	operand := source
	if strings.TrimSpace(task.Node.Host) != "" {
		operand = fmt.Sprintf("%s@%s:%s", ResolveSSHUser(task.Node), formatRsyncHost(task.Node.Host), source)
		sshParts, sshCleanup, err := buildRsyncSSHArgs(ctx, task.Node, sshutil.PurposeIntegrityCheck)
		if err != nil {
			return nil, fmt.Errorf("rsync capture SSH setup failed")
		}
		cleanup = sshCleanup
		args = append(args, "-e", strings.Join(sshParts, " "))
		if NeedsSudo(task.Node) {
			args = append(args, "--rsync-path", "sudo rsync")
		}
	}
	defer cleanup()
	args = append(args, "--", operand, os.DevNull)
	cmd := exec.CommandContext(ctx, rsyncCommandBinary(task), args...)
	stdout := &rsyncCaptureOutputBuffer{limit: maxRsyncCaptureManifestLen}
	stderr := &rsyncCaptureOutputBuffer{limit: maxRsyncCaptureCommandBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("rsync capture selection failed")
	}
	lines := strings.Split(strings.TrimRight(stdout.buf.String(), "\r\n"), "\n")
	entries := make([]rsyncCaptureListEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		matches := rsyncListEntryPattern.FindStringSubmatch(line)
		if len(matches) != 4 {
			return nil, fmt.Errorf("rsync capture selection output is unrecognized")
		}
		mode := matches[1]
		name := matches[3]
		kind := ""
		switch mode[0] {
		case 'd':
			kind = "directory"
		case '-':
			kind = "file"
		case 'l':
			kind = "symlink"
		default:
			return nil, fmt.Errorf("rsync capture selected unsupported file type")
		}
		if kind == "symlink" {
			if arrow := strings.LastIndex(name, " -> "); arrow >= 0 {
				name = name[:arrow]
			}
		}
		if len(name) == 0 || len(name) > maxRsyncCapturePathLen {
			return nil, fmt.Errorf("rsync capture selected invalid path")
		}
		entries = append(entries, rsyncCaptureListEntry{kind: kind, path: name})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("rsync capture selection is empty")
	}
	return entries, nil
}

func normalizeRsyncCapturePath(rawPath, kind, layout, root string) (string, error) {
	path := filepath.ToSlash(strings.TrimPrefix(rawPath, "./"))
	if path == "." {
		path = ""
	}
	if strings.ContainsRune(path, '\x00') || filepath.IsAbs(path) {
		return "", fmt.Errorf("rsync capture selected unsafe path")
	}
	for _, component := range strings.Split(path, "/") {
		if component == ".." {
			return "", fmt.Errorf("rsync capture selected parent path")
		}
	}
	switch layout {
	case model.TaskRunCaptureLayoutDirectoryRoot:
		if path == root {
			path = ""
		} else if strings.HasPrefix(path, root+"/") {
			path = strings.TrimPrefix(path, root+"/")
		} else {
			return "", fmt.Errorf("rsync capture root is missing from selection")
		}
	case model.TaskRunCaptureLayoutSingleFile:
		if kind != "file" && kind != "symlink" {
			return "", fmt.Errorf("single-file capture selected non-file entry")
		}
		path = ""
	}
	return path, nil
}

func populateRsyncCaptureEvidence(ctx context.Context, task model.Task, source, layout string, entries *[]model.RsyncCaptureManifestEntry) error {
	if strings.TrimSpace(task.Node.Host) == "" {
		for index := range *entries {
			entry := &(*entries)[index]
			path := localRsyncCaptureSourcePath(source, layout, entry.Path)
			if err := populateLocalRsyncEntry(ctx, path, entry); err != nil {
				return err
			}
		}
		return nil
	}
	client, err := DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeIntegrityCheck)
	if err != nil {
		return fmt.Errorf("rsync capture source hashing failed")
	}
	defer func() { _ = client.Close() }()
	filePaths := make([]string, 0)
	fileIndexes := make(map[string]int)
	symlinkPaths := make([]string, 0)
	symlinkIndexes := make(map[string]int)
	directoryPaths := make([]string, 0)
	for index := range *entries {
		entry := &(*entries)[index]
		path := localRsyncCaptureSourcePath(source, layout, entry.Path)
		switch entry.Kind {
		case "directory":
			directoryPaths = append(directoryPaths, path)
		case "file":
			filePaths = append(filePaths, path)
			fileIndexes[path] = index
		case "symlink":
			symlinkPaths = append(symlinkPaths, path)
			symlinkIndexes[path] = index
		}
	}
	for start := 0; start < len(directoryPaths); {
		end := rsyncCaptureBatchEnd(directoryPaths, start, "for p in")
		parts := make([]string, 0, end-start+2)
		parts = append(parts, "for p in")
		for _, path := range directoryPaths[start:end] {
			parts = append(parts, ShellEscape(path))
		}
		parts = append(parts, "; do test -d \"$p\" && test ! -L \"$p\" && find \"$p\" -mindepth 1 -maxdepth 1 -print0 > /dev/null || exit 1; done")
		command := strings.Join(parts, " ")
		if NeedsSudo(task.Node) {
			command = WrapWithSudoShell(command)
		}
		if _, err := RunSSHCommandOutput(ctx, client, command); err != nil {
			return fmt.Errorf("rsync capture directory is missing or unreadable")
		}
		start = end
	}
	for start := 0; start < len(filePaths); {
		end := rsyncCaptureBatchEnd(filePaths, start, "for p in")
		commandParts := make([]string, 0, (end-start)*2+12)
		commandParts = append(commandParts, "for p in")
		for _, path := range filePaths[start:end] {
			commandParts = append(commandParts, ShellEscape(path))
		}
		commandParts = append(commandParts, `; do test -f "$p" && test ! -L "$p" || exit 1; h=$(sha256sum -- "$p") || exit 1; s=$(stat -c '%s' -- "$p") || exit 1; printf '%s\0%s\0%s\0' "$p" "${h%% *}" "$s"; done`)
		hashCommand := strings.Join(commandParts, " ")
		if NeedsSudo(task.Node) {
			hashCommand = WrapWithSudoShell(hashCommand)
		}
		output, commandErr := RunSSHCommandOutput(ctx, client, hashCommand)
		if commandErr != nil {
			return fmt.Errorf("rsync capture source hashing failed")
		}
		records := strings.Split(output, "\x00")
		seen := make(map[string]struct{}, end-start)
		for index := 0; index+2 < len(records); index += 3 {
			path := records[index]
			digest := records[index+1]
			sizeText := records[index+2]
			if path == "" || len(digest) != sha256.Size*2 {
				return fmt.Errorf("rsync capture source hash output is invalid")
			}
			size, parseErr := strconv.ParseInt(sizeText, 10, 64)
			if parseErr != nil || size < 0 {
				return fmt.Errorf("rsync capture source hash output is invalid")
			}
			if _, decodeErr := hex.DecodeString(digest); decodeErr != nil {
				return fmt.Errorf("rsync capture source hash output is invalid")
			}
			entryIndex, ok := fileIndexes[path]
			if !ok {
				return fmt.Errorf("rsync capture source hash path is unexpected")
			}
			if _, exists := seen[path]; exists {
				return fmt.Errorf("rsync capture source hash path is duplicated")
			}
			seen[path] = struct{}{}
			(*entries)[entryIndex].SHA256 = digest
			(*entries)[entryIndex].Size = size
		}
		if len(seen) != end-start {
			return fmt.Errorf("rsync capture source hash set is incomplete")
		}
		start = end
	}
	for start := 0; start < len(symlinkPaths); {
		end := rsyncCaptureBatchEnd(symlinkPaths, start, "for p in")
		parts := make([]string, 0, (end-start)*2+8)
		parts = append(parts, "for p in")
		for _, path := range symlinkPaths[start:end] {
			parts = append(parts, ShellEscape(path))
		}
		parts = append(parts, `; do t=$(readlink -- "$p") || exit 1; printf '%s\0%s\0' "$p" "$t"; done`)
		symlinkCommand := strings.Join(parts, " ")
		if NeedsSudo(task.Node) {
			symlinkCommand = WrapWithSudoShell(symlinkCommand)
		}
		output, commandErr := RunSSHCommandOutput(ctx, client, symlinkCommand)
		if commandErr != nil {
			return fmt.Errorf("rsync capture symlink evidence failed")
		}
		records := strings.Split(output, "\x00")
		seen := make(map[string]struct{}, end-start)
		for index := 0; index+1 < len(records); index += 2 {
			path := records[index]
			target := records[index+1]
			if path == "" {
				continue
			}
			entryIndex, ok := symlinkIndexes[path]
			if !ok {
				return fmt.Errorf("rsync capture symlink path is unexpected")
			}
			if _, exists := seen[path]; exists {
				return fmt.Errorf("rsync capture symlink path is duplicated")
			}
			seen[path] = struct{}{}
			(*entries)[entryIndex].LinkTarget = target
		}
		if len(seen) != end-start {
			return fmt.Errorf("rsync capture symlink evidence is incomplete")
		}
		start = end
	}
	for index := range *entries {
		entry := &(*entries)[index]
		if entry.Kind == "file" && entry.SHA256 == "" {
			return fmt.Errorf("rsync capture source hash is missing")
		}
		if entry.Kind == "symlink" && entry.LinkTarget == "" {
			return fmt.Errorf("rsync capture symlink target is missing")
		}
	}
	return nil
}

func localRsyncCaptureSourcePath(source, layout, relative string) string {
	clean := filepath.Clean(source)
	if layout == model.TaskRunCaptureLayoutSingleFile || relative == "" {
		return clean
	}
	return filepath.Join(clean, filepath.FromSlash(relative))
}

func rsyncCaptureBatchEnd(paths []string, start int, prefix string) int {
	commandBytes := len(prefix)
	end := start
	for end < len(paths) {
		escaped := ShellEscape(paths[end])
		nextBytes := commandBytes + 1 + len(escaped)
		if end > start && nextBytes > maxRsyncCaptureCommandBytes {
			break
		}
		commandBytes = nextBytes
		end++
	}
	if end == start {
		return start + 1
	}
	return end
}

func populateLocalRsyncEntry(ctx context.Context, path string, entry *model.RsyncCaptureManifestEntry) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("rsync capture source changed during evidence collection")
	}
	switch entry.Kind {
	case "directory":
		if !info.IsDir() {
			return fmt.Errorf("rsync capture source type changed")
		}
		if err := verifyRsyncDirectoryReadable(ctx, path); err != nil {
			return err
		}
	case "file":
		if !info.Mode().IsRegular() {
			return fmt.Errorf("rsync capture source type changed")
		}
		digest, size, err := hashLocalRsyncFile(ctx, path)
		if err != nil {
			return fmt.Errorf("rsync capture source hashing failed")
		}
		entry.SHA256 = digest
		entry.Size = size
	case "symlink":
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("rsync capture source type changed")
		}
		target, err := os.Readlink(path)
		if err != nil {
			return fmt.Errorf("rsync capture symlink evidence failed")
		}
		entry.LinkTarget = target
	default:
		return fmt.Errorf("unsupported rsync capture entry type")
	}
	return nil
}

func hashLocalRsyncFile(ctx context.Context, path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	size, err := io.Copy(hasher, &rsyncCaptureContextReader{ctx: ctx, reader: file})
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

type rsyncCaptureContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *rsyncCaptureContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func verifyRsyncDirectoryReadable(ctx context.Context, path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("rsync capture directory is unreadable")
	}
	defer func() { _ = directory.Close() }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := directory.Readdirnames(128)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("rsync capture directory enumeration failed")
		}
	}
}

// VerifyRsyncCaptureManifestTarget verifies the Core-local destination bytes
// against source-side evidence captured before the transfer.
func VerifyRsyncCaptureManifestTarget(ctx context.Context, task model.Task, raw string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	manifest, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil || len(raw) > maxRsyncCaptureManifestLen || len(manifest.Entries) > maxRsyncCaptureEntries {
		return fmt.Errorf("rsync capture evidence is invalid")
	}
	if strings.TrimSpace(task.RsyncTarget) == "" || !filepath.IsAbs(task.RsyncTarget) {
		return fmt.Errorf("rsync capture target is not a Core-local absolute path")
	}
	base := strings.TrimSpace(task.RsyncTarget)
	for _, entry := range manifest.Entries {
		path := manifestTargetPath(base, manifest, entry.Path)
		if err := verifyLocalRsyncEntry(ctx, path, entry); err != nil {
			return err
		}
	}
	return nil
}

func manifestTargetPath(base string, manifest model.RsyncCaptureManifest, relative string) string {
	if manifest.Layout == model.TaskRunCaptureLayoutSingleFile {
		if manifest.Root != "" {
			return filepath.Join(base, manifest.Root)
		}
		return base
	}
	if manifest.Layout == model.TaskRunCaptureLayoutDirectoryRoot {
		base = filepath.Join(base, manifest.Root)
	}
	if relative == "" {
		return base
	}
	return filepath.Join(base, filepath.FromSlash(relative))
}

func verifyLocalRsyncEntry(ctx context.Context, path string, expected model.RsyncCaptureManifestEntry) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("rsync capture destination evidence is missing")
	}
	switch expected.Kind {
	case "directory":
		if !info.IsDir() {
			return fmt.Errorf("rsync capture destination type mismatch")
		}
		if err := verifyRsyncDirectoryReadable(ctx, path); err != nil {
			return err
		}
	case "file":
		if !info.Mode().IsRegular() {
			return fmt.Errorf("rsync capture destination type mismatch")
		}
		digest, size, err := hashLocalRsyncFile(ctx, path)
		if err != nil || digest != expected.SHA256 || (expected.Size >= 0 && size != expected.Size) {
			return fmt.Errorf("rsync capture destination content mismatch")
		}
	case "symlink":
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("rsync capture destination type mismatch")
		}
		target, err := os.Readlink(path)
		if err != nil || target != expected.LinkTarget {
			return fmt.Errorf("rsync capture destination symlink mismatch")
		}
	default:
		return fmt.Errorf("rsync capture evidence has unsupported entry type")
	}
	return nil
}
func writeRsyncCaptureFilesFrom(raw string, task model.Task) (string, func(), error) {
	manifest, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil || len(raw) > maxRsyncCaptureManifestLen || len(manifest.Entries) > maxRsyncCaptureEntries {
		return "", func() {}, fmt.Errorf("rsync capture evidence is invalid")
	}
	if manifest.Layout != model.TaskRunCaptureLayoutDirectoryContents && manifest.Layout != model.TaskRunCaptureLayoutDirectoryRoot {
		return "", func() {}, fmt.Errorf("rsync capture files-from requires a directory layout")
	}
	if layout := strings.TrimSpace(task.RsyncCaptureLayout); layout != "" && layout != manifest.Layout {
		return "", func() {}, fmt.Errorf("rsync capture layout metadata mismatch")
	}
	if root := strings.TrimSpace(task.RsyncCaptureRoot); root != "" && root != manifest.Root {
		return "", func() {}, fmt.Errorf("rsync capture root metadata mismatch")
	}
	file, err := os.CreateTemp("", "xirang-rsync-files-from-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create rsync capture files-from list: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(file.Name())
	}
	for _, entry := range manifest.Entries {
		if entry.Kind != "directory" && entry.Kind != "file" && entry.Kind != "symlink" {
			_ = file.Close()
			cleanup()
			return "", func() {}, fmt.Errorf("rsync capture files-from entry is invalid")
		}
		logicalPath, pathErr := normalizeRsyncCapturePath(entry.Path, entry.Kind, model.TaskRunCaptureLayoutDirectoryContents, "")
		if pathErr != nil {
			_ = file.Close()
			cleanup()
			return "", func() {}, fmt.Errorf("rsync capture files-from path is invalid")
		}
		// A leading ./ retains root metadata without enumerating its siblings.
		selectedPath := "./" + logicalPath
		if logicalPath == "" {
			if len(manifest.Entries) != 1 {
				continue
			}
			selectedPath = "."
		}
		if _, err := file.WriteString(selectedPath + "\x00"); err != nil {
			_ = file.Close()
			cleanup()
			return "", func() {}, fmt.Errorf("write rsync capture files-from list: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close rsync capture files-from list: %w", err)
	}
	return file.Name(), cleanup, nil
}

// ResolveRsyncRestoreSource returns the Core path selected by captured layout.
func ResolveRsyncRestoreSource(source string, info os.FileInfo, layout, root string) (string, error) {
	if err := validateRsyncCaptureLayout(layout, root); err != nil {
		return "", err
	}
	clean := filepath.Clean(source)
	switch layout {
	case model.TaskRunCaptureLayoutDirectoryContents:
		if !info.IsDir() {
			return "", fmt.Errorf("captured rsync source is not a directory")
		}
		return clean + string(filepath.Separator), nil
	case model.TaskRunCaptureLayoutDirectoryRoot:
		if !info.IsDir() {
			return "", fmt.Errorf("captured rsync source is not a directory")
		}
		selected := filepath.Join(clean, root)
		selectedInfo, err := os.Lstat(selected)
		if err != nil || !selectedInfo.IsDir() {
			return "", fmt.Errorf("captured rsync logical root is unavailable")
		}
		return selected + string(filepath.Separator), nil
	case model.TaskRunCaptureLayoutSingleFile:
		selected := clean
		if root != "" {
			selected = filepath.Join(clean, root)
		}
		selectedInfo, err := os.Lstat(selected)
		if err != nil || !selectedInfo.Mode().IsRegular() && selectedInfo.Mode()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("captured rsync file is unavailable")
		}
		return selected, nil
	default:
		return "", fmt.Errorf("captured rsync source layout is unknown")
	}
}

// RsyncSelectionDifferences performs a read-only, checksum-backed Rsync
// comparison using the exact transfer selection and exclude rules. It is used
// for ordinary backup verification; restore verification uses the captured
// source-side manifest below.
func RsyncSelectionDifferences(ctx context.Context, task model.Task, isRestore bool) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	source := strings.TrimSpace(task.RsyncSource)
	target := strings.TrimSpace(task.RsyncTarget)
	if source == "" || target == "" {
		return 0, fmt.Errorf("rsync verification paths are empty")
	}
	if isRestore {
		if strings.ContainsRune(source, '\x00') || util.IsRemotePathSpec(source) || !filepath.IsAbs(source) {
			return 0, fmt.Errorf("rsync verification source is not Core-local")
		}
		info, err := os.Lstat(source)
		if err != nil {
			return 0, fmt.Errorf("rsync verification source is unavailable")
		}
		source, err = ResolveRsyncRestoreSource(source, info, task.RsyncCaptureLayout, task.RsyncCaptureRoot)
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(task.Node.Host) == "" {
			return 0, fmt.Errorf("rsync verification node is unavailable")
		}
	} else if strings.TrimSpace(task.Node.Host) != "" {
		source = fmt.Sprintf("%s@%s:%s", ResolveSSHUser(task.Node), formatRsyncHost(task.Node.Host), source)
	}
	rules, err := parseRsyncExcludeRules(task.Policy)
	if err != nil {
		return 0, err
	}
	args := []string{"-a", "--dry-run", "--checksum", "--delete", "--out-format=%i %n%L"}
	args = appendRsyncExcludeArgs(args, rules)
	cleanup := func() {}
	if strings.TrimSpace(task.Node.Host) != "" {
		sshParts, sshCleanup, err := buildRsyncSSHArgs(ctx, task.Node, sshutil.PurposeIntegrityCheck)
		if err != nil {
			return 0, fmt.Errorf("rsync verification SSH setup failed")
		}
		cleanup = sshCleanup
		args = append(args, "-e", strings.Join(sshParts, " "))
		if NeedsSudo(task.Node) {
			args = append(args, "--rsync-path", "sudo rsync")
		}
	}
	defer cleanup()
	args = append(args, "--", source, target)
	cmd := exec.CommandContext(ctx, rsyncCommandBinary(task), args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("rsync verification comparison failed")
	}
	differences := 0
	for _, line := range strings.Split(strings.TrimRight(stdout.String(), "\r\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			differences++
		}
	}
	return differences, nil
}

// CaptureManifestEntryCount exposes the bounded evidence entry count without
// exposing the manifest representation itself.
func CaptureManifestEntryCount(raw string) (int, error) {
	manifest, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil {
		return 0, err
	}
	return len(manifest.Entries), nil
}

// ParseRsyncCaptureManifestDigest validates a stored digest field when callers
