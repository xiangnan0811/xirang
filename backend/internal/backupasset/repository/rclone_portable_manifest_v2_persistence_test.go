package repository

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

const (
	portablePersistenceSourceTime    = "2026-07-16T01:00:01.123456Z"
	portablePersistenceTargetTime    = "2026-07-16T01:00:01Z"
	portablePersistenceHelloSHA      = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	portablePersistenceSourceListing = `[{"Path":"file.txt","Name":"file.txt","Size":5,"ModTime":"2026-07-16T01:00:01.123456Z","IsDir":false,"Hashes":{"sha256":"2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},"Metadata":{"mode":"100640","uid":"1000","gid":"1000","mtime":"2026-07-16T01:00:01.123456Z"}}]`
	portablePersistenceTargetListing = `[{"Path":"file.txt","Name":"file.txt","Size":5,"ModTime":"2026-07-16T01:00:01Z","IsDir":false}]`
)

type portablePersistenceHistory struct {
	DomainKey          string `json:"synthetic_recovery_cleanup_domain_key"`
	MarkerKey          string `json:"synthetic_marker_key_base64"`
	RepositoryIdentity string `json:"repository_identity"`
	SourceFingerprint  string `json:"source_fingerprint"`
	TaggedAttempt      string `json:"tagged_attempt"`
	TaggedCommit       string `json:"tagged_commit"`
}

type portablePersistenceObject struct {
	body     []byte
	modified time.Time
}

type portablePersistenceAttempt struct {
	controls      map[string][]byte
	writes        []string
	present       bool
	transferred   bool
	targetListing []byte
	object        portablePersistenceObject
	objectPath    string
}

// Only the external Rclone endpoint is substituted. Observations are made by
// the production V1 builder, and controls are written/read by the real strategy.
type portablePersistenceRemote struct {
	mu              sync.Mutex
	attempts        map[string]*portablePersistenceAttempt
	objects         map[string]portablePersistenceObject
	readRequests    map[string]int
	downloadChecks  int
	preflightChecks int
}

func newPortablePersistenceRemote() *portablePersistenceRemote {
	return &portablePersistenceRemote{attempts: make(map[string]*portablePersistenceAttempt), objects: make(map[string]portablePersistenceObject), readRequests: make(map[string]int)}
}

func (remote *portablePersistenceRemote) checkRead(ctx context.Context, request provider.RclonePortablePublicationRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.ReadDeadline.IsZero() {
		if !request.Attempt.PointDeadlineAt.After(time.Now().UTC()) {
			return context.DeadlineExceeded
		}
		return nil
	}
	now := time.Now().UTC()
	if !request.ReadDeadline.After(now) || request.ReadDeadline.After(now.Add(request.ManifestOptions.Limits.Timeout)) ||
		request.Source != (provider.RclonePrivateLocator{}) || request.CopyDest != nil || request.ParentLeaseEligible ||
		request.Manifest.Version != 0 || request.Manifest.EntryCount != 0 || len(request.Manifest.Chunks) != 0 || len(request.Manifest.IndexEncoded) != 0 {
		return errors.New("invalid bounded historical read request")
	}
	if deadline, ok := ctx.Deadline(); ok && request.ReadDeadline.After(deadline) {
		return errors.New("historical read exceeds caller deadline")
	}
	remote.mu.Lock()
	remote.readRequests[request.Attempt.AttemptID]++
	remote.mu.Unlock()
	return nil
}

func (remote *portablePersistenceRemote) ProbeAttempt(ctx context.Context, request provider.RclonePortablePublicationRequest) (provider.RcloneAttemptPresence, error) {
	if err := remote.checkRead(ctx, request); err != nil || !request.ReadDeadline.IsZero() {
		return provider.RcloneAttemptPresenceUnknown, errors.New("invalid publication probe")
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if state := remote.attempts[request.Attempt.AttemptID]; state != nil && state.present {
		return provider.RcloneAttemptPresent, nil
	}
	remote.attempts[request.Attempt.AttemptID] = &portablePersistenceAttempt{controls: make(map[string][]byte)}
	return provider.RcloneAttemptAbsent, nil
}

func (remote *portablePersistenceRemote) Observe(ctx context.Context, request provider.RclonePortablePublicationRequest, locator provider.RclonePrivateLocator) (provider.RcloneManifestBundle, error) {
	if err := remote.checkRead(ctx, request); err != nil {
		return provider.RcloneManifestBundle{}, err
	}
	var listing []byte
	if !request.ReadDeadline.IsZero() && locator != request.DataRoot {
		return provider.RcloneManifestBundle{}, errors.New("historical observation was not restricted to committed data")
	}
	if !request.ReadDeadline.IsZero() || locator == request.DataRoot {
		remote.mu.Lock()
		state := remote.attempts[request.Attempt.AttemptID]
		if state != nil && state.transferred {
			listing = append([]byte(nil), state.targetListing...)
		}
		remote.mu.Unlock()
		if listing == nil {
			return provider.RcloneManifestBundle{}, errors.New("target data absent")
		}
	} else if locator == request.Source && locator != (provider.RclonePrivateLocator{}) {
		listing = []byte(portablePersistenceSourceListing)
	} else {
		return provider.RcloneManifestBundle{}, errors.New("unexpected observation locator")
	}
	return provider.BuildRcloneManifestV1(ctx, bytes.NewReader(listing), request.ManifestOptions)
}

func (remote *portablePersistenceRemote) PutControl(ctx context.Context, request provider.RclonePortablePublicationRequest, name string, payload []byte) error {
	if err := remote.checkRead(ctx, request); err != nil || !request.ReadDeadline.IsZero() || int64(len(payload)) > request.ControlPayloadMaxBytes {
		return errors.New("invalid control write")
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	state := remote.attempts[request.Attempt.AttemptID]
	if state == nil || state.controls[name] != nil {
		return errors.New("control collision")
	}
	state.present = true
	state.controls[name] = append([]byte(nil), payload...)
	state.writes = append(state.writes, name)
	return nil
}

func (remote *portablePersistenceRemote) ReadControl(ctx context.Context, request provider.RclonePortablePublicationRequest, name string, maxBytes int64) ([]byte, error) {
	if err := remote.checkRead(ctx, request); err != nil {
		return nil, err
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	state := remote.attempts[request.Attempt.AttemptID]
	if state == nil || state.controls[name] == nil || int64(len(state.controls[name])) > maxBytes {
		return nil, errors.New("control absent or exceeds limit")
	}
	return append([]byte(nil), state.controls[name]...), nil
}

func (remote *portablePersistenceRemote) TransferData(ctx context.Context, request provider.RclonePortablePublicationRequest, copyDest *provider.RclonePrivateLocator) error {
	if err := remote.checkRead(ctx, request); err != nil || !request.ReadDeadline.IsZero() || copyDest != nil {
		return errors.New("invalid test data transfer")
	}
	modified, err := time.Parse(time.RFC3339Nano, portablePersistenceTargetTime)
	if err != nil {
		return err
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	state := remote.attempts[request.Attempt.AttemptID]
	if state == nil || !state.present || state.transferred {
		return errors.New("unexpected transfer")
	}
	state.transferred = true
	state.targetListing = []byte(portablePersistenceTargetListing)
	state.object = portablePersistenceObject{body: []byte("hello"), modified: modified}
	state.objectPath = "backup:managed/v1/points/" + request.Attempt.Portable.AttemptComponent + "/data/file.txt"
	remote.objects[state.objectPath] = state.object
	return nil
}

func (remote *portablePersistenceRemote) VerifyFullBytes(ctx context.Context, request provider.RclonePortablePublicationRequest, expected uint64) (provider.RclonePortableByteVerification, error) {
	if err := remote.checkRead(ctx, request); err != nil || !request.ReadDeadline.IsZero() {
		return provider.RclonePortableByteVerification{}, errors.New("invalid download verification")
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	state := remote.attempts[request.Attempt.AttemptID]
	if state == nil || !state.transferred || !bytes.Equal(state.object.body, []byte("hello")) || expected != uint64(len(state.object.body)) {
		return provider.RclonePortableByteVerification{}, errors.New("source and destination bytes differ")
	}
	remote.downloadChecks++
	return provider.RclonePortableByteVerification{Complete: true, VerifiedBytes: expected}, nil
}

// The preflight command boundary models a bounded canary and computes its
// evidence from actual bytes and observations; renewal itself remains the
// production preflighter and binding workflow.
func (remote *portablePersistenceRemote) PreflightPortable(ctx context.Context, request provider.RclonePortableCommandPreflightRequest) (provider.RclonePortableCommandPreflightResult, error) {
	if err := ctx.Err(); err != nil {
		return provider.RclonePortableCommandPreflightResult{}, err
	}
	root, err := provider.NewRclonePrivateLocator("backup:managed/v1")
	if err != nil || request.ManagedRoot != root || request.BoundConfig.KeyedDigest() == "" || request.Runtime.Node.ID != 9 ||
		request.ControlPayloadMaxBytes < 64 || request.FullVerifyMaxBytes < 64 || !request.AbsoluteDeadline.After(time.Now().UTC()) {
		return provider.RclonePortableCommandPreflightResult{}, errors.New("invalid bounded canary request")
	}
	source := bytes.Repeat([]byte{0x42}, 64)
	destination := append([]byte(nil), source...)
	if !bytes.Equal(source, destination) {
		return provider.RclonePortableCommandPreflightResult{}, errors.New("canary bytes differ")
	}
	sum := sha256.Sum256(source)
	listing := fmt.Sprintf(`[{"Path":"canary.bin","Name":"canary.bin","Size":64,"ModTime":"2026-07-16T00:00:00Z","IsDir":false,"Hashes":{"sha256":"%x"}}]`, sum)
	observation, err := provider.BuildRcloneManifestV1(ctx, strings.NewReader(listing), request.ManifestOptions)
	if err != nil || observation.EntryCount != 1 || observation.LogicalBytes != 64 {
		return provider.RclonePortableCommandPreflightResult{}, errors.New("invalid bounded canary observation")
	}
	rootDigest := digestText("xirang-rclone-preflight-portable-root-v1\n" + request.BoundConfig.KeyedDigest() + "\nbackup:managed/v1")
	markerDigest := digestText("xirang-rclone-preflight-marker-v1\n" + string(source))
	evidenceDigest := digestText("xirang-rclone-preflight-portable-evidence-v1\n" + rootDigest + "\n" + markerDigest + "\n" + observation.ObservationDigest + "\n" + observation.ObservationDigest)
	remote.mu.Lock()
	remote.preflightChecks++
	remote.mu.Unlock()
	return provider.RclonePortableCommandPreflightResult{ManagedRootIdentityDigest: rootDigest, RepositoryMarkerDigest: markerDigest, EvidenceDigest: evidenceDigest, VerifiedBytes: uint64(len(source))}, nil
}

func (*portablePersistenceRemote) WriteNativeCanary(context.Context, provider.RcloneNativeCommandPreflightRequest) (provider.RcloneNativeCommandPreflightResult, error) {
	return provider.RcloneNativeCommandPreflightResult{}, errors.New("unexpected native preflight")
}

// The adapter remains real; this transport supplies only the exact external
// stat and byte stream for an object previously transferred or loaded as V1.
func (remote *portablePersistenceRemote) Run(ctx context.Context, invocation provider.CommandInvocation, limits provider.OperationLimits) (provider.CommandOutput, error) {
	if err := ctx.Err(); err != nil {
		return provider.CommandOutput{}, err
	}
	remote.mu.Lock()
	object, ok := remote.objects[invocation.PrivateLocator]
	remote.mu.Unlock()
	if !ok || invocation.Operation != provider.OperationRcloneStat {
		return provider.CommandOutput{}, errors.New("unexpected external stat")
	}
	name := filepath.Base(invocation.PrivateLocator)
	payload, err := json.Marshal(struct {
		Path    string    `json:"Path"`
		Name    string    `json:"Name"`
		Size    int       `json:"Size"`
		ModTime time.Time `json:"ModTime"`
		IsDir   bool      `json:"IsDir"`
	}{Path: name, Name: name, Size: len(object.body), ModTime: object.modified})
	if err != nil || int64(len(payload)) > limits.MaxMetadataBytes {
		return provider.CommandOutput{}, errors.New("invalid bounded external stat")
	}
	return provider.CommandOutput{Stdout: payload}, nil
}

func (remote *portablePersistenceRemote) Open(ctx context.Context, invocation provider.CommandInvocation, _ provider.OperationLimits, maxBytes int64) (provider.ReadHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	remote.mu.Lock()
	object, ok := remote.objects[invocation.PrivateLocator]
	remote.mu.Unlock()
	if !ok || invocation.Operation != provider.OperationRcloneCat || maxBytes < int64(len(object.body)) {
		return nil, errors.New("unexpected external content read")
	}
	return &portablePersistenceReadHandle{reader: bytes.NewReader(object.body)}, nil
}

type portablePersistenceReadHandle struct {
	reader *bytes.Reader
	read   int64
}

func (handle *portablePersistenceReadHandle) Read(p []byte) (int, error) {
	n, err := handle.reader.Read(p)
	handle.read += int64(n)
	return n, err
}
func (*portablePersistenceReadHandle) Close() error                { return nil }
func (handle *portablePersistenceReadHandle) ProviderBytes() int64 { return handle.read }

type portablePersistenceUnusedNative struct{}

func (portablePersistenceUnusedNative) ObserveSource(context.Context, provider.RcloneNativePublicationRequest) (provider.RcloneManifestBundle, error) {
	return provider.RcloneManifestBundle{}, errors.New("unexpected native observation")
}
func (portablePersistenceUnusedNative) Sync(context.Context, provider.RcloneNativePublicationRequest) error {
	return errors.New("unexpected native sync")
}
func (portablePersistenceUnusedNative) VerifyFullBytes(context.Context, provider.RcloneNativePublicationRequest, uint64) (provider.RcloneFullByteProof, error) {
	return provider.RcloneFullByteProof{}, errors.New("unexpected native verification")
}

func TestRclonePortableManifestV2Persistence(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		runPortableManifestV2Persistence(t, "sqlite")
	})
	t.Run("postgres", func(t *testing.T) {
		runPortableManifestV2Persistence(t, "postgres")
	})
}

func runPortableManifestV2Persistence(t *testing.T, engine string) {
	t.Helper()
	ctx := context.Background()
	db := openProducerRetentionIntegrationDB(t, engine)
	// The unchanged historical control fixture is bound to Task 7 and Node 9.
	for range 6 {
		seedTask(t, db, "rclone", "backup:unused", "")
	}
	for i := range 2 {
		node := model.Node{Name: fmt.Sprintf("portable-v1-unused-%d", i), Host: "example.invalid", Port: 22, Username: "reader", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY", BasePath: "/data", BackupDir: fmt.Sprintf("unused-%d", i)}
		if err := db.Create(&node).Error; err != nil {
			t.Fatal(err)
		}
	}
	fixture := newRclonePublicationFixtureWithDB(t, backupasset.PublicationVersionedPrefix, db)
	if fixture.task.ID != 7 || fixture.task.NodeID != 9 {
		t.Fatalf("fixed historical fixture requires Task 7/Node 9, got %d/%d", fixture.task.ID, fixture.task.NodeID)
	}
	var history portablePersistenceHistory
	fixtureDir := filepath.Join("..", "provider", "testdata", "rclone", "portable-v1")
	metadata, err := os.ReadFile(filepath.Join(fixtureDir, "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(metadata, &history); err != nil {
		t.Fatal(err)
	}
	oldAttempt, err := provider.DecodeRcloneAttemptV1(history.TaggedAttempt)
	if err != nil {
		t.Fatal(err)
	}
	oldCommit, err := provider.DecodeRcloneCommitV1(history.TaggedCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !oldAttempt.PointDeadlineAt.Before(time.Now().UTC()) {
		t.Fatal("fixed V1 fixture must have a genuinely expired publication deadline")
	}
	installPortablePersistenceHistoryIdentity(t, fixture, history, oldAttempt)
	remote := newPortablePersistenceRemote()
	installPortablePersistenceV1Controls(t, remote, fixtureDir, oldAttempt)
	clock := fixture.now
	strategy, err := provider.NewRclonePublicationStrategy(
		provider.NewRclonePortablePublisher(remote, func(time.Duration) {}, func() time.Time { return clock }),
		provider.NewRcloneNativePublisher(portablePersistenceUnusedNative{}, func() time.Time { return clock }),
	)
	if err != nil {
		t.Fatal(err)
	}
	limits := provider.OperationLimits{Timeout: time.Minute, MaxMetadataBytes: 1 << 20, MaxStderrBytes: 1 << 16, MaxRecordBytes: 1 << 16, MaxItems: 100}
	adapter, err := provider.NewRcloneAdapter(remote, provider.NewCursorCodec(fixture.service.keyring, func() time.Time { return clock }, time.Hour), limits, 100, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRclone, provider.Registration{Prober: &scriptedProber{}, EntryStatter: adapter, SequentialReader: adapter, RangeReader: adapter, CatalogReader: strategy, PublicationStrategy: strategy}); err != nil {
		t.Fatal(err)
	}
	publicationService, service, lease := newPortablePersistenceServices(t, fixture, registry, &clock)
	if _, err := fixture.service.keyring.Ensure(ctx, backupasset.KeyDomainEntryIdentity); err != nil {
		t.Fatal(err)
	}

	oldPoint, oldManifest := seedPortablePersistenceV1Point(t, fixture, history, oldAttempt, oldCommit)
	oldGeneration, oldEntry := buildPortablePersistenceCatalog(t, fixture, service, lease, oldPoint, &clock, "dir/file.txt")
	assertPortablePersistenceContent(t, service, oldPoint, oldGeneration, oldEntry, "2026-07-16T01:00:01Z")
	remote.mu.Lock()
	oldReadCount := remote.readRequests[oldAttempt.AttemptID]
	remote.mu.Unlock()
	if oldReadCount == 0 {
		t.Fatal("expired V1 point was not read with a bounded historical request")
	}
	// A preflight near expiry makes this a short-lived, but otherwise ordinary,
	// publication. Its real signed deadline is never changed after Prepare.
	setPortablePersistenceBindingExpiry(t, fixture, time.Now().UTC().Add(time.Minute).Truncate(time.Second))

	execution, err := publicationService.Prepare(ctx, fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) })
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if attempt.MinimumRuntimeRevision != 2 || attempt.ManifestSchemaRevision != 2 {
		t.Fatalf("new portable attempt=%+v", attempt)
	}
	if !attempt.PointDeadlineAt.Equal(fixture.binding.PreflightExpiresAt) {
		t.Fatalf("publication deadline=%v, want existing binding expiry %v", attempt.PointDeadlineAt, fixture.binding.PreflightExpiresAt)
	}
	prepared, result := executePortablePersistencePublication(t, strategy, execution)
	commit, err := result.ProviderCommit.RcloneCommit()
	if err != nil {
		t.Fatal(err)
	}
	if commit.SourceObservationDigest == commit.DestinationObservationDigest || commit.Portable.DownloadVerifiedBytes != 5 {
		t.Fatalf("source/destination or byte evidence wrong: %+v", commit)
	}
	if _, err := strategy.VerifyOrBuildManifest(ctx, prepared, *result.ProviderCommit, prepared.RcloneInput.ManifestLimits); err != nil {
		t.Fatalf("verify actual V2 controls: %v", err)
	}
	remote.mu.Lock()
	state := remote.attempts[attempt.AttemptID]
	index := append([]byte(nil), state.controls["manifest-index.json"]...)
	downloadChecks := remote.downloadChecks
	writes := append([]string(nil), state.writes...)
	remote.mu.Unlock()
	var indexFacts struct {
		Version      int    `json:"version"`
		Source       string `json:"source_observation_digest"`
		Destination  string `json:"destination_observation_digest"`
		ContentProof struct {
			Complete      bool   `json:"complete"`
			VerifiedBytes uint64 `json:"verified_bytes"`
		} `json:"content_proof"`
	}
	if err := json.Unmarshal(index, &indexFacts); err != nil || indexFacts.Version != 2 || indexFacts.Source != commit.SourceObservationDigest ||
		indexFacts.Destination != commit.DestinationObservationDigest || !indexFacts.ContentProof.Complete || indexFacts.ContentProof.VerifiedBytes != 5 || downloadChecks != 1 {
		t.Fatalf("unexpected actual V2 index: %+v checks=%d err=%v", indexFacts, downloadChecks, err)
	}
	if len(writes) < 3 || writes[0] != "attempt.json" || writes[len(writes)-1] != "commit.json" {
		t.Fatalf("portable control writes were not commit-last: %v", writes)
	}
	if err := execution.Abandon(backupasset.ErrPublicationSessionAbandoned); err != nil {
		t.Fatal(err)
	}
	expirePortablePersistencePublicationLease(t, db, attempt.RecoveryPointID, clock)
	// Reconstruct the publication service before either durable transition: no
	// prepared source bundle survives the restart.
	publicationService, service, lease = newPortablePersistenceServices(t, fixture, registry, &clock)
	if outcome, err := publicationService.ProcessPoint(ctx, attempt.RecoveryPointID); err != nil || outcome.State != backupasset.RecoveryPointVerifying {
		t.Fatalf("restart preparing reconciliation=%+v err=%v", outcome, err)
	}
	var verifying model.RecoveryPoint
	if err := db.First(&verifying, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if verifying.PhysicalAvailability != string(backupasset.PhysicalUnknown) || verifying.CommittedAt != nil || verifying.ObservedAt != nil {
		t.Fatalf("unverified V2 point=%+v", verifying)
	}
	clock = clock.Add(time.Second)
	if outcome, err := publicationService.ProcessPoint(ctx, attempt.RecoveryPointID); err != nil || outcome.State != backupasset.RecoveryPointCommitted {
		t.Fatalf("restart manifest reconciliation=%+v err=%v", outcome, err)
	}
	var point model.RecoveryPoint
	var manifest model.RecoveryPointManifest
	if err := db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("recovery_point_id = ? AND is_active = ?", point.ID, true).First(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	if manifest.GeneratorVersion != "2" || manifest.Digest != commit.ManifestIndexDigest || point.State != string(backupasset.RecoveryPointCommitted) ||
		point.PhysicalAvailability != string(backupasset.PhysicalOnline) || point.ObservedAt != nil ||
		point.CapturedAt == nil || !point.CapturedAt.Equal(commit.ProviderCommittedAt) ||
		point.CommittedAt == nil || !point.CommittedAt.Equal(clock) {
		t.Fatalf("persisted V2 point=%+v manifest=%+v", point, manifest)
	}
	generation, entry := buildPortablePersistenceCatalog(t, fixture, service, lease, point, &clock, "file.txt")
	assertPortablePersistenceContent(t, service, point, generation, entry, portablePersistenceSourceTime)
	// Use a real wall-clock wait, not an application-clock jump, so the
	// historical reader must replace the expired operation deadline.
	remaining := time.Until(attempt.PointDeadlineAt) + 200*time.Millisecond
	if remaining > 0 {
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-t.Context().Done():
			t.Fatal("test context ended before the original publication deadline")
		}
	}
	if !attempt.PointDeadlineAt.Before(time.Now().UTC()) {
		t.Fatal("V2 publication deadline did not pass in real time")
	}
	remote.mu.Lock()
	beforeHistoricalRead := remote.readRequests[attempt.AttemptID]
	remote.mu.Unlock()

	if err := db.Model(&model.TaskRun{}).Where("id = ?", fixture.taskRun.ID).Updates(map[string]any{"status": "success", "finished_at": clock}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", fixture.task.ID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err := db.Preload("Node").First(&task, fixture.task.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(task)
	if err != nil {
		t.Fatal(err)
	}
	clock = fixture.binding.PreflightExpiresAt.Add(time.Second)
	if _, err := publicationService.loadExactManagedRclonePublicationRuntime(ctx, task.ID); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("expired binding unexpectedly executable: %v", err)
	}
	admission := &rsyncVersioningManagedAdmission{publicationAdmission: &publicationAdmission{mode: publication.AdmissionManaged, generation: 1}}
	renewal, err := NewService(Dependencies{DB: db, Foundation: fixture.service.foundation, Registry: registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return clock }, Admission: admission, History: fixture.service.history, Metrics: publication.NoopMetrics{}, Publication: publicationService})
	if err != nil {
		t.Fatal(err)
	}
	preflighter, err := NewProductionRcloneVersioningPreflighter(RcloneProductionPreflightDependencies{
		CommandPlane: remote, IdentityKey: []byte(history.DomainKey), Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	renewal.rclonePreflighter = preflighter
	ticket, err := renewal.CreateRcloneVersioningPreflight(ctx, backupasset.RcloneVersioningPreflightRequest{TaskID: task.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix})
	if err != nil || ticket.Purpose != backupasset.RclonePreflightPurposeRenewal {
		t.Fatalf("renewal ticket=%+v err=%v", ticket, err)
	}
	if _, err := renewal.ActivateRcloneVersioning(ctx, backupasset.RcloneVersioningActivationRequest{TaskID: task.ID, ExpectedTaskRevision: revision, PreflightID: ticket.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint}); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	preflightChecks := remote.preflightChecks
	remote.mu.Unlock()
	if preflightChecks != 1 {
		t.Fatalf("renewal preflight checks=%d", preflightChecks)
	}
	publicationService, service, _ = newPortablePersistenceServices(t, fixture, registry, &clock)
	var after model.RecoveryPoint
	var afterManifest model.RecoveryPointManifest
	var afterEntry model.CatalogEntry
	if err := db.First(&after, "id = ?", point.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&afterManifest, "id = ?", manifest.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("generation_id = ? AND entry_id = ?", generation.ID, entry.EntryID).First(&afterEntry).Error; err != nil {
		t.Fatal(err)
	}
	locator, err := decodeManagedRclonePointLocator(after.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	persistedAttempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	lineageAfter, lineageErr := backupasset.DecodePublicationLineage(after.LineageJSON)
	if err != nil || lineageErr != nil || !persistedAttempt.PointDeadlineAt.Equal(attempt.PointDeadlineAt) ||
		!lineageAfter.PointDeadlineAt.Equal(attempt.PointDeadlineAt) || after.SourceFingerprint != point.SourceFingerprint ||
		after.EncryptedProviderLocator != point.EncryptedProviderLocator || afterManifest.ID != manifest.ID ||
		afterManifest.Digest != manifest.Digest || afterManifest.EncryptedCommitEvidence != manifest.EncryptedCommitEvidence ||
		afterEntry.EncryptedProviderLocator != entry.EncryptedProviderLocator {
		t.Fatalf("renewal changed historical point, locator, manifest, or deadline: %v", err)
	}
	// Reopen the actual authenticated Catalog reader after the real deadline
	// without superseding the generation whose locator is being compared.
	catalogSession, err := service.OpenCatalogRead(ctx, CatalogPointReadRequest{RepositoryID: after.RepositoryID, RecoveryPointID: after.ID})
	if err != nil {
		t.Fatalf("reopen expired V2 Catalog: %v", err)
	}
	page, err := catalogSession.ListCanonical(ctx, provider.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ModifiedAt == nil ||
		page.Items[0].ModifiedAt.Format(time.RFC3339Nano) != portablePersistenceSourceTime {
		_ = catalogSession.Close()
		t.Fatalf("expired V2 Catalog source projection=%+v err=%v", page.Items, err)
	}
	if _, err := catalogSession.Finalize(ctx); err != nil {
		_ = catalogSession.Close()
		t.Fatalf("finalize expired V2 Catalog: %v", err)
	}
	if err := catalogSession.Close(); err != nil {
		t.Fatal(err)
	}
	assertPortablePersistenceContent(t, service, after, generation, afterEntry, portablePersistenceSourceTime)
	remote.mu.Lock()
	afterHistoricalRead := remote.readRequests[attempt.AttemptID]
	remote.mu.Unlock()
	if afterHistoricalRead <= beforeHistoricalRead {
		t.Fatal("expired V2 point did not use a bounded historical read request")
	}
	var oldAfter model.RecoveryPoint
	var oldAfterManifest model.RecoveryPointManifest
	if err := db.First(&oldAfter, "id = ?", oldPoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&oldAfterManifest, "id = ?", oldManifest.ID).Error; err != nil {
		t.Fatal(err)
	}
	if oldAfter.EncryptedProviderLocator != oldPoint.EncryptedProviderLocator || oldAfterManifest.EncryptedCommitEvidence != oldManifest.EncryptedCommitEvidence ||
		oldAfter.PhysicalAvailability != string(backupasset.PhysicalUnknown) || oldAfter.ObservedAt != nil {
		t.Fatal("renewal changed the fixed V1 point or manifest")
	}
	assertPortablePersistenceContent(t, service, oldAfter, oldGeneration, oldEntry, "2026-07-16T01:00:01Z")
	assertPortablePersistenceTargetTimeDriftRejected(t, service, remote, after, generation, afterEntry, attempt)

	assertMalformedPortablePersistenceVersionRejected(t, fixture, publicationService, strategy, remote, clock)
}

func setPortablePersistenceBindingExpiry(t *testing.T, fixture *rclonePublicationFixture, expires time.Time) {
	t.Helper()
	fixture.binding.PreflightExpiresAt = expires
	payload, err := encodeManagedRcloneBindingDocumentV3(fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	var access model.RepositoryAccessBinding
	if err := fixture.db.Where("repository_id = ? AND status = ?", fixture.repository.ID, bindingStatusActive).First(&access).Error; err != nil {
		t.Fatal(err)
	}
	salt, err := hex.DecodeString(fixture.binding.IdentitySalt)
	if err != nil {
		t.Fatal(err)
	}
	access.EncryptedConfig = payload
	access.ConfigFingerprint = managedRcloneBindingDigest(salt, "active-binding", payload)
	if err := fixture.db.Save(&access).Error; err != nil {
		t.Fatal(err)
	}
}

func installPortablePersistenceHistoryIdentity(t *testing.T, fixture *rclonePublicationFixture, history portablePersistenceHistory, attempt provider.RcloneAttemptV1) {
	t.Helper()
	wrapped, err := secure.WrapDomainKey(string(backupasset.KeyDomainRecoveryCleanupOwnership), 1, []byte(history.DomainKey))
	if err != nil {
		t.Fatal(err)
	}
	row := model.WrappedDomainKey{ID: strings.Repeat("9", 32), Domain: string(backupasset.KeyDomainRecoveryCleanupOwnership), Version: 1,
		State: string(backupasset.DomainKeyActive), WrappedKey: wrapped.Envelope, WrapAlgorithm: wrapped.Algorithm,
		WrappingKeyFingerprint: wrapped.KEKFingerprint, ActivatedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now}
	if err := fixture.db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	fixture.binding.PreflightID = attempt.PreflightID
	fixture.binding.PreflightDigest = attempt.PreflightDigest
	fixture.binding.ManagedRootIdentityDigest = attempt.ManagedRootIdentityDigest
	identity, err := managedRcloneRepositoryIdentity(fixture.binding)
	if err != nil || identity != history.RepositoryIdentity || digestText(identity) != attempt.RepositoryIdentityDigest {
		t.Fatalf("fixed V1 repository identity mismatch: %v", err)
	}
	fixture.repository.RepositoryIdentity = &identity
	if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", fixture.repository.ID).Update("repository_identity", identity).Error; err != nil {
		t.Fatal(err)
	}
	payload, err := encodeManagedRcloneBindingDocumentV3(fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	var access model.RepositoryAccessBinding
	if err := fixture.db.Where("repository_id = ? AND status = ?", fixture.repository.ID, bindingStatusActive).First(&access).Error; err != nil {
		t.Fatal(err)
	}
	salt, err := hex.DecodeString(fixture.binding.IdentitySalt)
	if err != nil {
		t.Fatal(err)
	}
	access.EncryptedConfig = payload
	access.ConfigFingerprint = managedRcloneBindingDigest(salt, "active-binding", payload)
	if err := fixture.db.Save(&access).Error; err != nil {
		t.Fatal(err)
	}
	key, err := fixture.service.rcloneMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := base64.StdEncoding.DecodeString(history.MarkerKey)
	if err != nil || !bytes.Equal(key, want) {
		t.Fatalf("fixed V1 marker key mismatch: %v", err)
	}
}

func installPortablePersistenceV1Controls(t *testing.T, remote *portablePersistenceRemote, dir string, attempt provider.RcloneAttemptV1) {
	t.Helper()
	listing, err := os.ReadFile(filepath.Join(dir, "source-lsjson.json"))
	if err != nil {
		t.Fatal(err)
	}
	controls := make(map[string][]byte)
	for _, name := range []string{"attempt.json", "commit.json", "manifest-index.json", "manifest-000000.jsonl", "manifest-000001.jsonl"} {
		controls[name], err = os.ReadFile(filepath.Join(dir, "control", name))
		if err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(filepath.Join(dir, "data", "dir", "file.txt"))
	if err != nil || string(body) != "hello" {
		t.Fatalf("fixed V1 payload=%q err=%v", body, err)
	}
	modified, err := time.Parse(time.RFC3339Nano, "2026-07-16T01:00:01Z")
	if err != nil {
		t.Fatal(err)
	}
	objectPath := "backup:managed/v1/points/" + attempt.Portable.AttemptComponent + "/data/dir/file.txt"
	object := portablePersistenceObject{body: body, modified: modified}
	remote.attempts[attempt.AttemptID] = &portablePersistenceAttempt{controls: controls, present: true, transferred: true, targetListing: listing, object: object, objectPath: objectPath}
	remote.objects[objectPath] = object
}

func seedPortablePersistenceV1Point(t *testing.T, fixture *rclonePublicationFixture, history portablePersistenceHistory, attempt provider.RcloneAttemptV1, commit provider.RcloneCommitV1) (model.RecoveryPoint, model.RecoveryPointManifest) {
	t.Helper()
	started := attempt.CaptureStartedAt
	finished := commit.ProviderCommittedAt
	run := model.TaskRun{ID: attempt.TaskRunID, TaskID: fixture.task.ID, NodeIDSnapshot: fixture.task.NodeID, TriggerType: attempt.Trigger,
		Status: "success", StartedAt: &started, FinishedAt: &finished, CreatedAt: started, UpdatedAt: finished}
	if err := fixture.db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	lineage, err := rclonePublicationLineageForRun(fixture.link, fixture.task, run, attempt.Trigger, "", started, attempt.PreparedAt, attempt.PointDeadlineAt)
	if err != nil {
		t.Fatal(err)
	}
	lineageJSON, err := backupasset.EncodePublicationLineage(lineage)
	if err != nil {
		t.Fatal(err)
	}
	key, err := fixture.service.rcloneMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	locatorJSON, locator, err := encodeManagedRclonePointLocator(attempt, fixture.binding, key, commit)
	if err != nil || locator.PhysicalIdentityDigest != history.SourceFingerprint {
		t.Fatalf("fixed V1 point identity mismatch: %v", err)
	}
	consistency, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{Version: 1, Provider: backupasset.ProviderRclone,
		RepositoryIdentityDigest: attempt.RepositoryIdentityDigest, ProviderCommitDigest: locator.ProviderCommitDigest, CapabilityRevision: int(attempt.CapabilityRevision)})
	if err != nil {
		t.Fatal(err)
	}
	fidelity, err := encodeManagedRcloneFidelity(commit.FidelityEvidenceDigest)
	if err != nil {
		t.Fatal(err)
	}
	point := model.RecoveryPoint{ID: attempt.RecoveryPointID, RepositoryID: fixture.repository.ID, ProducingTaskID: &fixture.task.ID, ProducingTaskRunID: &run.ID,
		ProducingTaskNameSnapshot: fixture.task.Name, ProducingNodeIDSnapshot: fixture.task.NodeID, ProducingNodeNameSnapshot: fixture.task.Node.Name,
		LineageJSON: lineageJSON, EncryptedProviderLocator: locatorJSON, Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointCommitted),
		CapturedAt: &finished, CommittedAt: &finished, SourceFingerprint: locator.PhysicalIdentityDigest, ManifestDigestAlgorithm: "sha256", ManifestDigest: commit.ManifestIndexDigest,
		EntryCount: int64(commit.ManifestEntryCount), LogicalBytes: int64(commit.LogicalBytes), ConsistencyJSON: consistency, FidelityJSON: fidelity,
		CapabilityRevision: int(attempt.CapabilityRevision), CapabilitiesJSON: fixture.repository.CapabilitiesJSON,
		ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), PhysicalAvailability: string(backupasset.PhysicalUnknown), HoldState: string(backupasset.HoldNone),
		CreatedAt: started, UpdatedAt: finished}
	if err := fixture.db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	manifest := model.RecoveryPointManifest{ID: strings.Repeat("8", 32), RecoveryPointID: point.ID, Revision: 1, DigestAlgorithm: "sha256", Digest: commit.ManifestIndexDigest,
		Generator: "xirang-rclone", GeneratorVersion: "1", Completeness: string(backupasset.ManifestComplete), EntryCount: point.EntryCount, LogicalBytes: point.LogicalBytes,
		FidelityJSON: fidelity, EncryptedCommitEvidence: locator.TaggedCommit, IsActive: true, CreatedAt: finished, UpdatedAt: finished}
	if err := fixture.db.Create(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	// GORM's encryption hook mutates the inserted instance; reload the plaintext
	// view used by the production readers and later identity comparisons.
	if err := fixture.db.First(&point, "id = ?", point.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&manifest, "id = ?", manifest.ID).Error; err != nil {
		t.Fatal(err)
	}
	return point, manifest
}

func newPortablePersistenceServices(t *testing.T, fixture *rclonePublicationFixture, registry *provider.Registry, clock *time.Time) (*PublicationService, *Service, *backupasset.LeaseService) {
	t.Helper()
	now := func() time.Time { return *clock }
	leaseConfig, err := fixture.service.foundation.LeaseConfig()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := backupasset.NewLeaseService(fixture.db, now, leaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	admission := &publicationAdmission{mode: publication.AdmissionManaged, generation: 1}
	publisher, err := NewPublicationService(PublicationDependencies{DB: fixture.db, Foundation: fixture.service.foundation, Registry: registry,
		Keyring: fixture.service.keyring, Lease: lease, Admission: admission, Metrics: publication.NoopMetrics{}, Audit: &auditSpy{}, History: fixture.service.history, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{DB: fixture.db, Foundation: fixture.service.foundation, Registry: registry, Keyring: fixture.service.keyring,
		Now: now, Admission: admission, History: fixture.service.history, Metrics: publication.NoopMetrics{}, Publication: publisher})
	if err != nil {
		t.Fatal(err)
	}
	return publisher, service, lease
}

func executePortablePersistencePublication(t *testing.T, strategy *provider.RclonePublicationStrategy, execution publication.Execution) (provider.PreparedPublication, provider.ProviderExecutionResult) {
	t.Helper()
	input, err := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	}).RclonePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	input.PortableRequest.Source, err = provider.NewRclonePrivateLocator("/source")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := strategy.Prepare(execution.Context(), provider.PublicationPrepareRequest{Attempt: *execution.Attempt(), RcloneInput: &input})
	if err != nil {
		t.Fatalf("prepare real portable strategy: %v", err)
	}
	result, err := strategy.Execute(execution.Context(), prepared, provider.PublicationProgress{})
	if err != nil || result.ProviderCommit == nil || result.Completion != backupasset.CompletionKnownExitZero {
		t.Fatalf("execute real portable strategy: result=%+v err=%v", result, err)
	}
	if _, err := strategy.RecordCommit(execution.Context(), prepared, result); err != nil {
		t.Fatal(err)
	}
	return prepared, result
}

func expirePortablePersistencePublicationLease(t *testing.T, db *gorm.DB, pointID string, now time.Time) {
	t.Helper()
	if err := db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", pointID, backupasset.LeaseActive).
		Updates(map[string]any{"status": backupasset.LeaseExpired, "lease_expires_at": now.Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
}

func buildPortablePersistenceCatalog(t *testing.T, fixture *rclonePublicationFixture, service *Service, lease *backupasset.LeaseService, point model.RecoveryPoint, clock *time.Time, file string) (model.CatalogGeneration, model.CatalogEntry) {
	t.Helper()
	indexer, err := catalog.NewIndexer(catalog.IndexerDependencies{DB: fixture.db, Factory: service, Lease: lease, IdentityKeys: fixture.service.keyring,
		Now: func() time.Time { return *clock }, Config: catalog.IndexerConfig{BatchSize: 10, BuildTimeout: time.Minute, MaxEntries: 100, HeartbeatInterval: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := indexer.Build(context.Background(), catalog.BuildRequest{RepositoryID: point.RepositoryID, RecoveryPointID: point.ID, CorrelationID: "rclone-v2-persistence"})
	if err != nil || generation.State != string(catalog.GenerationComplete) || !generation.IsActive {
		t.Fatalf("real Catalog build=%+v err=%v", generation, err)
	}
	var entry model.CatalogEntry
	if err := fixture.db.Where("generation_id = ? AND normalized_path = ?", generation.ID, file).First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.Fingerprint != portablePersistenceHelloSHA || entry.FingerprintStrength != "strong" || entry.Size != 5 {
		t.Fatalf("Catalog source projection=%+v", entry)
	}
	return generation, entry
}

func assertPortablePersistenceContent(t *testing.T, service *Service, point model.RecoveryPoint, generation model.CatalogGeneration, entry model.CatalogEntry, wantTime string) {
	t.Helper()
	modified, err := time.Parse(time.RFC3339Nano, wantTime)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ModifiedAt == nil || !entry.ModifiedAt.Equal(modified) {
		t.Fatalf("Catalog logical time=%v want=%v", entry.ModifiedAt, modified)
	}
	session, err := service.OpenContentSource(context.Background(), content.SourceRequest{Ref: backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: entry.EntryID},
		CatalogGenerationID: generation.ID, ExpectedSource: point.SourceFingerprint, ExpectedEntry: entry.Fingerprint, Mode: content.SourceModeSequential, MaxBytes: entry.Size})
	if err != nil {
		t.Fatalf("open real Portable content: %v", err)
	}
	defer func() { _ = session.Close() }()
	if session.Stat().ModifiedAt == nil || !session.Stat().ModifiedAt.Equal(modified) {
		t.Fatalf("content logical time=%v want=%v", session.Stat().ModifiedAt, modified)
	}
	body, readErr := io.ReadAll(session.Reader())
	closeErr := session.Close()
	if readErr != nil || closeErr != nil || string(body) != "hello" {
		t.Fatalf("Portable content=%q read=%v close=%v", body, readErr, closeErr)
	}
}

func assertPortablePersistenceTargetTimeDriftRejected(t *testing.T, service *Service, remote *portablePersistenceRemote, point model.RecoveryPoint, generation model.CatalogGeneration, entry model.CatalogEntry, attempt provider.RcloneAttemptV1) {
	t.Helper()
	request := content.SourceRequest{Ref: backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: entry.EntryID},
		CatalogGenerationID: generation.ID, ExpectedSource: point.SourceFingerprint, ExpectedEntry: entry.Fingerprint,
		Mode: content.SourceModeSequential, MaxBytes: entry.Size}
	remote.mu.Lock()
	state := remote.attempts[attempt.AttemptID]
	path := state.objectPath
	original := remote.objects[path]
	changed := original
	changed.modified = changed.modified.Add(time.Second)
	remote.objects[path] = changed
	remote.mu.Unlock()
	session, err := service.OpenContentSource(context.Background(), request)
	remote.mu.Lock()
	remote.objects[path] = original
	remote.mu.Unlock()
	if session != nil {
		_ = session.Close()
		t.Fatal("same-size target mtime drift returned a content session")
	}
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("same-size target mtime drift error=%v", err)
	}

	remote.mu.Lock()
	listing := state.targetListing
	state.targetListing = bytes.Replace(listing, []byte(portablePersistenceTargetTime), []byte("2026-07-16T01:00:02Z"), 1)
	remote.mu.Unlock()
	session, err = service.OpenContentSource(context.Background(), request)
	remote.mu.Lock()
	state.targetListing = listing
	remote.mu.Unlock()
	if session != nil {
		_ = session.Close()
		t.Fatal("recursive target drift returned a content session")
	}
	if !errors.Is(err, provider.ErrRcloneDestinationUnstable) {
		t.Fatalf("recursive target drift error=%v", err)
	}

	remote.mu.Lock()
	marker := state.controls["commit.json"]
	state.controls["commit.json"] = append([]byte(nil), marker...)
	state.controls["commit.json"][len(marker)-1] ^= 1
	remote.mu.Unlock()
	session, err = service.OpenContentSource(context.Background(), request)
	remote.mu.Lock()
	state.controls["commit.json"] = marker
	remote.mu.Unlock()
	if session != nil {
		_ = session.Close()
		t.Fatal("changed commit marker returned a content session")
	}
	if !errors.Is(err, provider.ErrRcloneMarkerMismatch) {
		t.Fatalf("changed commit marker error=%v", err)
	}
}

func assertMalformedPortablePersistenceVersionRejected(t *testing.T, fixture *rclonePublicationFixture, publisher *PublicationService, strategy *provider.RclonePublicationStrategy, remote *portablePersistenceRemote, clock time.Time) {
	t.Helper()
	ctx := context.Background()
	var task model.Task
	if err := fixture.db.Preload("Node").First(&task, fixture.task.ID).Error; err != nil {
		t.Fatal(err)
	}
	run := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: task.NodeID, TriggerType: "manual", Status: "running", StartedAt: &clock, CreatedAt: clock, UpdatedAt: clock}
	if err := fixture.db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	execution, err := publisher.Prepare(ctx, publication.Run{Task: task, TaskRunID: run.ID, Trigger: "manual", StartedAt: clock, Audit: fixture.run().Audit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) })
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	prepared, result := executePortablePersistencePublication(t, strategy, execution)
	if _, err := strategy.VerifyOrBuildManifest(ctx, prepared, *result.ProviderCommit, prepared.RcloneInput.ManifestLimits); err != nil {
		t.Fatal(err)
	}
	key := append([]byte(nil), prepared.RcloneInput.PortableRequest.MarkerKey...)
	remote.mu.Lock()
	state := remote.attempts[attempt.AttemptID]
	index := bytes.Replace(state.controls["manifest-index.json"], []byte(`"version":2`), []byte(`"version":99`), 1)
	if bytes.Equal(index, state.controls["manifest-index.json"]) {
		remote.mu.Unlock()
		t.Fatal("V2 index had no version to corrupt")
	}
	var envelope struct {
		Version        int             `json:"version"`
		Kind           string          `json:"kind"`
		Document       json.RawMessage `json:"document"`
		Authentication string          `json:"authentication"`
	}
	if err := json.Unmarshal(state.controls["commit.json"], &envelope); err != nil {
		remote.mu.Unlock()
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Document, &document); err != nil {
		remote.mu.Unlock()
		t.Fatal(err)
	}
	sum := sha256.Sum256(index)
	document["manifest_index_digest"], err = json.Marshal(hex.EncodeToString(sum[:]))
	if err != nil {
		remote.mu.Unlock()
		t.Fatal(err)
	}
	envelope.Document, err = json.Marshal(document)
	if err != nil {
		remote.mu.Unlock()
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, "xirang-rclone-control-v1\ncommit\n")
	_, _ = mac.Write(envelope.Document)
	envelope.Authentication = hex.EncodeToString(mac.Sum(nil))
	marker, err := json.Marshal(envelope)
	if err != nil {
		remote.mu.Unlock()
		t.Fatal(err)
	}
	state.controls["manifest-index.json"] = index
	state.controls["commit.json"] = marker
	remote.mu.Unlock()
	if err := execution.Abandon(backupasset.ErrPublicationSessionAbandoned); err != nil {
		t.Fatal(err)
	}
	expirePortablePersistencePublicationLease(t, fixture.db, attempt.RecoveryPointID, clock)
	_, reconcileErr := publisher.ProcessPoint(ctx, attempt.RecoveryPointID)
	if !errors.Is(reconcileErr, provider.ErrRcloneManifestObservationMismatch) {
		t.Fatalf("authenticated unknown manifest version error=%v", reconcileErr)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := fixture.db.Model(&model.RecoveryPointManifest{}).Where("recovery_point_id = ?", point.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointPreparing) || point.PhysicalAvailability != string(backupasset.PhysicalUnknown) ||
		point.CommittedAt != nil || point.ObservedAt != nil || count != 0 {
		t.Fatalf("malformed version changed publication availability: point=%+v manifests=%d", point, count)
	}
}
