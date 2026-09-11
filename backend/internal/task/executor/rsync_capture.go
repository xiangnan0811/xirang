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
	"syscall"

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

var rsyncListEntryPattern = regexp.MustCompile(`^(.{11}) (.*)$`)

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
	source := task.RsyncSource
	if strings.TrimSpace(source) == "" {
		return "", fmt.Errorf("rsync capture source is empty")
	}
	rules, err := parseRsyncExcludeRules(task.Policy)
	if err != nil {
		return "", err
	}

	layout := strings.TrimSpace(task.RsyncCaptureLayout)
	root := task.RsyncCaptureRoot
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
			path, pathErr := normalizeRsyncCapturePath(selected.Path, selected.Kind, model.TaskRunCaptureLayoutDirectoryContents, "")
			if pathErr != nil {
				return "", fmt.Errorf("rsync capture selection path is invalid")
			}
			entries = append(entries, model.RsyncCaptureManifestEntry{Path: path, Kind: selected.Kind})
		}
	} else {
		listed, listErr := listRsyncCaptureEntries(ctx, task, source, rules)
		if listErr != nil {
			return "", listErr
		}
		if len(listed) == 0 && layout == model.TaskRunCaptureLayoutDirectoryContents {
			kind, kindErr := rsyncCaptureSourceKind(ctx, task, strings.TrimSuffix(source, string(filepath.Separator)))
			if kindErr != nil || kind != "directory" {
				return "", fmt.Errorf("rsync capture selection is empty or could not be enumerated")
			}
			listed = []rsyncCaptureListEntry{{kind: "directory", path: "."}}
		}
		if len(listed) == 0 {
			return "", fmt.Errorf("rsync capture selection is empty or could not be enumerated")
		}
		entries = make([]model.RsyncCaptureManifestEntry, 0, len(listed))
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
			entries = append(entries, model.RsyncCaptureManifestEntry{Path: path, Kind: listedEntry.kind})
			if len(entries) > maxRsyncCaptureEntries {
				return "", fmt.Errorf("rsync capture selection exceeds evidence limit")
			}
		}
		if layout == model.TaskRunCaptureLayoutDirectoryContents {
			hasRoot := false
			for _, entry := range entries {
				if entry.Path == "" && entry.Kind == "directory" {
					hasRoot = true
					break
				}
			}
			if !hasRoot {
				entries = append(entries, model.RsyncCaptureManifestEntry{Path: "", Kind: "directory"})
			}
		}
		if layout == model.TaskRunCaptureLayoutDirectoryRoot && root != "" && len(listed) == 1 &&
			listed[0].kind == "directory" && !strings.HasSuffix(source, string(filepath.Separator)) &&
			!util.IsRemotePathSpec(task.RsyncTarget) && !strings.HasSuffix(task.RsyncTarget, string(filepath.Separator)) {
			if _, statErr := os.Lstat(task.RsyncTarget); os.IsNotExist(statErr) {
				root = ""
			}
		}
	}
	if err := validateRsyncCaptureEntries(entries, layout, root, false); err != nil {
		return "", err
	}
	if err := populateRsyncCaptureEvidence(ctx, task, source, layout, &entries); err != nil {
		return "", err
	}
	if err := validateRsyncCaptureEntries(entries, layout, root, true); err != nil {
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

func validateRsyncCaptureEntries(entries []model.RsyncCaptureManifestEntry, layout, root string, requireEvidence bool) error {
	if len(entries) == 0 || len(entries) > maxRsyncCaptureEntries {
		return fmt.Errorf("rsync capture selection has invalid entry count")
	}
	if err := validateRsyncCaptureLayout(layout, root); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.Kind != "directory" && entry.Kind != "file" && entry.Kind != "symlink" {
			return fmt.Errorf("rsync capture selection entry is invalid")
		}
		path, err := normalizeRsyncCapturePath(entry.Path, entry.Kind, model.TaskRunCaptureLayoutDirectoryContents, "")
		if err != nil || path != entry.Path || len(path) > maxRsyncCapturePathLen {
			return fmt.Errorf("rsync capture selection path is invalid")
		}
		if _, exists := seen[path]; exists {
			return fmt.Errorf("rsync capture selection contains duplicate path")
		}
		seen[path] = struct{}{}
		if entry.Size < 0 {
			return fmt.Errorf("rsync capture entry size is invalid")
		}
		if entry.Kind == "symlink" && requireEvidence &&
			(entry.LinkTarget == "" || len(entry.LinkTarget) > maxRsyncCapturePathLen ||
				strings.ContainsRune(entry.LinkTarget, '\x00')) {
			return fmt.Errorf("rsync capture symlink evidence is invalid")
		}
	}
	return nil
}

func validRsyncSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
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
	if root == "" || root == "." || root == ".." || filepath.IsAbs(root) ||
		strings.ContainsRune(root, '\x00') || strings.ContainsRune(root, '/') ||
		(filepath.Separator == '\\' && strings.ContainsRune(root, '\\')) {
		return fmt.Errorf("invalid rsync capture root")
	}
	if filepath.Base(root) != root {
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
		target := task.RsyncTarget
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
	args := []string{"-a", "--dry-run", "--8-bit-output", "--out-format=%i %n"}
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

	destination, err := os.MkdirTemp("", "xirang-rsync-capture-dst-*")
	if err != nil {
		return nil, fmt.Errorf("create rsync capture destination: %w", err)
	}
	_ = os.Chmod(destination, 0o700)
	defer func() { _ = os.RemoveAll(destination) }()
	args = append(args, "--", operand, destination+string(filepath.Separator))
	cmd := exec.CommandContext(ctx, rsyncCommandBinary(task), args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
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
		if len(matches) != 3 {
			return nil, fmt.Errorf("rsync capture selection output is unrecognized")
		}
		itemized := matches[1]
		name, decodeErr := decodeRsyncDisplayPath(matches[2])
		if decodeErr != nil {
			return nil, decodeErr
		}
		kind := ""
		switch itemized[1] {
		case 'd':
			kind = "directory"
		case 'f':
			kind = "file"
		case 'l', 'L':
			kind = "symlink"
		default:
			return nil, fmt.Errorf("rsync capture selected unsupported file type")
		}
		if len(name) == 0 || len(name) > maxRsyncCapturePathLen {
			return nil, fmt.Errorf("rsync capture selected invalid path")
		}
		entries = append(entries, rsyncCaptureListEntry{kind: kind, path: name})
		if len(entries) > maxRsyncCaptureEntries {
			return nil, fmt.Errorf("rsync capture selection exceeds evidence limit")
		}
	}
	return entries, nil
}

func decodeRsyncDisplayPath(raw string) (string, error) {
	var decoded strings.Builder
	decoded.Grow(len(raw))
	for index := 0; index < len(raw); index++ {
		if raw[index] != '\\' {
			decoded.WriteByte(raw[index])
			continue
		}
		if index+4 < len(raw) && raw[index+1] == '#' &&
			isRsyncOctal(raw[index+2]) && isRsyncOctal(raw[index+3]) && isRsyncOctal(raw[index+4]) {
			value := (int(raw[index+2]-'0') << 6) |
				(int(raw[index+3]-'0') << 3) |
				int(raw[index+4]-'0')
			if value > 0xff {
				return "", fmt.Errorf("rsync capture selected path has invalid octal escape")
			}
			if value == 0 {
				return "", fmt.Errorf("rsync capture selected path contains NUL")
			}
			decoded.WriteByte(byte(value))
			index += 4
			continue
		}
		decoded.WriteByte('\\')
	}
	if decoded.Len() > maxRsyncCapturePathLen {
		return "", fmt.Errorf("rsync capture selected invalid path")
	}
	return decoded.String(), nil
}

func isRsyncOctal(value byte) bool {
	return value >= '0' && value <= '7'
}

func normalizeRsyncCapturePath(rawPath, kind, layout, root string) (string, error) {
	path := filepath.ToSlash(strings.TrimPrefix(rawPath, "./"))
	if kind == "directory" {
		path = strings.TrimRight(path, "/")
	}
	if path == "." {
		path = ""
	}
	if strings.ContainsRune(path, '\x00') || filepath.IsAbs(path) {
		return "", fmt.Errorf("rsync capture selected unsafe path")
	}
	if path != "" {
		for _, component := range strings.Split(path, "/") {
			if component == "" || component == "." || component == ".." {
				if component == ".." {
					return "", fmt.Errorf("rsync capture selected parent path")
				}
				return "", fmt.Errorf("rsync capture selected invalid path")
			}
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
		commandParts = append(commandParts, `; do test -f "$p" && test ! -L "$p" || exit 1; h=$(sha256sum < "$p") || exit 1; h=${h%% *}; s=$(stat -c '%s' -- "$p") || exit 1; printf '%s\0%s\0%s\0' "$p" "$h" "$s"; done`)
		hashCommand := strings.Join(commandParts, " ")
		if NeedsSudo(task.Node) {
			hashCommand = WrapWithSudoShell(hashCommand)
		}
		output, commandErr := RunSSHCommandOutput(ctx, client, hashCommand)
		if commandErr != nil {
			return fmt.Errorf("rsync capture source hashing failed")
		}
		if len(output) > maxRsyncCaptureManifestLen {
			return fmt.Errorf("rsync capture source hash output is too large")
		}
		records := strings.Split(output, "\x00")
		if len(records) == 0 || records[len(records)-1] != "" ||
			len(records)-1 != (end-start)*3 {
			return fmt.Errorf("rsync capture source hash output is invalid")
		}
		seen := make(map[string]struct{}, end-start)
		for index := 0; index < len(records)-1; index += 3 {
			path := records[index]
			digest := records[index+1]
			sizeText := records[index+2]
			if path == "" || !validRsyncSHA256(digest) {
				return fmt.Errorf("rsync capture source hash output is invalid")
			}
			size, parseErr := strconv.ParseInt(sizeText, 10, 64)
			if parseErr != nil || size < 0 {
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
		parts = append(parts, `; do t=$(readlink -n -- "$p"; rc=$?; printf '\001'; exit "$rc") || exit 1; t=${t%?}; printf '%s\0%s\0' "$p" "$t"; done`)
		symlinkCommand := strings.Join(parts, " ")
		if NeedsSudo(task.Node) {
			symlinkCommand = WrapWithSudoShell(symlinkCommand)
		}
		output, commandErr := RunSSHCommandOutput(ctx, client, symlinkCommand)
		if commandErr != nil {
			return fmt.Errorf("rsync capture symlink evidence failed")
		}
		if len(output) > maxRsyncCaptureManifestLen {
			return fmt.Errorf("rsync capture symlink output is too large")
		}
		records := strings.Split(output, "\x00")
		if len(records) == 0 || records[len(records)-1] != "" ||
			len(records)-1 != (end-start)*2 {
			return fmt.Errorf("rsync capture symlink output is invalid")
		}
		seen := make(map[string]struct{}, end-start)
		for index := 0; index < len(records)-1; index += 2 {
			path := records[index]
			target := records[index+1]
			if path == "" || len(path) > maxRsyncCapturePathLen || len(target) > maxRsyncCapturePathLen {
				return fmt.Errorf("rsync capture symlink output is invalid")
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
func hashLocalRsyncFile(ctx context.Context, path string) (string, int64, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
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
		if len(target) > maxRsyncCapturePathLen {
			return fmt.Errorf("rsync capture symlink target is too long")
		}
		entry.LinkTarget = target
	default:
		return fmt.Errorf("unsupported rsync capture entry type")
	}
	return nil
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
	if err != nil || len(raw) > maxRsyncCaptureManifestLen {
		return fmt.Errorf("rsync capture evidence is invalid")
	}
	if err := validateRsyncCaptureEntries(manifest.Entries, manifest.Layout, manifest.Root, true); err != nil {
		return fmt.Errorf("rsync capture evidence is invalid")
	}
	if strings.TrimSpace(task.RsyncTarget) == "" || !filepath.IsAbs(task.RsyncTarget) {
		return fmt.Errorf("rsync capture target is not a Core-local absolute path")
	}
	base := task.RsyncTarget
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
