package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"xirang/backend/internal/model"
	"xirang/backend/internal/rsyncconfinement"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"
)

const (
	maxRsyncCaptureEntries      = 100000
	maxRsyncCaptureManifestLen  = 8 << 20
	maxRsyncCapturePathLen      = 4096
	maxRsyncCaptureCommandBytes = 64 << 10
)

// RsyncCaptureRole selects which configured filesystem boundary owns the path
// being enumerated. Backup captures use SourceRole; restore verification
// captures the node's already-restored target with TargetRole.
type RsyncCaptureRole string

const (
	RsyncCaptureSourceRole RsyncCaptureRole = "source"
	RsyncCaptureTargetRole RsyncCaptureRole = "target"
)

func rsyncCapturePolicy(policy rsyncconfinement.Policy, role RsyncCaptureRole) ([]string, string, error) {
	switch role {
	case RsyncCaptureSourceRole:
		return policy.SourceRoots, "rsync_capture_source", nil
	case RsyncCaptureTargetRole:
		return policy.TargetRoots, "rsync_capture_target", nil
	default:
		return nil, "", fmt.Errorf("unsupported rsync capture role %q", role)
	}
}

var rsyncListEntryPattern = regexp.MustCompile(`^(.{11}) (.*)$`)

type rsyncCaptureListEntry struct {
	kind string
	path string
}

type rsyncCaptureOutputBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *rsyncCaptureOutputBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return 0, io.ErrShortWrite
	}
	if len(p) > remaining {
		b.truncated = true
		_, _ = b.buf.Write(p[:remaining])
		return remaining, io.ErrShortWrite
	}
	return b.buf.Write(p)
}

// Keep the original error available to errors.Is/As while exposing bounded,
// sanitized diagnostics to task history. In particular, do not flatten an
// ExitError or a context cancellation into a generic capture failure.
type rsyncCaptureFailure struct {
	cause   error
	message string
}

func (e *rsyncCaptureFailure) Error() string { return e.message }
func (e *rsyncCaptureFailure) Unwrap() error { return e.cause }

func newRsyncCaptureFailure(operation string, cause error, outputs ...*rsyncCaptureOutputBuffer) error {
	message := operation + ": " + cause.Error()
	for _, output := range outputs {
		if output.truncated {
			message += fmt.Sprintf("; diagnostic output exceeded %d bytes", output.limit)
		}
	}
	// The first buffer is stderr. stdout can contain a complete file listing,
	// so only its limit failure is relevant to the diagnostic.
	if len(outputs) > 0 {
		if diagnostic := strings.TrimSpace(outputs[0].buf.String()); diagnostic != "" {
			message += "; diagnostic: " + diagnostic
		}
	}
	return &rsyncCaptureFailure{cause: cause, message: util.SanitizeMessage(message)}
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
func CaptureRsyncManifest(ctx context.Context, task model.Task, role RsyncCaptureRole) (string, error) {
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
		layout, root, err = inferRsyncCaptureLayout(ctx, task, source, role)
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
		listed, listErr := listRsyncCaptureEntries(ctx, task, source, rules, role)
		if listErr != nil {
			return "", listErr
		}
		if len(listed) == 0 && layout == model.TaskRunCaptureLayoutDirectoryContents {
			kind, kindErr := rsyncCaptureSourceKind(ctx, task, strings.TrimSuffix(source, string(filepath.Separator)), role)
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
	if err := populateRsyncCaptureEvidence(ctx, task, source, layout, root, rules, &entries, role); err != nil {
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

func inferRsyncCaptureLayout(ctx context.Context, task model.Task, source string, role RsyncCaptureRole) (string, string, error) {
	cleanSource := strings.TrimSuffix(source, string(filepath.Separator))
	if cleanSource == "" {
		cleanSource = source
	}
	kind, err := rsyncCaptureSourceKind(ctx, task, cleanSource, role)
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
			policy, policyErr := rsyncconfinement.LoadPolicyFromEnv()
			if policyErr != nil {
				return "", "", policyErr
			}
			kind, kindErr := rsyncCaptureLocalPathKind(target, policy.TargetRoots, "rsync_capture_target")
			if kindErr == nil {
				targetIsDirectory = kind == "directory"
			} else if !os.IsNotExist(kindErr) {
				return "", "", kindErr
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

func rsyncCaptureSourceKind(ctx context.Context, task model.Task, source string, role RsyncCaptureRole) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	policy, err := rsyncconfinement.LoadPolicyFromEnv()
	if err != nil {
		return "", err
	}
	checkedPath := strings.TrimSuffix(strings.TrimSpace(source), string(filepath.Separator))
	if checkedPath == "" {
		checkedPath = source
	}
	if strings.TrimSpace(task.Node.Host) != "" {
		checkedPath = filepath.Clean(checkedPath)
	}
	roots, label, roleErr := rsyncCapturePolicy(policy, role)
	if roleErr != nil {
		return "", roleErr
	}
	if err := rsyncconfinement.ValidatePath(checkedPath, roots, label); err != nil {
		return "", err
	}
	if strings.TrimSpace(task.Node.Host) == "" {
		kind, kindErr := rsyncCaptureLocalPathKind(checkedPath, roots, label)
		if kindErr != nil {
			if os.IsNotExist(kindErr) {
				return "", newRsyncCaptureFailure("rsync capture source does not exist", kindErr)
			}
			return "", newRsyncCaptureFailure("rsync capture source is unavailable", kindErr)
		}
		return kind, nil
	}
	listed, listErr := listRsyncCaptureEntries(ctx, task, checkedPath, nil, role)
	if listErr != nil {
		return "", newRsyncCaptureFailure("rsync capture source inspection failed", listErr)
	}
	if len(listed) == 0 {
		return "", fmt.Errorf("rsync capture source does not exist")
	}
	base := filepath.Base(checkedPath)
	for _, entry := range listed {
		if strings.TrimSuffix(entry.path, string(filepath.Separator)) == base {
			return entry.kind, nil
		}
	}
	return listed[0].kind, nil
}

func prepareRsyncCaptureSource(
	ctx context.Context,
	task model.Task,
	source string,
	policy rsyncconfinement.Policy,
	role RsyncCaptureRole,
) (operand, localSource string, transportArgs, runtimeReadPaths []string, cleanup func(), err error) {
	cleanup = func() {}
	roots, label, roleErr := rsyncCapturePolicy(policy, role)
	if roleErr != nil {
		return "", "", nil, nil, cleanup, roleErr
	}
	if strings.TrimSpace(task.Node.Host) == "" {
		if policy.Configured() && util.IsRemotePathSpec(source) {
			return "", "", nil, nil, cleanup, fmt.Errorf("受限 Rsync 捕获不支持未绑定节点的远程源")
		}
		if err := rsyncconfinement.ValidatePath(strings.TrimSuffix(strings.TrimSpace(source), string(filepath.Separator)), roots, label); err != nil {
			return "", "", nil, nil, cleanup, err
		}
		return source, source, nil, nil, cleanup, nil
	}
	remotePath := filepath.Clean(strings.TrimSuffix(strings.TrimSpace(source), string(filepath.Separator)))
	if remotePath == "." || !filepath.IsAbs(remotePath) {
		return "", "", nil, nil, cleanup, fmt.Errorf("rsync capture remote source must be absolute")
	}
	operand = fmt.Sprintf("%s@%s:%s", ResolveSSHUser(task.Node), formatRsyncHost(task.Node.Host), source)
	sshParts, sshCleanup, sshErr := buildRsyncSSHArgs(ctx, task.Node, sshutil.PurposeIntegrityCheck)
	if sshErr != nil {
		return "", "", nil, nil, cleanup, newRsyncCaptureFailure("rsync capture SSH setup failed", sshErr)
	}
	cleanup = sshCleanup
	transportArgs = append(transportArgs, "-e", strings.Join(sshParts, " "))
	if len(roots) > 0 {
		remoteCommand, remoteErr := rsyncconfinement.BuildRemoteRsyncPath(
			"read", "", rsyncCommandBinary(task), roots, remotePath, NeedsSudo(task.Node),
		)
		if remoteErr != nil {
			cleanup()
			return "", "", nil, nil, func() {}, remoteErr
		}
		transportArgs = append(transportArgs, "--rsync-path", remoteCommand)
	} else if NeedsSudo(task.Node) {
		transportArgs = append(transportArgs, "--rsync-path", "sudo "+rsyncCommandBinary(task))
	}
	return operand, "", transportArgs, rsyncSSHRuntimeReadPaths(sshParts), cleanup, nil
}

func runRsyncCaptureCommand(
	ctx context.Context,
	task model.Task,
	args []string,
	localSource string,
	localTarget string,
	trustedLocalSource bool,
	localSourceTargetRole bool,
	runtimeReadPaths []string,
	stdout, stderr io.Writer,
) error {
	cmd, cleanup, err := rsyncconfinement.NewCommand(ctx, rsyncconfinement.CommandRequest{
		Binary:                rsyncCommandBinary(task),
		Args:                  args,
		LocalSource:           localSource,
		LocalTarget:           localTarget,
		LocalSourceTargetRole: localSourceTargetRole,
		RuntimeReadPaths:      append([]string(nil), runtimeReadPaths...),
		TrustedLocalSource:    trustedLocalSource,
		TrustedLocalTarget:    true,
	})
	if err != nil {
		return err
	}
	defer cleanup()
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return errors.Join(err, ctx.Err())
	}
	return nil
}

func copyRsyncCaptureSelection(ctx context.Context, task model.Task, source string, rules []string, role RsyncCaptureRole) (string, error) {
	policy, err := rsyncconfinement.LoadPolicyFromEnv()
	if err != nil {
		return "", err
	}
	operand, localSource, transportArgs, runtimeReadPaths, cleanup, err := prepareRsyncCaptureSource(ctx, task, source, policy, role)
	if err != nil {
		return "", err
	}
	defer cleanup()
	destination, err := os.MkdirTemp("", "xirang-rsync-capture-evidence-*")
	if err != nil {
		return "", fmt.Errorf("create rsync capture evidence tree: %w", err)
	}
	_ = os.Chmod(destination, 0o700)
	args := []string{"-a", "--8-bit-output"}
	args = appendRsyncExcludeArgs(args, rules)
	args = append(args, transportArgs...)
	args = append(args, "--", operand, destination+string(filepath.Separator))
	stdout := &rsyncCaptureOutputBuffer{limit: maxRsyncCaptureCommandBytes}
	stderr := &rsyncCaptureOutputBuffer{limit: maxRsyncCaptureCommandBytes}
	if err := runRsyncCaptureCommand(ctx, task, args, localSource, destination, false, role == RsyncCaptureTargetRole, runtimeReadPaths, stdout, stderr); err != nil {
		_ = os.RemoveAll(destination)
		return "", newRsyncCaptureFailure("rsync capture evidence copy failed", err, stderr, stdout)
	}
	return destination, nil
}

func rsyncCaptureStagedPath(staging, source, layout, root, relative string) string {
	base := filepath.Clean(staging)
	if layout == model.TaskRunCaptureLayoutDirectoryRoot || layout == model.TaskRunCaptureLayoutSingleFile {
		sourceBase := filepath.Base(filepath.Clean(strings.TrimSuffix(source, string(filepath.Separator))))
		base = filepath.Join(base, sourceBase)
	}
	if relative == "" {
		return base
	}
	return filepath.Join(base, filepath.FromSlash(relative))
}

func listRsyncCaptureEntries(ctx context.Context, task model.Task, source string, rules []string, role RsyncCaptureRole) ([]rsyncCaptureListEntry, error) {
	policy, err := rsyncconfinement.LoadPolicyFromEnv()
	if err != nil {
		return nil, err
	}
	operand, localSource, transportArgs, runtimeReadPaths, cleanup, err := prepareRsyncCaptureSource(ctx, task, source, policy, role)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	destination, err := os.MkdirTemp("", "xirang-rsync-capture-dst-*")
	if err != nil {
		return nil, fmt.Errorf("create rsync capture destination: %w", err)
	}
	_ = os.Chmod(destination, 0o700)
	defer func() { _ = os.RemoveAll(destination) }()
	args := []string{"-a", "--dry-run", "--8-bit-output", "--out-format=%i %n"}
	args = appendRsyncExcludeArgs(args, rules)
	args = append(args, transportArgs...)
	args = append(args, "--", operand, destination+string(filepath.Separator))
	stdout := &rsyncCaptureOutputBuffer{limit: maxRsyncCaptureManifestLen}
	stderr := &rsyncCaptureOutputBuffer{limit: maxRsyncCaptureCommandBytes}
	if err := runRsyncCaptureCommand(ctx, task, args, localSource, destination, false, role == RsyncCaptureTargetRole, runtimeReadPaths, stdout, stderr); err != nil {
		return nil, newRsyncCaptureFailure("rsync capture selection failed", err, stderr, stdout)
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

func populateRsyncCaptureEvidence(
	ctx context.Context,
	task model.Task,
	source, layout, root string,
	rules []string,
	entries *[]model.RsyncCaptureManifestEntry,
	role RsyncCaptureRole,
) error {
	staging, err := copyRsyncCaptureSelection(ctx, task, source, rules, role)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	for index := range *entries {
		entry := &(*entries)[index]
		path := rsyncCaptureStagedPath(staging, source, layout, root, entry.Path)
		if err := populateLocalRsyncEntry(ctx, path, entry); err != nil {
			return err
		}
	}
	if layout != model.TaskRunCaptureLayoutSingleFile {
		verifyRoot := staging
		if layout == model.TaskRunCaptureLayoutDirectoryRoot {
			sourceBase := filepath.Base(filepath.Clean(strings.TrimSuffix(source, string(filepath.Separator))))
			verifyRoot = filepath.Join(staging, sourceBase)
		}
		manifest := model.RsyncCaptureManifest{Layout: layout, Root: root, Entries: *entries}
		if err := verifyStagedRsyncTree(verifyRoot, manifest); err != nil {
			return fmt.Errorf("rsync capture selection changed during evidence collection: %w", err)
		}
	}
	return nil
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
	target := strings.TrimSpace(task.RsyncTarget)
	if target == "" || !filepath.IsAbs(target) {
		return fmt.Errorf("rsync capture target is not a Core-local absolute path")
	}
	policy, err := rsyncconfinement.LoadPolicyFromEnv()
	if err != nil {
		return err
	}
	resolved := target
	directory := manifest.Layout != model.TaskRunCaptureLayoutSingleFile
	if manifest.Layout == model.TaskRunCaptureLayoutDirectoryRoot ||
		(manifest.Layout == model.TaskRunCaptureLayoutSingleFile && manifest.Root != "") {
		resolved = filepath.Join(resolved, manifest.Root)
	}
	root, base, err := openRsyncCaptureVerificationRoot(resolved, policy.TargetRoots, directory)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, entry := range manifest.Entries {
		relative := rsyncCaptureManifestSourcePath(base, entry.Path)
		if err := verifyRsyncRootEntry(ctx, root, relative, entry, "destination"); err != nil {
			return err
		}
	}
	return nil
}

// VerifyRsyncCaptureManifestSource verifies the exact Core-local source bytes
// selected by a stored manifest. It opens a descriptor-rooted filesystem and
// stages no unselected files, so excluded files left beside a prior backup do
// not change restore evidence.
func VerifyRsyncCaptureManifestSource(ctx context.Context, task model.Task, raw string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manifest, err := model.DecodeRsyncCaptureManifest(raw)
	if err != nil || len(raw) > maxRsyncCaptureManifestLen {
		return fmt.Errorf("rsync capture evidence is invalid")
	}
	if err := validateRsyncCaptureEntries(manifest.Entries, manifest.Layout, manifest.Root, true); err != nil {
		return fmt.Errorf("rsync capture evidence is invalid")
	}
	if manifest.Layout == model.TaskRunCaptureLayoutSingleFile &&
		(len(manifest.Entries) != 1 || manifest.Entries[0].Path != "" ||
			(manifest.Entries[0].Kind != "file" && manifest.Entries[0].Kind != "symlink")) {
		return fmt.Errorf("rsync capture single-file evidence is invalid")
	}
	if task.RsyncCaptureLayout != manifest.Layout || task.RsyncCaptureRoot != manifest.Root {
		return fmt.Errorf("rsync capture metadata mismatch")
	}
	source := strings.TrimSpace(task.RsyncSource)
	if source == "" || !filepath.IsAbs(source) || strings.ContainsRune(source, '\x00') {
		return fmt.Errorf("rsync capture source is not a Core-local absolute path")
	}
	policy, err := rsyncconfinement.LoadPolicyFromEnv()
	if err != nil {
		return err
	}
	if len(policy.TargetRoots) > 0 {
		if err := policy.ValidateTarget(source, "rsync_target"); err != nil {
			return err
		}
	}
	resolved := source
	if manifest.Layout == model.TaskRunCaptureLayoutDirectoryRoot ||
		(manifest.Layout == model.TaskRunCaptureLayoutSingleFile && manifest.Root != "") {
		resolved = filepath.Join(resolved, manifest.Root)
	}
	root, base, err := openRsyncCaptureVerificationRoot(
		resolved,
		policy.TargetRoots,
		manifest.Layout != model.TaskRunCaptureLayoutSingleFile,
	)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, entry := range manifest.Entries {
		relative := rsyncCaptureManifestSourcePath(base, entry.Path)
		if err := verifyRsyncRootEntry(ctx, root, relative, entry, "source"); err != nil {
			return err
		}
	}
	return nil
}

func openRsyncCapturePathRoot(path string, allowedRoots []string) (*os.Root, string, error) {
	clean := filepath.Clean(path)
	if clean == "." || !filepath.IsAbs(clean) {
		return nil, "", fmt.Errorf("rsync capture path must be absolute")
	}
	if len(allowedRoots) > 0 {
		matchingRoots := make([]string, 0, len(allowedRoots))
		for _, configuredRoot := range allowedRoots {
			configuredRoot = filepath.Clean(strings.TrimSpace(configuredRoot))
			if configuredRoot == "" || configuredRoot == "." {
				continue
			}
			relative, err := filepath.Rel(configuredRoot, clean)
			if err != nil || relative == ".." ||
				strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
				continue
			}
			matchingRoots = append(matchingRoots, configuredRoot)
		}
		sort.SliceStable(matchingRoots, func(i, j int) bool {
			return len(matchingRoots[i]) > len(matchingRoots[j])
		})
		var candidateErr error
		for _, configuredRoot := range matchingRoots {
			relative, relativeErr := filepath.Rel(configuredRoot, clean)
			if relativeErr != nil {
				candidateErr = relativeErr
				continue
			}
			root, openErr := os.OpenRoot(configuredRoot)
			if openErr != nil {
				candidateErr = openErr
				continue
			}
			if _, statErr := root.Lstat(relative); statErr != nil {
				_ = root.Close()
				candidateErr = statErr
				continue
			}
			return root, relative, nil
		}
		if candidateErr != nil {
			return nil, "", candidateErr
		}
		return nil, "", fmt.Errorf("%w: rsync capture path is outside configured roots", rsyncconfinement.ErrCapabilityUnavailable)
	}
	parent := filepath.Dir(clean)
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, "", err
	}
	return root, filepath.Base(clean), nil
}

func rsyncCaptureLocalPathKind(path string, allowedRoots []string, label string) (string, error) {
	clean := filepath.Clean(strings.TrimSuffix(strings.TrimSpace(path), string(filepath.Separator)))
	if err := rsyncconfinement.ValidatePath(clean, allowedRoots, label); err != nil {
		return "", err
	}
	root, relative, err := openRsyncCapturePathRoot(clean, allowedRoots)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(relative)
	if err != nil {
		return "", err
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

func openRsyncCaptureVerificationRoot(path string, allowedRoots []string, directory bool) (*os.Root, string, error) {
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	if base == string(filepath.Separator) {
		base = "."
	} else if err := validateRsyncCaptureRoot(base); err != nil {
		return nil, "", err
	}
	parent := filepath.Dir(clean)
	if directory {
		if len(allowedRoots) == 0 {
			root, err := os.OpenRoot(clean)
			if err != nil {
				return nil, "", fmt.Errorf("open rsync capture source root: %w", err)
			}
			return root, ".", nil
		}
		for _, configuredRoot := range allowedRoots {
			configuredRoot = filepath.Clean(strings.TrimSpace(configuredRoot))
			if configuredRoot == "" || configuredRoot == "." {
				continue
			}
			relative, err := filepath.Rel(configuredRoot, clean)
			if err != nil || relative == ".." ||
				strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
				continue
			}
			allowed, err := os.OpenRoot(configuredRoot)
			if err != nil {
				continue
			}
			if relative == "." {
				return allowed, ".", nil
			}
			root, openErr := allowed.OpenRoot(relative)
			_ = allowed.Close()
			if openErr != nil {
				continue
			}
			return root, ".", nil
		}
		return nil, "", fmt.Errorf("%w: Core backup source is outside configured Rsync target roots", rsyncconfinement.ErrCapabilityUnavailable)
	}
	if len(allowedRoots) == 0 {
		root, err := os.OpenRoot(parent)
		if err != nil {
			return nil, "", fmt.Errorf("open rsync capture source root: %w", err)
		}
		return root, base, nil
	}
	for _, configuredRoot := range allowedRoots {
		configuredRoot = filepath.Clean(strings.TrimSpace(configuredRoot))
		if configuredRoot == "" || configuredRoot == "." {
			continue
		}
		relative, err := filepath.Rel(configuredRoot, parent)
		if err != nil || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			continue
		}
		allowed, err := os.OpenRoot(configuredRoot)
		if err != nil {
			continue
		}
		if relative == "." {
			return allowed, base, nil
		}
		root, openErr := allowed.OpenRoot(relative)
		_ = allowed.Close()
		if openErr != nil {
			continue
		}
		return root, base, nil
	}
	return nil, "", fmt.Errorf("%w: Core backup source is outside configured Rsync target roots", rsyncconfinement.ErrCapabilityUnavailable)
}

func rsyncCaptureManifestSourcePath(base, relative string) string {
	if relative == "" {
		return base
	}
	return filepath.Join(base, filepath.FromSlash(relative))
}

func verifyRsyncRootEntry(ctx context.Context, root *os.Root, path string, expected model.RsyncCaptureManifestEntry, label string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := root.Lstat(path)
	if err != nil {
		return fmt.Errorf("rsync capture %s evidence is missing", label)
	}
	switch expected.Kind {
	case "directory":
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("rsync capture %s evidence type mismatch", label)
		}
		directory, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("rsync capture %s directory is unreadable", label)
		}
		defer func() { _ = directory.Close() }()
		openedInfo, err := directory.Stat()
		if err != nil || !openedInfo.IsDir() || openedInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, openedInfo) {
			return fmt.Errorf("rsync capture %s evidence type mismatch", label)
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := directory.Readdirnames(128)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("rsync capture %s directory enumeration failed", label)
			}
		}
	case "file":
		if !info.Mode().IsRegular() {
			return fmt.Errorf("rsync capture %s evidence type mismatch", label)
		}
		file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("rsync capture %s file is unreadable", label)
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
			_ = file.Close()
			return fmt.Errorf("rsync capture %s evidence type mismatch", label)
		}
		hasher := sha256.New()
		size, copyErr := io.Copy(hasher, &rsyncCaptureContextReader{ctx: ctx, reader: file})
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("rsync capture %s hashing failed", label)
		}
		if closeErr != nil {
			return fmt.Errorf("rsync capture %s close failed", label)
		}
		if hex.EncodeToString(hasher.Sum(nil)) != expected.SHA256 ||
			(expected.Size >= 0 && size != expected.Size) {
			return fmt.Errorf("rsync capture %s evidence content mismatch", label)
		}
		return nil
	case "symlink":
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("rsync capture %s evidence type mismatch", label)
		}
		target, err := root.Readlink(path)
		if err != nil || target != expected.LinkTarget {
			return fmt.Errorf("rsync capture %s evidence symlink mismatch", label)
		}
		return nil
	default:
		return fmt.Errorf("rsync capture evidence has unsupported entry type")
	}
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
	policy, err := rsyncconfinement.LoadPolicyFromEnv()
	if err != nil {
		return 0, err
	}
	rules, err := parseRsyncExcludeRules(task.Policy)
	if err != nil {
		return 0, err
	}
	args := []string{"-a", "--dry-run", "--checksum", "--delete", "--out-format=%i %n%L"}
	args = appendRsyncExcludeArgs(args, rules)
	sourceOperand := source
	targetOperand := target
	localSource := ""
	localTarget := ""
	trustedLocalSource := false
	runtimeReadPaths := []string(nil)

	if isRestore {
		if strings.ContainsRune(source, '\x00') || util.IsRemotePathSpec(source) || !filepath.IsAbs(source) {
			return 0, fmt.Errorf("rsync verification source is not Core-local")
		}
		info, err := os.Lstat(source)
		if err != nil {
			return 0, fmt.Errorf("rsync verification source is unavailable")
		}
		sourceOperand, err = ResolveRsyncRestoreSource(source, info, task.RsyncCaptureLayout, task.RsyncCaptureRoot)
		if err != nil {
			return 0, err
		}
		localSource = sourceOperand
		trustedLocalSource = true
		if strings.TrimSpace(task.Node.Host) == "" {
			return 0, fmt.Errorf("rsync verification node is unavailable")
		}
		sshParts, sshCleanup, sshErr := buildRsyncSSHArgs(ctx, task.Node, sshutil.PurposeIntegrityCheck)
		if sshErr != nil {
			return 0, fmt.Errorf("rsync verification SSH setup failed")
		}
		defer sshCleanup()
		args = append(args, "-e", strings.Join(sshParts, " "))
		runtimeReadPaths = rsyncSSHRuntimeReadPaths(sshParts)
		targetOperand = fmt.Sprintf("%s@%s:%s", ResolveSSHUser(task.Node), formatRsyncHost(task.Node.Host), target)
		if len(policy.TargetRoots) > 0 {
			remoteCommand, remoteErr := rsyncconfinement.BuildRemoteRsyncPath(
				"write", "", rsyncCommandBinary(task), policy.TargetRoots, target, NeedsSudo(task.Node),
			)
			if remoteErr != nil {
				return 0, remoteErr
			}
			args = append(args, "--rsync-path", remoteCommand)
		} else if NeedsSudo(task.Node) {
			args = append(args, "--rsync-path", "sudo "+rsyncCommandBinary(task))
		}
	} else if strings.TrimSpace(task.Node.Host) != "" {
		operand, _, transportArgs, paths, sourceCleanup, sourceErr := prepareRsyncCaptureSource(ctx, task, source, policy, RsyncCaptureSourceRole)
		if sourceErr != nil {
			return 0, sourceErr
		}
		defer sourceCleanup()
		sourceOperand = operand
		args = append(args, transportArgs...)
		localTarget = target
		runtimeReadPaths = paths
	} else {
		if policy.Configured() && util.IsRemotePathSpec(target) {
			return 0, fmt.Errorf("受限 Rsync 验证不支持未绑定节点的远程目标")
		}
		if err := policy.ValidateSource(source, "rsync_verification_source"); err != nil {
			return 0, err
		}
		localSource = source
		localTarget = target
	}

	args = append(args, "--", sourceOperand, targetOperand)
	var stdout, stderr rsyncCaptureOutputBuffer
	stdout.limit = maxRsyncCaptureManifestLen
	stderr.limit = maxRsyncCaptureCommandBytes
	if err := runRsyncCaptureCommand(ctx, task, args, localSource, localTarget, trustedLocalSource, false, runtimeReadPaths, &stdout, &stderr); err != nil {
		return 0, fmt.Errorf("rsync verification comparison failed")
	}
	differences := 0
	for _, line := range strings.Split(strings.TrimRight(stdout.buf.String(), "\r\n"), "\n") {
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
