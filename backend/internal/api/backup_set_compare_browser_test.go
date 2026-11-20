package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

// TestBackupSetCompareBrowserFixture serves only an opt-in, private SQLite
// fixture. It has no provider connection or fixture control HTTP endpoints.
func TestBackupSetCompareBrowserFixture(t *testing.T) {
	dir := os.Getenv("XIRANG_COMPARE_BROWSER_FIXTURE_DIR")
	if dir == "" {
		t.Skip("XIRANG_COMPARE_BROWSER_FIXTURE_DIR is not set")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal("invalid fixture directory")
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		t.Fatal("browser fixture requires an existing private directory")
	}
	t.Setenv("TMPDIR", absolute)
	t.Setenv("XIRANG_RCLONE_HTTP_DIAGNOSTIC_DIR", "")
	fixture := newRcloneHTTPFixture(t, "", nil)
	now := time.Now().UTC().Truncate(time.Second)
	node := model.Node{Name: "Compare fixture node", Host: "compare.invalid", Port: 22, Username: "fixture", AuthType: "key", Status: "online", BackupDir: "fixture", CreatedAt: now, UpdatedAt: now}
	if err := fixture.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	task := model.Task{Name: "Compare fixture task", NodeID: node.ID, ExecutorType: "restic", Status: "idle", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := fixture.db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	repositories := []model.BackupRepository{
		{ID: strings.Repeat("a", 32), ProviderKind: string(backupasset.ProviderRestic), DisplayName: "Compare online", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1, CapabilitiesJSON: `{"list":true,"open_sequential":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now},
		{ID: strings.Repeat("b", 32), ProviderKind: string(backupasset.ProviderRestic), DisplayName: "Compare offline", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOffline), CapabilityRevision: 1, CapabilitiesJSON: `{"list":true,"open_sequential":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now},
	}
	if err := fixture.db.Create(&repositories).Error; err != nil {
		t.Fatal(err)
	}
	unlinkedAt := now.Add(-101*time.Minute - 30*time.Second)
	links := []model.TaskRepositoryLink{
		{ID: strings.Repeat("c", 32), TaskID: &task.ID, RepositoryID: repositories[0].ID, TaskNameSnapshot: task.Name, NodeIDSnapshot: node.ID, NodeNameSnapshot: node.Name, PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: unlinkedAt, CreatedAt: now, UpdatedAt: now},
		{ID: strings.Repeat("d", 32), TaskID: &task.ID, RepositoryID: repositories[1].ID, TaskNameSnapshot: task.Name, NodeIDSnapshot: node.ID, NodeNameSnapshot: node.Name, PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: now.Add(-48 * time.Hour), UnlinkedAt: &unlinkedAt, CreatedAt: now, UpdatedAt: now},
	}
	if err := fixture.db.Create(&links).Error; err != nil {
		t.Fatal(err)
	}
	const versionCount = 102
	for index := range versionCount {
		captured := now.Add(time.Duration(index-versionCount) * time.Minute)
		started := captured.Add(-time.Minute)
		run := model.TaskRun{TaskID: task.ID, TriggerType: "manual", Status: "success", StartedAt: &started, FinishedAt: &captured, CreatedAt: captured, UpdatedAt: captured}
		if err := fixture.db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
		side := 0
		if index == 0 {
			side = 1
		}
		lineage, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{Version: 1, TaskRepositoryLinkID: links[side].ID, TaskID: task.ID, TaskRunID: run.ID, Trigger: "manual", PublicationMode: string(backupasset.PublicationNativeSnapshot), PointCodecVersion: 1, TagCodecVersion: 1, StartedAt: captured.Add(-2 * time.Minute), PreparedAt: started, PointDeadlineAt: captured.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		pointID := fmt.Sprintf("%032x", index+1)
		physical := backupasset.PhysicalOnline
		if side == 1 {
			physical = backupasset.PhysicalOffline
		}
		count := int64(0)
		if index == 0 || index == versionCount-1 {
			count = 209 // two directories, two duplicate basenames, 205 numbered files
		}
		point := model.RecoveryPoint{ID: pointID, RepositoryID: repositories[side].ID, ProducingTaskID: &task.ID, ProducingTaskRunID: &run.ID, ProducingTaskNameSnapshot: task.Name, ProducingNodeIDSnapshot: node.ID, ProducingNodeNameSnapshot: node.Name, LineageJSON: lineage, EncryptedProviderLocator: "FAKE_COMPARE_LOCATOR_FOR_TEST_ONLY", Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted), CapturedAt: &captured, CommittedAt: &captured, SourceFingerprint: fmt.Sprintf("%064x", index+1), ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("b", 64), EntryCount: count, ConsistencyJSON: `{}`, FidelityJSON: `{}`, CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(physical), HoldState: string(backupasset.HoldNone), CreatedAt: captured, UpdatedAt: captured}
		if err := fixture.db.Create(&point).Error; err != nil {
			t.Fatal(err)
		}
		manifest := model.RecoveryPointManifest{
			ID: fmt.Sprintf("%032x", 2000+index), RecoveryPointID: pointID, Revision: 1,
			DigestAlgorithm: "sha256", Digest: point.ManifestDigest, Generator: "xirang-restic-ls", GeneratorVersion: "1",
			Completeness: string(backupasset.ManifestComplete), EntryCount: count, IsActive: true,
			CreatedAt: captured, UpdatedAt: captured,
		}
		if err := fixture.db.Create(&manifest).Error; err != nil {
			t.Fatal(err)
		}
		generationID := fmt.Sprintf("%032x", 1000+index)
		generation := model.CatalogGeneration{ID: generationID, RecoveryPointID: pointID, Generation: 1, State: "complete", IsActive: true, SourceFingerprint: point.SourceFingerprint, ManifestID: &manifest.ID, ExpectedEntryCount: count, WrittenEntryCount: count, ExpectedDigest: manifest.Digest, WrittenDigest: strings.Repeat("a", 64), StartedAt: captured, FinishedAt: &captured, CreatedAt: captured, UpdatedAt: captured}
		if err := fixture.db.Create(&generation).Error; err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			continue
		}
		entries := make([]model.CatalogEntry, 0, count)
		add := func(sequence int, parent *string, path, name, kind string, size int64) string {
			id := fmt.Sprintf("%064x", index*1000+sequence)
			entries = append(entries, model.CatalogEntry{GenerationID: generationID, EntryID: id, RecoveryPointID: pointID, ParentEntryID: parent, NormalizedPath: path, Name: name, EntryType: kind, Size: size, ModifiedAt: &now, Mode: "0640", Owner: "fixture", MimeType: "application/octet-stream", FingerprintStrength: "none", EncryptedProviderLocator: "FAKE_COMPARE_ENTRY_FOR_TEST_ONLY", SecurityState: "sealed", CreatedAt: captured})
			return id
		}
		alpha := add(1, nil, "alpha", "alpha", "directory", 0)
		beta := add(2, nil, "beta", "beta", "directory", 0)
		size := int64(1)
		if index == versionCount-1 {
			size = 2
		}
		add(3, &alpha, "alpha/report.txt", "report.txt", "file", size)
		add(4, &beta, "beta/report.txt", "report.txt", "file", size)
		for n := range 205 {
			name := fmt.Sprintf("z-change-%03d.txt", n)
			add(n+5, nil, name, name, "file", size)
		}
		if err := fixture.db.CreateInBatches(entries, 200).Error; err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: fixture.router, ReadHeaderTimeout: 5 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	fixtureContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	descriptor := map[string]any{
		"url": "http://" + listener.Addr().String(), "token": fixture.token,
		"admin_id": fixture.admin.ID, "admin_username": fixture.admin.Username, "node_id": node.ID,
		"base_point_id": fmt.Sprintf("%032x", versionCount), "old_point_id": fmt.Sprintf("%032x", 1),
		"empty_point_id": fmt.Sprintf("%032x", versionCount-1),
	}
	file, err := os.OpenFile(filepath.Join(absolute, "descriptor.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encodeErr := json.NewEncoder(file).Encode(descriptor)
	closeErr := file.Close()
	if encodeErr != nil || closeErr != nil {
		t.Fatal("write private browser descriptor")
	}
	t.Log("Compare browser fixture ready")
	select {
	case <-fixtureContext.Done():
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatal("browser fixture server stopped unexpectedly")
		}
	}
}
