package export

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"
)

func TestPersistentWorkerPreservesCompositeRootsAndMixedArchiveReport(t *testing.T) {
	first := frozenItemFixture()
	first.LogicalSize = 5
	first.ArchiveComponents = []string{"Readme.txt"}
	second := first
	second.Ref.RecoveryPointID = strings.Repeat("d", 32)
	second.Ref.EntryID = strings.Repeat("e", 64)
	second.CatalogGenerationID = strings.Repeat("f", 32)
	second.SourceFingerprint = "second-source-fingerprint-v1"
	second.EntryFingerprint = "second-entry-fingerprint-v1"
	second.ArchiveComponents = []string{"README.TXT"}
	second.SelectionRootOrdinal = 1
	directory := persistentNonFileFixture(backupasset.CatalogEntryDirectory, strings.Repeat("b", 64), "directory-entry")
	directory.ArchiveComponents = []string{"folder"}
	link := persistentNonFileFixture(backupasset.CatalogEntrySymlink, strings.Repeat("c", 64), "link-entry")
	link.ArchiveComponents = []string{"link"}

	fixture := createPersistentReadSpoolFixtureWithSelection(t, newWorkerServiceHarness(t), []FrozenItem{first, second, directory, link})
	defer fixture.snapshot.ClearKeyMaterial()
	byRef := make(map[backupasset.AssetRef]PersistentAttemptItem)
	for _, item := range fixture.snapshot.Items {
		byRef[item.Frozen.Ref] = item
	}
	if len(byRef) != 4 || fixture.file.Frozen.Ref != first.Ref || byRef[first.Ref].Frozen.SelectionRootOrdinal != 0 ||
		byRef[second.Ref].Frozen.SelectionRootOrdinal != 1 {
		t.Fatalf("persisted selection identities=%+v", fixture.snapshot.Items)
	}
	budget, err := NewAttemptBudgetService(fixture.harness.db, func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatal(err)
	}
	source := &persistentSourceResolverFake{payload: []byte("other"), providerBytes: 5}
	broker, err := content.NewAttemptBroker(source, budget, func() time.Time { return fixture.clock })
	if err != nil {
		t.Fatal(err)
	}
	fixture.worker.broker = broker
	if _, err := fixture.worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken, ItemID: byRef[second.Ref].ItemID,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.worker.loader.Load(context.Background(), PersistentAttemptLoadRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID, FenceToken: fixture.fenceToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.ClearKeyMaterial()
	for _, item := range snapshot.Items {
		if item.Frozen.EntryType == backupasset.CatalogEntryFile && (item.State != ItemRead || item.SpoolLocator == "") {
			t.Fatalf("file not spooled: %+v", item)
		}
	}
	sealed, err := fixture.worker.SealArchive(context.Background(), persistentSealRequest(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Report.ResultKind != ResultPartial || sealed.Report.Packed != 3 || sealed.Report.Skipped != 1 ||
		sealed.Report.Failed != 0 || sealed.Report.LogicalBytes != 10 || sealed.Report.ProviderBytes != 10 || len(sealed.Report.Items) != 4 {
		t.Fatalf("mixed report=%+v", sealed.Report)
	}
	wantReports := map[string]ArchiveItemReport{
		byRef[first.Ref].ItemID:     {ItemID: byRef[first.Ref].ItemID, MemberPath: "rp-11111111/root-0/Readme.txt", State: ItemPacked, LogicalBytes: 5, ProviderBytes: 5},
		byRef[second.Ref].ItemID:    {ItemID: byRef[second.Ref].ItemID, MemberPath: "rp-dddddddd/root-1/README.TXT", State: ItemPacked, LogicalBytes: 5, ProviderBytes: 5},
		byRef[directory.Ref].ItemID: {ItemID: byRef[directory.Ref].ItemID, MemberPath: "folder/", State: ItemPacked},
		byRef[link.Ref].ItemID:      {ItemID: byRef[link.Ref].ItemID, MemberPath: "link", State: ItemSkipped, ErrorCategory: ItemErrorLinkMetadataUnavailable},
	}
	for _, item := range sealed.Report.Items {
		if want, ok := wantReports[item.ItemID]; !ok || item != want {
			t.Fatalf("mixed report item=%+v want=%+v", item, want)
		}
		delete(wantReports, item.ItemID)
	}
	if len(wantReports) != 0 || len(source.requests) != 3 {
		t.Fatalf("missing report items=%+v source requests=%+v", wantReports, source.requests)
	}
	metadata := fixture.worker.metadata.(*metadataValidatorFake)
	seenMetadata := make(map[backupasset.AssetRef]bool)
	for _, item := range metadata.items {
		seenMetadata[item.Ref] = true
	}
	if !seenMetadata[directory.Ref] || !seenMetadata[link.Ref] {
		t.Fatalf("non-file metadata validation=%+v", metadata.items)
	}

	encrypted, err := fixture.worker.store.OpenSealed(sealed.Locator)
	if err != nil {
		t.Fatal(err)
	}
	var plaintext bytes.Buffer
	_, decryptErr := DecryptStream(context.Background(), &plaintext, encrypted, snapshot.DEK, CipherBinding{
		ExportID: snapshot.JobID, SelectionDigest: snapshot.SelectionDigest, ArchiveProfile: snapshot.ArchiveProfile,
		FormatVersion: 1, AttemptFenceDigest: snapshot.AttemptFenceDigest, Purpose: CipherPurposeFinalArchive,
	})
	if closeErr := encrypted.Close(); decryptErr != nil || closeErr != nil {
		t.Fatal(errors.Join(decryptErr, closeErr))
	}
	_, embedded := readArchivePathMembers(t, ArchiveZIP, plaintext.Bytes())
	if !reflect.DeepEqual(embedded, sealed.Report) {
		t.Fatalf("embedded report=%+v sealed=%+v", embedded, sealed.Report)
	}
	archive, err := zip.NewReader(bytes.NewReader(plaintext.Bytes()), int64(plaintext.Len()))
	if err != nil {
		t.Fatal(err)
	}
	wantMembers := map[string]string{
		"folder/":                       "",
		"rp-11111111/root-0/Readme.txt": "sssss",
		"rp-dddddddd/root-1/README.TXT": "other",
	}
	if len(archive.File) != len(wantMembers)+1 {
		t.Fatalf("archive member count=%d", len(archive.File))
	}
	for _, member := range archive.File {
		if member.Name == archiveReportName {
			continue
		}
		want, ok := wantMembers[member.Name]
		if !ok || member.FileInfo().IsDir() != (member.Name == "folder/") {
			t.Fatalf("unexpected archive member=%s", member.Name)
		}
		reader, err := member.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || string(data) != want {
			t.Fatalf("member=%s bytes=%q err=%v", member.Name, data, errors.Join(readErr, closeErr))
		}
		delete(wantMembers, member.Name)
	}
	if len(wantMembers) != 0 {
		t.Fatalf("missing members=%v", wantMembers)
	}
}

func TestPersistentWorkerRejectsExpiredJobBeforeOpeningSource(t *testing.T) {
	harness := newWorkerServiceHarness(t)
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(t, harness, 41, "expired-job-spool-admission")
	var job model.BackupAssetExportJob
	if err := harness.db.First(&job, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	clock := job.AbsoluteDeadline.UTC()
	budget, err := NewAttemptBudgetService(harness.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	source := &persistentSourceResolverFake{payload: bytes.Repeat([]byte("s"), int(frozenItemFixture().LogicalSize)), providerBytes: frozenItemFixture().LogicalSize}
	inner, err := content.NewAttemptBroker(source, budget, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	broker := &recordingAttemptBroker{inner: inner}
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "expired-spool")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	worker, err := NewPersistentWorker(PersistentWorkerDependencies{
		DB: harness.db, Keys: backupasset.NewKeyring(harness.db, func() time.Time { return clock }),
		Broker: broker, Metadata: &metadataValidatorFake{}, Store: store, AttemptWork: NewAttemptWorkRegistry(),
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotExportStoreTree(t, store.root)
	_, err = worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{
		JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemAttempt.ItemID,
	})
	if !errors.Is(err, ErrAttemptFenceLost) || len(broker.bindings) != 0 || len(source.requests) != 0 {
		t.Fatalf("expired job spool error=%v bindings=%d source requests=%d", err, len(broker.bindings), len(source.requests))
	}
	assertExportStoreTreeUnchanged(t, store.root, before)
}
