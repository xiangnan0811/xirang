package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"

	"golang.org/x/sys/unix"
)

const rsyncTreeCommandProfileVersion = 1

// RsyncTreeCommandSource is private strategy input, constructed from trusted
// Task/node facts rather than an HTTP request. The first managed-tree command
// profile supports a local source; remote transport construction is added with
// the task-execution boundary, not as a free-form source string here.
type RsyncTreeCommandSource struct {
	LocalPath string                 `json:"-"`
	Remote    *RsyncTreeRemoteSource `json:"-"`
}

type RsyncTreeHostKeyMode string

const (
	RsyncTreeHostKeyStrict    RsyncTreeHostKeyMode = "strict"
	RsyncTreeHostKeyAcceptNew RsyncTreeHostKeyMode = "accept_new"
)

// RsyncTreeSSHTransport is a typed projection of an already-authorized node
// transport. It intentionally has no raw ssh argument or option fields.
type RsyncTreeSSHTransport struct {
	Port           uint16
	HostKeyMode    RsyncTreeHostKeyMode
	KnownHostsFile string `json:"-"`
	IdentityFile   string `json:"-"`
}

// RsyncTreeRemoteSource is constructed by the task-execution boundary from a
// Node and its credential policy. Callers cannot append remote shell text.
type RsyncTreeRemoteSource struct {
	User         string
	Host         string
	Path         string `json:"-"`
	Transport    RsyncTreeSSHTransport
	UseSudoRsync bool
}

// RsyncTreeCommandInput has no extra-argument field by design. Every emitted
// argument is owned by this package's fixed allowlist.
type RsyncTreeCommandInput struct {
	Mode           backupasset.TaskPublicationMode
	Source         RsyncTreeCommandSource
	StagingTree    string `json:"-"`
	ParentTree     string `json:"-"`
	CaptureACLs    bool
	CaptureXattrs  bool
	BandwidthKibps uint64
}

type RsyncTreeCommand struct {
	Binary string   `json:"-"`
	Args   []string `json:"-"`
}

// RsyncTreePublicationInput is process-local strategy input. It carries no
// user-supplied flags, raw output, or serializable managed-root locator.
type RsyncTreePublicationInput struct {
	ManagedRoot           string `json:"-"`
	Source                RsyncTreeCommandSource
	CaptureACLs           bool
	CaptureXattrs         bool
	BandwidthKibps        uint64
	MarkerKey             []byte `json:"-"`
	SourceFingerprint     string `json:"-"`
	ChildFenceDigest      string `json:"-"`
	ManifestLimits        ManifestLimits
	MaxCommandOutputBytes int64
}

type rsyncTreePreparedPublication struct {
	input        RsyncTreePublicationInput
	command      RsyncTreeCommand
	parentBefore *rsyncTreeManifest
}

type rsyncTreeProcessResult struct {
	ExitCode      int
	ExitCodeKnown bool
}

type rsyncTreeProcessRunner interface {
	Run(context.Context, RsyncTreeCommand, int64) (rsyncTreeProcessResult, error)
}

type localRsyncTreeProcessRunner struct {
	environment func() []string
}

func newLocalRsyncTreeProcessRunner(environment func() []string) (*localRsyncTreeProcessRunner, error) {
	if environment == nil {
		return nil, fmt.Errorf("%w: managed Rsync process environment unavailable", backupasset.ErrInvalidState)
	}
	return &localRsyncTreeProcessRunner{environment: environment}, nil
}

func (runner *localRsyncTreeProcessRunner) Run(ctx context.Context, command RsyncTreeCommand, maxOutputBytes int64) (rsyncTreeProcessResult, error) {
	unknown := rsyncTreeProcessResult{ExitCode: UnknownProviderExitCode}
	if runner == nil || runner.environment == nil || strings.TrimSpace(command.Binary) == "" || maxOutputBytes <= 0 {
		return unknown, fmt.Errorf("%w: invalid managed Rsync process request", backupasset.ErrInvalidState)
	}
	if strings.ContainsRune(command.Binary, '\x00') {
		return unknown, fmt.Errorf("%w: invalid managed Rsync binary", backupasset.ErrInvalidState)
	}
	for _, argument := range command.Args {
		if strings.ContainsRune(argument, '\x00') {
			return unknown, fmt.Errorf("%w: invalid managed Rsync command operand", backupasset.ErrInvalidState)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	limiter := &rsyncTreeOutputLimiter{maximum: maxOutputBytes, cancel: cancel}
	process := exec.CommandContext(runContext, command.Binary, command.Args...)
	process.Env = SanitizeRsyncTreeEnvironment(runner.environment())
	process.Stdout = limiter
	process.Stderr = limiter
	err := process.Run()
	if limiter.Exceeded() {
		return unknown, fmt.Errorf("%w: managed Rsync output limit exceeded", backupasset.ErrCapabilityUnavailable)
	}
	if ctx.Err() != nil {
		return unknown, ctx.Err()
	}
	if err == nil {
		return rsyncTreeProcessResult{ExitCode: 0, ExitCodeKnown: true}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return rsyncTreeProcessResult{ExitCode: exitErr.ExitCode(), ExitCodeKnown: true}, nil
	}
	return unknown, err
}

type rsyncTreeOutputLimiter struct {
	maximum int64
	cancel  context.CancelFunc

	mu       sync.Mutex
	written  int64
	exceeded bool
}

func (limiter *rsyncTreeOutputLimiter) Write(value []byte) (int, error) {
	if limiter == nil || limiter.maximum <= 0 {
		return 0, fmt.Errorf("managed Rsync output limiter unavailable")
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.exceeded || int64(len(value)) > limiter.maximum-limiter.written {
		limiter.exceeded = true
		if limiter.cancel != nil {
			limiter.cancel()
		}
		return 0, fmt.Errorf("managed Rsync output limit exceeded")
	}
	limiter.written += int64(len(value))
	return len(value), nil
}

func (limiter *rsyncTreeOutputLimiter) Exceeded() bool {
	if limiter == nil {
		return false
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return limiter.exceeded
}

// SanitizeRsyncTreeEnvironment removes process variables that can alter
// rsync's argument, remote-shell, proxy, or temporary-path behavior. The
// caller supplies the inherited environment so tests and process runners can
// apply the same exact policy.
func SanitizeRsyncTreeEnvironment(base []string) []string {
	result := make([]string, 0, len(base))
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "" || strings.ContainsRune(entry, '\x00') || strings.HasPrefix(name, "RSYNC_") {
			continue
		}
		switch name {
		case "TMPDIR", "TMP", "TEMP":
			continue
		}
		result = append(result, entry)
	}
	return result
}

func BuildRsyncTreeCommand(input RsyncTreeCommandInput) (RsyncTreeCommand, error) {
	source, remote, err := input.Source.operand()
	if err != nil {
		return RsyncTreeCommand{}, err
	}
	stagingTree, err := normalizedRsyncTreeDirectory(input.StagingTree, false)
	if err != nil {
		return RsyncTreeCommand{}, err
	}
	if remote == nil && source == stagingTree {
		return RsyncTreeCommand{}, fmt.Errorf("%w: Rsync source and staging tree overlap", backupasset.ErrInvalidState)
	}
	arguments := []string{
		"--archive",
		"--checksum",
		"--hard-links",
		"--numeric-ids",
		"--fsync",
		"--protect-args",
		"--info=progress2",
		"--no-devices",
		"--no-specials",
	}
	if input.CaptureACLs {
		arguments = append(arguments, "--acls")
	}
	if input.CaptureXattrs {
		arguments = append(arguments, "--xattrs")
	}
	if input.BandwidthKibps > 0 {
		arguments = append(arguments, "--bwlimit="+strconv.FormatUint(input.BandwidthKibps, 10)+"k")
	}
	if remote != nil {
		transport, err := remote.Transport.command()
		if err != nil {
			return RsyncTreeCommand{}, err
		}
		arguments = append(arguments, "-e", transport)
		if remote.UseSudoRsync {
			arguments = append(arguments, "--rsync-path", "sudo rsync")
		}
	}
	switch input.Mode {
	case backupasset.PublicationVersionedHardlink:
		parentTree, err := normalizedRsyncTreeDirectory(input.ParentTree, false)
		if err != nil {
			return RsyncTreeCommand{}, err
		}
		arguments = append(arguments, "--link-dest="+parentTree)
	case backupasset.PublicationVersionedFullCopy:
		if input.ParentTree != "" {
			return RsyncTreeCommand{}, fmt.Errorf("%w: full-copy Rsync command has a parent tree", backupasset.ErrInvalidState)
		}
	default:
		return RsyncTreeCommand{}, fmt.Errorf("%w: invalid managed Rsync publication mode", backupasset.ErrInvalidState)
	}
	arguments = append(arguments, "--", rsyncTreeTrailingSlash(source), rsyncTreeTrailingSlash(stagingTree))
	return RsyncTreeCommand{Binary: "rsync", Args: arguments}, nil
}

func rsyncTreeTrailingSlash(value string) string {
	if strings.HasSuffix(value, string(filepath.Separator)) {
		return value
	}
	return value + string(filepath.Separator)
}

func (source RsyncTreeCommandSource) operand() (string, *RsyncTreeRemoteSource, error) {
	hasLocal := strings.TrimSpace(source.LocalPath) != ""
	if hasLocal == (source.Remote != nil) {
		return "", nil, fmt.Errorf("%w: managed Rsync command source must be exactly local or remote", backupasset.ErrInvalidState)
	}
	if hasLocal {
		localPath, err := normalizedRsyncTreeDirectory(source.LocalPath, true)
		if err != nil {
			return "", nil, err
		}
		return localPath, nil, nil
	}
	remotePath, err := normalizedRsyncTreeDirectory(source.Remote.Path, true)
	if err != nil {
		return "", nil, err
	}
	user := strings.TrimSpace(source.Remote.User)
	host := strings.TrimSpace(source.Remote.Host)
	if !validRsyncTreeRemoteUser(user) || !validRsyncTreeRemoteHost(host) {
		return "", nil, fmt.Errorf("%w: invalid managed Rsync remote source", backupasset.ErrInvalidState)
	}
	if parsedIP := net.ParseIP(host); parsedIP != nil && strings.Contains(host, ":") {
		host = "[" + parsedIP.String() + "]"
	}
	return user + "@" + host + ":" + remotePath, source.Remote, nil
}

func (transport RsyncTreeSSHTransport) command() (string, error) {
	knownHosts, err := normalizedRsyncTreeDirectory(transport.KnownHostsFile, false)
	if err != nil {
		return "", err
	}
	port := transport.Port
	if port == 0 {
		port = 22
	}
	hostKeyValue := ""
	switch transport.HostKeyMode {
	case RsyncTreeHostKeyStrict:
		hostKeyValue = "yes"
	case RsyncTreeHostKeyAcceptNew:
		hostKeyValue = "accept-new"
	default:
		return "", fmt.Errorf("%w: invalid managed Rsync host key mode", backupasset.ErrInvalidState)
	}
	arguments := []string{"ssh", "-p", strconv.FormatUint(uint64(port), 10), "-o", "StrictHostKeyChecking=" + hostKeyValue, "-o", "UserKnownHostsFile=" + knownHosts}
	if transport.IdentityFile != "" {
		identityFile, err := normalizedRsyncTreeDirectory(transport.IdentityFile, false)
		if err != nil {
			return "", err
		}
		arguments = append(arguments, "-i", identityFile)
	}
	for index := range arguments {
		arguments[index] = rsyncTreeShellQuote(arguments[index])
	}
	return strings.Join(arguments, " "), nil
}

func validRsyncTreeRemoteUser(value string) bool {
	if value == "" || len(value) > 64 || value[0] == '-' {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func validRsyncTreeRemoteHost(value string) bool {
	if value == "" || len(value) > 253 || strings.TrimSpace(value) != value || strings.ContainsRune(value, '\x00') {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func rsyncTreeShellQuote(value string) string {
	if value != "" {
		safe := true
		for _, character := range value {
			if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("_@%+=:,./-", character) {
				continue
			}
			safe = false
			break
		}
		if safe {
			return value
		}
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type rsyncTreePublicationStrategy struct {
	runner rsyncTreeProcessRunner
	now    func() time.Time
}

func NewRsyncTreePublicationStrategy(runner rsyncTreeProcessRunner, now func() time.Time) (PublicationStrategy, error) {
	if interfaceNil(runner) {
		return nil, fmt.Errorf("%w: managed Rsync strategy runner unavailable", backupasset.ErrInvalidState)
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &rsyncTreePublicationStrategy{runner: runner, now: now}, nil
}

// NewLocalRsyncTreePublicationStrategy constructs the process-local managed
// Rsync strategy used by the runtime composition root. The strategy still
// fails closed during Prepare on a platform without managed-tree primitives.
func NewLocalRsyncTreePublicationStrategy(now func() time.Time) (PublicationStrategy, error) {
	runner, err := newLocalRsyncTreeProcessRunner(os.Environ)
	if err != nil {
		return nil, err
	}
	return NewRsyncTreePublicationStrategy(runner, now)
}

func (*rsyncTreePublicationStrategy) Kind() backupasset.ProviderKind {
	return backupasset.ProviderRsync
}

func (strategy *rsyncTreePublicationStrategy) Prepare(ctx context.Context, request PublicationPrepareRequest) (PreparedPublication, error) {
	if ctx == nil || strategy == nil || interfaceNil(strategy.runner) || request.ResticInput != nil || request.RsyncTreeInput == nil {
		return PreparedPublication{}, fmt.Errorf("%w: managed Rsync prepare request unavailable", backupasset.ErrInvalidState)
	}
	if err := ctx.Err(); err != nil {
		return PreparedPublication{}, err
	}
	attempt, err := request.Attempt.RsyncTreeAttempt()
	if err != nil {
		return PreparedPublication{}, err
	}
	input, err := cloneAndValidateRsyncTreePublicationInput(*request.RsyncTreeInput)
	if err != nil {
		return PreparedPublication{}, err
	}
	tree, err := openRsyncManagedTree(input.ManagedRoot)
	if err != nil {
		return PreparedPublication{}, err
	}
	defer func() { _ = tree.Close() }()
	if err := validateRsyncTreeManagedRoot(tree, attempt); err != nil {
		return PreparedPublication{}, err
	}
	if input.Source.Remote == nil {
		if _, err := tree.validateLocalSourceRoot(input.Source.LocalPath); err != nil {
			return PreparedPublication{}, err
		}
	}
	var parentBefore *rsyncTreeManifest
	if attempt.PublicationMode == backupasset.PublicationVersionedHardlink {
		parentMarker, err := tree.readFinalMetadata(attempt.ParentRecoveryPointID, "commit.json")
		if err != nil {
			return PreparedPublication{}, err
		}
		parentCommit, err := decodeRsyncTreeCommitMarkerV1(parentMarker, input.MarkerKey)
		if err != nil {
			return PreparedPublication{}, err
		}
		if parentCommit.RepositoryID != attempt.RepositoryID || parentCommit.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
			parentCommit.RecoveryPointID != attempt.ParentRecoveryPointID || parentCommit.CommitMarkerDigest != attempt.ParentCommitDigest ||
			parentCommit.ManifestDigest != attempt.ParentManifestDigest {
			return PreparedPublication{}, fmt.Errorf("%w: managed Rsync hardlink parent commit mismatch", backupasset.ErrConflict)
		}
		parentFD, err := tree.openFinalTree(attempt.ParentRecoveryPointID)
		if err != nil {
			return PreparedPublication{}, err
		}
		manifest, manifestErr := buildRsyncTreeManifest(ctx, parentFD, input.ManifestLimits)
		closeErr := unixClose(parentFD)
		if manifestErr != nil {
			return PreparedPublication{}, manifestErr
		}
		if closeErr != nil {
			return PreparedPublication{}, closeErr
		}
		if manifest.Digest != attempt.ParentManifestDigest {
			return PreparedPublication{}, fmt.Errorf("%w: managed Rsync parent manifest changed", backupasset.ErrConflict)
		}
		parentBefore = &manifest
	}
	if err := tree.CreateFreshStagingTree(attempt.StagingComponent); err != nil {
		return PreparedPublication{}, err
	}
	attemptMarker, err := encodeRsyncTreeAttemptMarkerV1(attempt, input.MarkerKey)
	if err != nil {
		return PreparedPublication{}, err
	}
	if err := tree.WriteStagingMetadata(attempt.StagingComponent, "attempt.json", attemptMarker); err != nil {
		return PreparedPublication{}, err
	}
	stagingTree, err := tree.stagingTreePath(attempt.StagingComponent)
	if err != nil {
		return PreparedPublication{}, err
	}
	commandInput := RsyncTreeCommandInput{
		Mode: inputMode(attempt), Source: input.Source, StagingTree: stagingTree, CaptureACLs: input.CaptureACLs,
		CaptureXattrs: input.CaptureXattrs, BandwidthKibps: input.BandwidthKibps,
	}
	if attempt.PublicationMode == backupasset.PublicationVersionedHardlink {
		parentTree, err := tree.finalTreePath(attempt.ParentRecoveryPointID)
		if err != nil {
			return PreparedPublication{}, err
		}
		commandInput.ParentTree = parentTree
	}
	command, err := BuildRsyncTreeCommand(commandInput)
	if err != nil {
		return PreparedPublication{}, err
	}
	state := &rsyncTreePreparedPublication{input: input, command: command, parentBefore: parentBefore}
	return PreparedPublication{Attempt: request.Attempt, RsyncTreeInput: &input, rsyncTree: state}, nil
}

func (strategy *rsyncTreePublicationStrategy) Execute(ctx context.Context, prepared PreparedPublication, _ PublicationProgress) (ProviderExecutionResult, error) {
	attempt, state, err := strategy.prepared(prepared)
	if err != nil {
		return ProviderExecutionResult{}, err
	}
	result, err := strategy.runner.Run(ctx, state.command, state.input.MaxCommandOutputBytes)
	if err != nil || !result.ExitCodeKnown {
		return ProviderExecutionResult{ExitCode: UnknownProviderExitCode, Completion: backupasset.CompletionOutcomeUnknown}, err
	}
	if result.ExitCode != 0 {
		return ProviderExecutionResult{ExitCode: result.ExitCode, Completion: backupasset.CompletionKnownNonzero, EvidenceCode: backupasset.FailureProviderNonzeroExit}, nil
	}
	commit, err := strategy.commit(ctx, attempt, state)
	if err != nil {
		return ProviderExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, EvidenceCode: backupasset.FailureManifestUnavailable}, err
	}
	providerCommit := NewRsyncTreeProviderCommit(commit)
	if err := providerCommit.Validate(); err != nil {
		return ProviderExecutionResult{}, err
	}
	return ProviderExecutionResult{ExitCode: 0, Completion: backupasset.CompletionKnownExitZero, ProviderCommit: &providerCommit}, nil
}

func (strategy *rsyncTreePublicationStrategy) RecordCommit(_ context.Context, prepared PreparedPublication, result ProviderExecutionResult) (ProviderCommit, error) {
	attempt, _, err := strategy.prepared(prepared)
	if err != nil {
		return ProviderCommit{}, err
	}
	if result.ExitCode != 0 || result.Completion != backupasset.CompletionKnownExitZero || result.EvidenceCode != "" || result.ProviderCommit == nil {
		return ProviderCommit{}, fmt.Errorf("%w: managed Rsync execution has no commit fact", backupasset.ErrInvalidState)
	}
	commit, err := result.ProviderCommit.RsyncTreeCommit()
	if err != nil {
		return ProviderCommit{}, err
	}
	if commit.RepositoryID != attempt.RepositoryID || commit.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID || commit.RecoveryPointID != attempt.RecoveryPointID ||
		commit.AttemptID != attempt.AttemptID || commit.PublicationMode != attempt.PublicationMode || !commit.ProviderCommittedAt.Before(attempt.PointDeadlineAt) {
		return ProviderCommit{}, fmt.Errorf("%w: managed Rsync commit does not match its attempt", backupasset.ErrConflict)
	}
	return *result.ProviderCommit, nil
}

func (strategy *rsyncTreePublicationStrategy) VerifyOrBuildManifest(ctx context.Context, prepared PreparedPublication, providerCommit ProviderCommit, limits ManifestLimits) (ManifestResult, error) {
	attempt, state, err := strategy.prepared(prepared)
	if err != nil {
		return ManifestResult{}, err
	}
	commit, err := providerCommit.RsyncTreeCommit()
	if err != nil {
		return ManifestResult{}, err
	}
	if commit.RepositoryID != attempt.RepositoryID || commit.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID || commit.RecoveryPointID != attempt.RecoveryPointID || commit.AttemptID != attempt.AttemptID ||
		commit.PublicationMode != attempt.PublicationMode {
		return ManifestResult{}, fmt.Errorf("%w: managed Rsync manifest commit identity mismatch", backupasset.ErrConflict)
	}
	if !validRsyncTreeManifestLimits(limits) {
		return ManifestResult{}, fmt.Errorf("%w: invalid managed Rsync verification limits", backupasset.ErrInvalidState)
	}
	tree, err := openRsyncManagedTree(state.input.ManagedRoot)
	if err != nil {
		return ManifestResult{}, err
	}
	defer func() { _ = tree.Close() }()
	if err := validateRsyncTreeManagedRoot(tree, attempt); err != nil {
		return ManifestResult{}, err
	}
	storedMarker, err := tree.readFinalMetadata(attempt.FinalComponent, "commit.json")
	if err != nil {
		return ManifestResult{}, err
	}
	storedCommit, err := decodeRsyncTreeCommitMarkerV1(storedMarker, state.input.MarkerKey)
	if err != nil {
		return ManifestResult{}, err
	}
	if !equalRsyncTreeCommit(storedCommit, commit) {
		return ManifestResult{}, fmt.Errorf("%w: managed Rsync final commit marker mismatch", backupasset.ErrConflict)
	}
	finalFD, err := tree.openFinalTree(attempt.FinalComponent)
	if err != nil {
		return ManifestResult{}, err
	}
	manifest, manifestErr := buildRsyncTreeManifest(ctx, finalFD, limits)
	closeErr := unixClose(finalFD)
	if manifestErr != nil {
		return ManifestResult{}, manifestErr
	}
	if closeErr != nil {
		return ManifestResult{}, closeErr
	}
	if manifest.Digest != commit.ManifestDigest || manifest.EntryCount != commit.ManifestEntryCount || manifest.LogicalBytes != commit.LogicalBytes {
		return ManifestResult{}, fmt.Errorf("%w: managed Rsync final manifest changed", backupasset.ErrConflict)
	}
	if attempt.PublicationMode == backupasset.PublicationVersionedFullCopy {
		if err := validateRsyncTreeFullCopyFidelity(manifest); err != nil {
			return ManifestResult{}, err
		}
	}
	result := RsyncTreeManifestV1{DigestAlgorithm: manifest.DigestAlgorithm, Digest: manifest.Digest, EntryCount: manifest.EntryCount, LogicalBytes: manifest.LogicalBytes, FidelityDigest: commit.FidelityDigest}
	return ManifestResult{Provider: backupasset.ProviderRsync, Version: taggedPublicationSchemaV1, RsyncTree: &result}, nil
}

func (strategy *rsyncTreePublicationStrategy) Reconcile(ctx context.Context, request PublicationReconcileRequest) (PublicationReconcileResult, error) {
	if ctx == nil || strategy == nil || request.RsyncTreeInput == nil {
		return PublicationReconcileResult{}, fmt.Errorf("%w: managed Rsync reconciliation request unavailable", backupasset.ErrInvalidState)
	}
	if err := ctx.Err(); err != nil {
		return PublicationReconcileResult{}, err
	}
	attempt, err := request.Attempt.RsyncTreeAttempt()
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	input, err := cloneAndValidateRsyncTreeReconcileInput(*request.RsyncTreeInput)
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	tree, err := openRsyncManagedTree(input.ManagedRoot)
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	defer func() { _ = tree.Close() }()
	if err := validateRsyncTreeManagedRoot(tree, attempt); err != nil {
		return PublicationReconcileResult{}, err
	}
	storedAttempt, err := tree.readFinalMetadata(attempt.FinalComponent, "attempt.json")
	if errors.Is(err, unix.ENOENT) {
		finalExists, existenceErr := tree.finalComponentExists(attempt.FinalComponent)
		if existenceErr != nil {
			return PublicationReconcileResult{}, existenceErr
		}
		if finalExists {
			return PublicationReconcileResult{}, fmt.Errorf("%w: managed Rsync final directory lacks an attempt marker", backupasset.ErrConflict)
		}
		return reconcileRsyncTreeOwnedStaging(tree, attempt, input)
	}
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	markerAttempt, err := decodeRsyncTreeAttemptMarkerV1(storedAttempt, input.MarkerKey)
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	if markerAttempt != attempt {
		return PublicationReconcileResult{}, fmt.Errorf("%w: managed Rsync final attempt marker mismatch", backupasset.ErrConflict)
	}
	storedCommit, err := tree.readFinalMetadata(attempt.FinalComponent, "commit.json")
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	commit, err := decodeRsyncTreeCommitMarkerV1(storedCommit, input.MarkerKey)
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	if err := validateRsyncTreeReconcileCommit(attempt, input, commit); err != nil {
		return PublicationReconcileResult{}, err
	}
	finalFD, err := tree.openFinalTree(attempt.FinalComponent)
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	manifest, manifestErr := buildRsyncTreeManifest(ctx, finalFD, input.ManifestLimits)
	closeErr := unixClose(finalFD)
	if manifestErr != nil {
		return PublicationReconcileResult{}, manifestErr
	}
	if closeErr != nil {
		return PublicationReconcileResult{}, closeErr
	}
	if manifest.Digest != commit.ManifestDigest || manifest.EntryCount != commit.ManifestEntryCount || manifest.LogicalBytes != commit.LogicalBytes {
		return PublicationReconcileResult{}, fmt.Errorf("%w: managed Rsync final manifest changed", backupasset.ErrConflict)
	}
	if err := validateRsyncTreeReconcileFidelity(ctx, tree, attempt, input, manifest); err != nil {
		return PublicationReconcileResult{}, err
	}
	result := RsyncTreeReconcileV1{
		State:  RsyncTreeReconcileFinal,
		Commit: &commit,
		Manifest: &RsyncTreeManifestV1{
			DigestAlgorithm: manifest.DigestAlgorithm, Digest: manifest.Digest, EntryCount: manifest.EntryCount,
			LogicalBytes: manifest.LogicalBytes, FidelityDigest: commit.FidelityDigest,
		},
	}
	if err := result.Validate(); err != nil {
		return PublicationReconcileResult{}, err
	}
	return PublicationReconcileResult{RsyncTree: &result}, nil
}

func reconcileRsyncTreeOwnedStaging(tree *rsyncManagedTree, attempt RsyncTreeAttemptV1, input RsyncTreeReconcileInput) (PublicationReconcileResult, error) {
	storedAttempt, err := tree.readStagingMetadata(attempt.StagingComponent, "attempt.json")
	if errors.Is(err, unix.ENOENT) {
		return PublicationReconcileResult{RsyncTree: &RsyncTreeReconcileV1{State: RsyncTreeReconcileAbsent}}, nil
	}
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	markerAttempt, err := decodeRsyncTreeAttemptMarkerV1(storedAttempt, input.MarkerKey)
	if err != nil {
		return PublicationReconcileResult{}, err
	}
	if markerAttempt != attempt {
		return PublicationReconcileResult{}, fmt.Errorf("%w: managed Rsync staging attempt marker mismatch", backupasset.ErrConflict)
	}
	return PublicationReconcileResult{RsyncTree: &RsyncTreeReconcileV1{State: RsyncTreeReconcileStaging}}, nil
}

func cloneAndValidateRsyncTreeReconcileInput(input RsyncTreeReconcileInput) (RsyncTreeReconcileInput, error) {
	if _, err := normalizeRsyncManagedRoot(input.ManagedRoot); err != nil || !validRsyncTreeMarkerKey(input.MarkerKey) ||
		!validRsyncTreeDigest(input.SourceFingerprint) || !validRsyncTreeDigest(input.ChildFenceDigest) ||
		!validRsyncTreeManifestLimits(input.ManifestLimits) || input.ManifestLimits.MaxBytes > maxRsyncManagedTreeMetadataBytes {
		return RsyncTreeReconcileInput{}, fmt.Errorf("%w: invalid managed Rsync reconciliation input", backupasset.ErrInvalidState)
	}
	input.MarkerKey = append([]byte(nil), input.MarkerKey...)
	return input, nil
}

func validateRsyncTreeReconcileCommit(attempt RsyncTreeAttemptV1, input RsyncTreeReconcileInput, commit RsyncTreeCommitV1) error {
	if err := commit.Validate(); err != nil {
		return err
	}
	if commit.RepositoryID != attempt.RepositoryID || commit.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
		commit.RecoveryPointID != attempt.RecoveryPointID || commit.AttemptID != attempt.AttemptID || commit.PublicationMode != attempt.PublicationMode ||
		!commit.PointDeadlineAt.Equal(attempt.PointDeadlineAt.UTC()) || !commit.ProviderCommittedAt.Before(attempt.PointDeadlineAt.UTC()) ||
		commit.SourceFingerprint != input.SourceFingerprint || commit.ChildFenceDigest != input.ChildFenceDigest {
		return fmt.Errorf("%w: managed Rsync final commit identity mismatch", backupasset.ErrConflict)
	}
	if attempt.PublicationMode == backupasset.PublicationVersionedHardlink &&
		(commit.ParentRecoveryPointID != attempt.ParentRecoveryPointID || commit.ParentCommitDigest != attempt.ParentCommitDigest) {
		return fmt.Errorf("%w: managed Rsync final commit parent mismatch", backupasset.ErrConflict)
	}
	return nil
}

func validateRsyncTreeReconcileFidelity(ctx context.Context, tree *rsyncManagedTree, attempt RsyncTreeAttemptV1, input RsyncTreeReconcileInput, manifest rsyncTreeManifest) error {
	switch attempt.PublicationMode {
	case backupasset.PublicationVersionedFullCopy:
		return validateRsyncTreeFullCopyFidelity(manifest)
	case backupasset.PublicationVersionedHardlink:
		parentMarker, err := tree.readFinalMetadata(attempt.ParentRecoveryPointID, "commit.json")
		if err != nil {
			return err
		}
		parentCommit, err := decodeRsyncTreeCommitMarkerV1(parentMarker, input.MarkerKey)
		if err != nil {
			return err
		}
		if parentCommit.RepositoryID != attempt.RepositoryID || parentCommit.TaskRepositoryLinkID != attempt.TaskRepositoryLinkID ||
			parentCommit.RecoveryPointID != attempt.ParentRecoveryPointID || parentCommit.CommitMarkerDigest != attempt.ParentCommitDigest ||
			parentCommit.ManifestDigest != attempt.ParentManifestDigest {
			return fmt.Errorf("%w: managed Rsync hardlink parent commit mismatch", backupasset.ErrConflict)
		}
		parentFD, err := tree.openFinalTree(attempt.ParentRecoveryPointID)
		if err != nil {
			return err
		}
		parentManifest, manifestErr := buildRsyncTreeManifest(ctx, parentFD, input.ManifestLimits)
		closeErr := unixClose(parentFD)
		if manifestErr != nil {
			return manifestErr
		}
		if closeErr != nil {
			return closeErr
		}
		if parentManifest.Digest != attempt.ParentManifestDigest {
			return fmt.Errorf("%w: managed Rsync hardlink parent manifest changed", backupasset.ErrConflict)
		}
		return validateRsyncTreeHardlinkFidelity(parentManifest, parentManifest, manifest)
	default:
		return fmt.Errorf("%w: invalid managed Rsync reconciliation mode", backupasset.ErrInvalidState)
	}
}

func (strategy *rsyncTreePublicationStrategy) prepared(prepared PreparedPublication) (RsyncTreeAttemptV1, *rsyncTreePreparedPublication, error) {
	if strategy == nil || interfaceNil(strategy.runner) || prepared.ResticInput != nil || prepared.RsyncTreeInput == nil || prepared.rsyncTree == nil {
		return RsyncTreeAttemptV1{}, nil, fmt.Errorf("%w: managed Rsync prepared publication unavailable", backupasset.ErrInvalidState)
	}
	attempt, err := prepared.Attempt.RsyncTreeAttempt()
	if err != nil {
		return RsyncTreeAttemptV1{}, nil, err
	}
	if !equalRsyncTreePublicationInput(*prepared.RsyncTreeInput, prepared.rsyncTree.input) {
		return RsyncTreeAttemptV1{}, nil, fmt.Errorf("%w: managed Rsync prepared input drift", backupasset.ErrInvalidState)
	}
	return attempt, prepared.rsyncTree, nil
}

func (strategy *rsyncTreePublicationStrategy) commit(ctx context.Context, attempt RsyncTreeAttemptV1, state *rsyncTreePreparedPublication) (RsyncTreeCommitV1, error) {
	if err := ctx.Err(); err != nil {
		return RsyncTreeCommitV1{}, err
	}
	now := strategy.now().UTC()
	if now.IsZero() || !now.Before(attempt.PointDeadlineAt) {
		return RsyncTreeCommitV1{}, fmt.Errorf("%w: managed Rsync point deadline elapsed", backupasset.ErrInvalidState)
	}
	tree, err := openRsyncManagedTree(state.input.ManagedRoot)
	if err != nil {
		return RsyncTreeCommitV1{}, err
	}
	defer func() { _ = tree.Close() }()
	if err := validateRsyncTreeManagedRoot(tree, attempt); err != nil {
		return RsyncTreeCommitV1{}, err
	}
	stagingFD, err := tree.openStagingTree(attempt.StagingComponent)
	if err != nil {
		return RsyncTreeCommitV1{}, err
	}
	manifest, manifestErr := buildRsyncTreeManifest(ctx, stagingFD, state.input.ManifestLimits)
	closeErr := unixClose(stagingFD)
	if manifestErr != nil {
		return RsyncTreeCommitV1{}, manifestErr
	}
	if closeErr != nil {
		return RsyncTreeCommitV1{}, closeErr
	}
	if attempt.PublicationMode == backupasset.PublicationVersionedFullCopy {
		if err := validateRsyncTreeFullCopyFidelity(manifest); err != nil {
			return RsyncTreeCommitV1{}, err
		}
	} else {
		if state.parentBefore == nil {
			return RsyncTreeCommitV1{}, fmt.Errorf("%w: managed Rsync hardlink parent evidence missing", backupasset.ErrInvalidState)
		}
		parentFD, err := tree.openFinalTree(attempt.ParentRecoveryPointID)
		if err != nil {
			return RsyncTreeCommitV1{}, err
		}
		parentAfter, parentErr := buildRsyncTreeManifest(ctx, parentFD, state.input.ManifestLimits)
		parentCloseErr := unixClose(parentFD)
		if parentErr != nil {
			return RsyncTreeCommitV1{}, parentErr
		}
		if parentCloseErr != nil {
			return RsyncTreeCommitV1{}, parentCloseErr
		}
		if err := validateRsyncTreeHardlinkFidelity(*state.parentBefore, parentAfter, manifest); err != nil {
			return RsyncTreeCommitV1{}, err
		}
	}
	if err := tree.FsyncStagingTree(attempt.StagingComponent); err != nil {
		return RsyncTreeCommitV1{}, err
	}
	if err := tree.WriteStagingMetadata(attempt.StagingComponent, "manifest.jsonl", manifest.Encoded); err != nil {
		return RsyncTreeCommitV1{}, err
	}
	commit := RsyncTreeCommitV1{
		LayoutVersion: taggedPublicationSchemaV1, RepositoryID: attempt.RepositoryID, TaskRepositoryLinkID: attempt.TaskRepositoryLinkID,
		RecoveryPointID: attempt.RecoveryPointID, AttemptID: attempt.AttemptID, PublicationMode: attempt.PublicationMode,
		ManifestDigestAlgorithm: manifest.DigestAlgorithm, ManifestDigest: manifest.Digest, ManifestEntryCount: manifest.EntryCount, LogicalBytes: manifest.LogicalBytes,
		FidelityDigest: rsyncTreeFidelityDigest(attempt, state.input, manifest), SourceFingerprint: state.input.SourceFingerprint,
		ProviderCommittedAt: now, ChildFenceDigest: state.input.ChildFenceDigest, PointDeadlineAt: attempt.PointDeadlineAt,
		RenameVerified: true, DirectoryFsyncVerified: true,
	}
	if attempt.PublicationMode == backupasset.PublicationVersionedHardlink {
		commit.ParentRecoveryPointID = attempt.ParentRecoveryPointID
		commit.ParentCommitDigest = attempt.ParentCommitDigest
	}
	commit, commitMarker, err := encodeRsyncTreeCommitMarkerV1(commit, state.input.MarkerKey)
	if err != nil {
		return RsyncTreeCommitV1{}, err
	}
	if err := tree.WriteStagingMetadata(attempt.StagingComponent, "commit.json", commitMarker); err != nil {
		return RsyncTreeCommitV1{}, err
	}
	if err := tree.FsyncStagingTree(attempt.StagingComponent); err != nil {
		return RsyncTreeCommitV1{}, err
	}
	if err := tree.CommitStaging(attempt.StagingComponent, attempt.FinalComponent); err != nil {
		return RsyncTreeCommitV1{}, err
	}
	return commit, nil
}

func cloneAndValidateRsyncTreePublicationInput(input RsyncTreePublicationInput) (RsyncTreePublicationInput, error) {
	if _, err := normalizeRsyncManagedRoot(input.ManagedRoot); err != nil || !validRsyncTreeMarkerKey(input.MarkerKey) || !validRsyncTreeDigest(input.SourceFingerprint) ||
		!validRsyncTreeDigest(input.ChildFenceDigest) || !validRsyncTreeManifestLimits(input.ManifestLimits) || input.ManifestLimits.MaxBytes > maxRsyncManagedTreeMetadataBytes || input.MaxCommandOutputBytes <= 0 {
		return RsyncTreePublicationInput{}, fmt.Errorf("%w: invalid managed Rsync publication input", backupasset.ErrInvalidState)
	}
	if input.Source.Remote != nil {
		remote := *input.Source.Remote
		input.Source.Remote = &remote
	}
	input.MarkerKey = append([]byte(nil), input.MarkerKey...)
	return input, nil
}

func equalRsyncTreePublicationInput(left, right RsyncTreePublicationInput) bool {
	if left.ManagedRoot != right.ManagedRoot || left.Source.LocalPath != right.Source.LocalPath || left.CaptureACLs != right.CaptureACLs || left.CaptureXattrs != right.CaptureXattrs ||
		left.BandwidthKibps != right.BandwidthKibps || left.SourceFingerprint != right.SourceFingerprint || left.ChildFenceDigest != right.ChildFenceDigest ||
		left.ManifestLimits != right.ManifestLimits || left.MaxCommandOutputBytes != right.MaxCommandOutputBytes || !bytes.Equal(left.MarkerKey, right.MarkerKey) {
		return false
	}
	if left.Source.Remote == nil || right.Source.Remote == nil {
		return left.Source.Remote == right.Source.Remote
	}
	return *left.Source.Remote == *right.Source.Remote
}

func validateRsyncTreeManagedRoot(tree *rsyncManagedTree, attempt RsyncTreeAttemptV1) error {
	if err := tree.VerifyRootIdentity(); err != nil {
		return err
	}
	marker, err := tree.readRepositoryMarker()
	if err != nil {
		return err
	}
	if markerDigest := rsyncTreeDigest(marker); markerDigest != attempt.RepositoryMarkerDigest || tree.identityDigest(markerDigest) != attempt.ManagedRootIdentityDigest {
		return fmt.Errorf("%w: managed Rsync root identity changed", backupasset.ErrConflict)
	}
	return nil
}

func rsyncTreeFidelityDigest(attempt RsyncTreeAttemptV1, input RsyncTreePublicationInput, manifest rsyncTreeManifest) string {
	return rsyncTreeDigest([]byte(strings.Join([]string{
		"rsync-tree-fidelity-v1", string(attempt.PublicationMode), manifest.Digest, strconv.FormatBool(input.CaptureACLs), strconv.FormatBool(input.CaptureXattrs),
		strconv.FormatBool(true), strconv.FormatBool(true),
	}, "\n")))
}

func equalRsyncTreeCommit(left, right RsyncTreeCommitV1) bool {
	return left.LayoutVersion == right.LayoutVersion && left.RepositoryID == right.RepositoryID && left.TaskRepositoryLinkID == right.TaskRepositoryLinkID &&
		left.RecoveryPointID == right.RecoveryPointID && left.AttemptID == right.AttemptID && left.PublicationMode == right.PublicationMode &&
		left.ParentRecoveryPointID == right.ParentRecoveryPointID && left.ParentCommitDigest == right.ParentCommitDigest &&
		left.ManifestDigestAlgorithm == right.ManifestDigestAlgorithm && left.ManifestDigest == right.ManifestDigest &&
		left.ManifestEntryCount == right.ManifestEntryCount && left.LogicalBytes == right.LogicalBytes && left.FidelityDigest == right.FidelityDigest &&
		left.SourceFingerprint == right.SourceFingerprint && left.ProviderCommittedAt.Equal(right.ProviderCommittedAt) && left.CommitMarkerDigest == right.CommitMarkerDigest &&
		left.ChildFenceDigest == right.ChildFenceDigest && left.PointDeadlineAt.Equal(right.PointDeadlineAt) && left.RenameVerified == right.RenameVerified &&
		left.DirectoryFsyncVerified == right.DirectoryFsyncVerified && left.FailureCode == right.FailureCode
}

func inputMode(attempt RsyncTreeAttemptV1) backupasset.TaskPublicationMode {
	return attempt.PublicationMode
}

func unixClose(fd int) error {
	if err := unix.Close(fd); err != nil {
		return rsyncManagedTreeSystemError(err)
	}
	return nil
}

func normalizedRsyncTreeDirectory(value string, allowRoot bool) (string, error) {
	if strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("%w: invalid managed Rsync directory", backupasset.ErrInvalidState)
	}
	clean := filepath.Clean(strings.TrimSpace(value))
	if !filepath.IsAbs(clean) || clean == "." || (!allowRoot && clean == string(filepath.Separator)) {
		return "", fmt.Errorf("%w: invalid managed Rsync directory", backupasset.ErrInvalidState)
	}
	return clean, nil
}
