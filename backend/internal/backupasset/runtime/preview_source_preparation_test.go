package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	runtimepkg "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/backupasset/publication"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

// TestRuntimePreparePreviewSourceRepairsMutableRsyncBeforeFirstIssue exercises
// the user path that was missing from the v0.55.2 delivery flow: the mutable
// source changes before any completion callback or Catalog worker build, the
// preparation call invalidates the stale projection and wakes the existing
// worker, and only the newly active exact entry is handed to Broker.Issue.
func TestRuntimePreparePreviewSourceRepairsMutableRsyncBeforeFirstIssue(t *testing.T) {
	if runtimepkg.GOOS != "linux" {
		t.Skip("local Rsync Provider access requires Linux openat2 support")
	}
	fixture := newPreviewSourcePreparationFixture(t,
		[]byte("services:\n  api:\n    image: nginx:initial\n"),
		[]byte("services:\n  worker:\n    image: nginx:sibling\n"),
	)
	ctx := context.Background()
	initial := fixture.entries[0]
	initialGeneration := fixture.generations[0]
	initialPoint := fixture.points[0]
	initialRoot := fixture.rootInfos[0]
	updatedPayload := []byte("services:\n  api:\n    image: nginx:prepared-without-callback\n")

	// This is intentionally before the worker is started and before any
	// ObserveBackupSourceCompletion call. Keep the target directory metadata
	// unchanged so the mutable point's root fingerprint remains unchanged.
	writePreviewFirstPayload(t, fixture.sourceRoots[0], fixture.targetRoots[0], updatedPayload)
	if err := os.Chtimes(fixture.targetRoots[0], initialRoot.ModTime(), initialRoot.ModTime()); err != nil {
		t.Fatalf("preserve mutable target root metadata: %v", err)
	}
	var pointBeforePrepare model.RecoveryPoint
	if err := fixture.db.First(&pointBeforePrepare, "id = ?", initialPoint.ID).Error; err != nil {
		t.Fatalf("load point before preparation: %v", err)
	}
	if pointBeforePrepare.SourceFingerprint != initialPoint.SourceFingerprint {
		t.Fatalf("controlled child mutation changed root fingerprint before preparation: before=%q after=%q", initialPoint.SourceFingerprint, pointBeforePrepare.SourceFingerprint)
	}

	pausedFactory := fixture.startPausedCatalogWorker(t)
	prepared, err := fixture.runtime.PreparePreviewSource(ctx, previewPreparationAdminActor(), initial.ref)
	if err != nil {
		t.Fatalf("prepare stale mutable Rsync preview source: %v", err)
	}
	preparedAgain, err := fixture.runtime.PreparePreviewSource(ctx, previewPreparationAdminActor(), initial.ref)
	if err != nil {
		t.Fatalf("coalesced duplicate preview preparation: %v", err)
	}
	if preparedAgain.Coverage.Status == catalog.CoverageComplete ||
		(preparedAgain.Generation != nil && preparedAgain.Generation.ID == initialGeneration.ID) ||
		!preparedAgain.ContentAvailability.Available {
		t.Fatalf("duplicate preparation returned stale/file-unavailable status=%+v", preparedAgain)
	}
	if prepared.Coverage.Status == catalog.CoverageComplete ||
		(prepared.Generation != nil && prepared.Generation.ID == initialGeneration.ID) ||
		!prepared.ContentAvailability.Available {
		t.Fatalf("preparation returned stale/file-unavailable status=%+v", prepared)
	}

	select {
	case <-pausedFactory.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("preparation did not wake the existing Catalog worker")
	}
	gap := previewPreparationCatalogStatus(t, fixture.runtime.CatalogService(), initial.ref.RecoveryPointID)
	if gap.Generation != nil || gap.Coverage.Status != catalog.CoverageBuilding ||
		gap.LatestBuild == nil || gap.LatestBuild.State != catalog.GenerationBuilding {
		t.Fatalf("superseded Catalog gap was not projected as pending/building: %+v", gap)
	}
	if !gap.ContentAvailability.Available {
		t.Fatalf("pending source replacement was mislabeled file-unavailable: %+v", gap)
	}

	pausedFactory.Release()
	updatedGeneration, updatedEntry := waitForPreviewFirstCatalog(t, fixture.db, initial.ref.RecoveryPointID, initialGeneration.ID, updatedPayload)
	if updatedGeneration.SourceFingerprint != initialGeneration.SourceFingerprint ||
		updatedEntry.EntryID != initial.modelEntry.EntryID || updatedEntry.Size != int64(len(updatedPayload)) {
		t.Fatalf("replacement Catalog lost mutable lineage: initial=%+v updated=%+v entry=%+v", initialGeneration, updatedGeneration, updatedEntry)
	}

	current := loadPreviewFirstCurrentEntry(t, fixture.runtime.CatalogService(), fixture.db, initial.ref.RecoveryPointID)
	if current.generationID != updatedGeneration.ID || current.modelEntry.Size != int64(len(updatedPayload)) {
		t.Fatalf("exact current entry was not refreshed after preparation: %+v", current)
	}
	issueAndServePreviewFirst(t, fixture.runtime.ContentBroker(), fixture.db, current, fixture.clock, updatedPayload)

	var pointAfterPrepare model.RecoveryPoint
	if err := fixture.db.First(&pointAfterPrepare, "id = ?", initialPoint.ID).Error; err != nil {
		t.Fatalf("load point after preparation: %v", err)
	}
	if pointAfterPrepare.SourceFingerprint != initialPoint.SourceFingerprint {
		t.Fatalf("child mutation unexpectedly changed mutable root fingerprint: before=%q after=%q", initialPoint.SourceFingerprint, pointAfterPrepare.SourceFingerprint)
	}
	// A source on the same node must remain isolated from the prepared point.
	sibling := loadPreviewFirstCurrentEntry(t, fixture.runtime.CatalogService(), fixture.db, fixture.points[1].ID)
	if sibling.generationID != fixture.generations[1].ID || sibling.modelEntry.Size != int64(len(fixture.payloads[1])) {
		t.Fatalf("preparing point A changed sibling point B: initial=%+v current=%+v", fixture.entries[1], sibling)
	}
	issueAndServePreviewFirst(t, fixture.runtime.ContentBroker(), fixture.db, sibling, fixture.clock, fixture.payloads[1])
}

// TestRuntimePreparePreviewSourceJoinsLiveBuildWithoutPreparingStaleActiveGeneration
// covers the overlap where the exact mutable entry changes while a real
// Catalog builder already owns the next generation. Preparation must supersede
// the stale active generation, preserve the live builder and lease, and project
// a pending gap until the builder publishes the replacement.
func TestRuntimePreparePreviewSourceJoinsLiveBuildWithoutPreparingStaleActiveGeneration(t *testing.T) {
	if runtimepkg.GOOS != "linux" {
		t.Skip("local Rsync Provider access requires Linux openat2 support")
	}
	fixture := newPreviewSourcePreparationFixture(t,
		[]byte("services:\n  api:\n    image: nginx:live-build-old\n"),
		[]byte("services:\n  worker:\n    image: nginx:live-build-sibling\n"),
	)
	initial := fixture.entries[0]
	initialGeneration := fixture.generations[0]
	updatedPayload := []byte("services:\n  api:\n    image: nginx:live-build-child-mutated\n")
	rootInfo := fixture.rootInfos[0]
	writePreviewFirstPayload(t, fixture.sourceRoots[0], fixture.targetRoots[0], updatedPayload)
	if err := os.Chtimes(fixture.targetRoots[0], rootInfo.ModTime(), rootInfo.ModTime()); err != nil {
		t.Fatalf("preserve mutable target root metadata after child mutation: %v", err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", initial.ref.RecoveryPointID).
		Update("observed_at", fixture.clock).Error; err != nil {
		t.Fatalf("set recent mutable observation: %v", err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", initial.ref.RecoveryPointID).Error; err != nil {
		t.Fatalf("load recent mutable point: %v", err)
	}
	if point.ObservedAt == nil || !point.ObservedAt.Equal(fixture.clock) {
		t.Fatalf("mutable point observed_at=%v, want recent fixture clock %v", point.ObservedAt, fixture.clock)
	}

	pausedFactory, _ := fixture.configurePausedCatalogWorker(t)
	buildDone := make(chan struct {
		generation model.CatalogGeneration
		err        error
	}, 1)
	go func() {
		generation, err := fixture.runtime.catalogIndexer.Build(context.Background(), catalog.BuildRequest{
			RepositoryID:    fixture.points[0].RepositoryID,
			RecoveryPointID: initial.ref.RecoveryPointID,
			CorrelationID:   "preview-preparation-live-build",
		})
		buildDone <- struct {
			generation model.CatalogGeneration
			err        error
		}{generation: generation, err: err}
	}()
	select {
	case <-pausedFactory.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for live Catalog builder")
	}
	var inFlight model.CatalogGeneration
	if err := fixture.db.Where("recovery_point_id = ? AND state = ? AND is_active = ?",
		initial.ref.RecoveryPointID, catalog.GenerationBuilding, false).
		Order("generation DESC").First(&inFlight).Error; err != nil {
		t.Fatalf("load live Catalog generation: %v", err)
	}
	if inFlight.Generation <= initialGeneration.Generation {
		t.Fatalf("live Catalog generation=%+v did not advance beyond initial=%+v", inFlight, initialGeneration)
	}

	status, err := fixture.runtime.PreparePreviewSource(
		context.Background(), previewPreparationAdminActor(), initial.ref,
	)
	if err != nil {
		t.Fatalf("prepare source during live Catalog build: status=%+v err=%v", status, err)
	}
	if status.Generation != nil || status.Coverage.Status != catalog.CoverageBuilding ||
		status.LatestBuild == nil || status.LatestBuild.State != catalog.GenerationBuilding ||
		!status.ContentAvailability.Available {
		t.Fatalf("preparation did not project superseded active generation as pending: %+v", status)
	}
	var activeDuringBuild model.CatalogGeneration
	activeErr := fixture.db.Where("recovery_point_id = ? AND is_active = ?", initial.ref.RecoveryPointID, true).
		First(&activeDuringBuild).Error
	if !errors.Is(activeErr, gorm.ErrRecordNotFound) {
		t.Fatalf("preparation retained an active generation while live builder replaced it: generation=%+v err=%v", activeDuringBuild, activeErr)
	}
	var inFlightAfter model.CatalogGeneration
	if err := fixture.db.First(&inFlightAfter, "id = ?", inFlight.ID).Error; err != nil {
		t.Fatalf("reload live Catalog generation after preparation: %v", err)
	}
	if inFlightAfter.State != string(catalog.GenerationBuilding) || inFlightAfter.IsActive {
		t.Fatalf("preparation changed live Catalog generation: %+v", inFlightAfter)
	}
	var liveLease model.RecoveryPointLease
	if err := fixture.db.Where("recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ?",
		initial.ref.RecoveryPointID, backupasset.LeaseHolderCatalogBuild, "catalog:"+initial.ref.RecoveryPointID,
		backupasset.LeaseActive).First(&liveLease).Error; err != nil {
		t.Fatalf("preparation lost live Catalog lease: %v", err)
	}
	if !liveLease.LeaseExpiresAt.After(fixture.clock) || !liveLease.AbsoluteDeadline.After(fixture.clock) {
		t.Fatalf("live Catalog lease is not valid while builder is paused: %+v", liveLease)
	}

	pausedFactory.Release()
	select {
	case result := <-buildDone:
		if result.err != nil || result.generation.State != string(catalog.GenerationComplete) || !result.generation.IsActive {
			t.Fatalf("live Catalog build result=%+v err=%v", result.generation, result.err)
		}
		if result.generation.ID == initialGeneration.ID {
			t.Fatalf("live Catalog build reused initial generation: %+v", result.generation)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for live Catalog builder completion")
	}
	current := loadPreviewFirstCurrentEntry(t, fixture.runtime.CatalogService(), fixture.db, initial.ref.RecoveryPointID)
	if current.generationID == initialGeneration.ID || current.modelEntry.Size != int64(len(updatedPayload)) {
		t.Fatalf("live Catalog replacement did not publish mutated child: %+v", current)
	}
	issueAndServePreviewFirst(t, fixture.runtime.ContentBroker(), fixture.db, current, fixture.clock, updatedPayload)
}

// TestRuntimePreparePreviewSourceRepairsRootMetadataDrift verifies that a
// changed mutable root is handled as source metadata drift, not as a missing
// file. The exact connected binding is refreshed and only that point's
// Catalog lineage is rebuilt.
func TestRuntimePreparePreviewSourceRepairsRootMetadataDrift(t *testing.T) {
	if runtimepkg.GOOS != "linux" {
		t.Skip("local Rsync Provider access requires Linux openat2 support")
	}
	fixture := newPreviewSourcePreparationFixture(t,
		[]byte("services:\n  api:\n    image: nginx:root-drift\n"),
		[]byte("services:\n  worker:\n    image: nginx:root-drift-sibling\n"),
	)
	first := fixture.entries[0]
	oldPoint := fixture.points[0]
	oldGeneration := fixture.generations[0]
	rootInfo := fixture.rootInfos[0]
	changedRootTime := rootInfo.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(fixture.targetRoots[0], changedRootTime, changedRootTime); err != nil {
		t.Fatalf("change mutable target root metadata: %v", err)
	}

	pausedFactory := fixture.startPausedCatalogWorker(t)
	status, err := fixture.runtime.PreparePreviewSource(context.Background(), previewPreparationAdminActor(), first.ref)
	if err != nil {
		t.Fatalf("prepare root metadata drift: status=%+v err=%v", status, err)
	}
	if status.Coverage.Status == catalog.CoverageComplete ||
		(status.Generation != nil && status.Generation.ID == oldGeneration.ID) {
		t.Fatalf("root metadata drift returned stale false-ready status=%+v", status)
	}
	select {
	case <-pausedFactory.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("root metadata drift did not wake the Catalog worker")
	}
	gap := previewPreparationCatalogStatus(t, fixture.runtime.CatalogService(), first.ref.RecoveryPointID)
	if gap.Generation != nil || gap.Coverage.Status != catalog.CoverageBuilding ||
		gap.LatestBuild == nil || gap.LatestBuild.State != catalog.GenerationBuilding ||
		!gap.ContentAvailability.Available {
		t.Fatalf("root metadata drift gap status=%+v", gap)
	}
	pausedFactory.Release()
	updatedGeneration, updatedEntry := waitForPreviewFirstCatalog(t, fixture.db, first.ref.RecoveryPointID, oldGeneration.ID, fixture.payloads[0])
	if updatedGeneration.SourceFingerprint == oldGeneration.SourceFingerprint ||
		updatedEntry.EntryID != first.modelEntry.EntryID {
		t.Fatalf("root metadata drift did not refresh exact point lineage: old=%+v new=%+v entry=%+v", oldGeneration, updatedGeneration, updatedEntry)
	}
	var refreshedPoint model.RecoveryPoint
	if err := fixture.db.First(&refreshedPoint, "id = ?", oldPoint.ID).Error; err != nil {
		t.Fatalf("reload point after root metadata drift: %v", err)
	}
	if refreshedPoint.SourceFingerprint == oldPoint.SourceFingerprint {
		t.Fatalf("root metadata drift did not update mutable point source fingerprint: before=%q after=%q", oldPoint.SourceFingerprint, refreshedPoint.SourceFingerprint)
	}
	current := loadPreviewFirstCurrentEntry(t, fixture.runtime.CatalogService(), fixture.db, first.ref.RecoveryPointID)
	issueAndServePreviewFirst(t, fixture.runtime.ContentBroker(), fixture.db, current, fixture.clock, fixture.payloads[0])
	sibling := loadPreviewFirstCurrentEntry(t, fixture.runtime.CatalogService(), fixture.db, fixture.points[1].ID)
	if sibling.generationID != fixture.generations[1].ID {
		t.Fatalf("root metadata preparation changed sibling point: initial=%+v current=%+v", fixture.entries[1], sibling)
	}
}

// TestRuntimePreparePreviewSourceFailsClosedForMissingDeletedOfflineAndUnauthorized
// covers failures that must not be converted into a metadata-drift repair. A
// missing Catalog entry is a real 404; a deleted physical file is rebuilt
// before the exact current entry is looked up; an offline binding remains
// unavailable; and an unauthorized actor cannot trigger provider access or
// Catalog mutation.
func TestRuntimePreparePreviewSourceFailsClosedForMissingDeletedOfflineAndUnauthorized(t *testing.T) {
	if runtimepkg.GOOS != "linux" {
		t.Skip("local Rsync Provider access requires Linux openat2 support")
	}
	fixture := newPreviewSourcePreparationFixture(t,
		[]byte("services:\n  api:\n    image: nginx:deleted\n"),
		[]byte("services:\n  worker:\n    image: nginx:offline\n"),
	)
	ctx := context.Background()
	admin := previewPreparationAdminActor()
	first := fixture.entries[0]
	second := fixture.entries[1]
	firstGeneration := fixture.generations[0]
	secondGeneration := fixture.generations[1]

	if _, err := fixture.runtime.PreparePreviewSource(ctx, content.DeliveryActor{UserID: 1, Username: "viewer", Role: "viewer"}, first.ref); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("unauthorized preparation error=%v, want forbidden", err)
	}
	assertPreviewPreparationGenerationUnchanged(t, fixture.db, first.ref.RecoveryPointID, firstGeneration.ID)

	missingRef := backupasset.AssetRef{RecoveryPointID: first.ref.RecoveryPointID, EntryID: strings.Repeat("f", 64)}
	if _, err := fixture.runtime.PreparePreviewSource(ctx, admin, missingRef); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("missing Catalog entry preparation error=%v, want not found", err)
	}
	assertPreviewPreparationGenerationUnchanged(t, fixture.db, first.ref.RecoveryPointID, firstGeneration.ID)

	// Remove the real provider entry while its completed Catalog is still the
	// current projection. The source remains online, so preparation must prove
	// the disappearance through a replacement Catalog rather than serve stale
	// bytes or guess another entry.
	if err := os.Remove(filepath.Join(fixture.targetRoots[0], "docker-compose.yml")); err != nil {
		t.Fatalf("delete target entry: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.sourceRoots[0], "docker-compose.yml")); err != nil {
		t.Fatalf("delete source entry: %v", err)
	}
	if err := os.Chtimes(fixture.targetRoots[0], fixture.rootInfos[0].ModTime(), fixture.rootInfos[0].ModTime()); err != nil {
		t.Fatalf("preserve online target root metadata after deletion: %v", err)
	}
	deletedFactory := fixture.startPausedCatalogWorker(t)
	if status, err := fixture.runtime.PreparePreviewSource(ctx, admin, first.ref); err != nil {
		t.Fatalf("prepare deleted physical entry: status=%+v err=%v", status, err)
	}
	select {
	case <-deletedFactory.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("deleted physical entry did not wake the Catalog worker")
	}
	deletedGap := previewPreparationCatalogStatus(t, fixture.runtime.CatalogService(), first.ref.RecoveryPointID)
	if deletedGap.Generation != nil || deletedGap.Coverage.Status != catalog.CoverageBuilding ||
		!deletedGap.ContentAvailability.Available {
		t.Fatalf("deleted physical entry gap status=%+v", deletedGap)
	}
	deletedFactory.Release()
	waitForPreviewPreparationMissingEntry(t, fixture.db, fixture.runtime.CatalogService(), first.ref, firstGeneration.ID)
	assertPreviewPreparationGenerationUnchanged(t, fixture.db, second.ref.RecoveryPointID, secondGeneration.ID)
	// A missing root is an availability failure, not proof that the catalog
	// entry was deleted. It must never become a 404 or a replacement guess.
	if err := os.RemoveAll(fixture.targetRoots[1]); err != nil {
		t.Fatalf("remove sibling target root: %v", err)
	}
	if _, err := fixture.runtime.PreparePreviewSource(ctx, admin, second.ref); err == nil ||
		errors.Is(err, backupasset.ErrNotFound) || !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("missing source root preparation error=%v, want non-404 capability unavailable", err)
	}
	assertPreviewPreparationGenerationUnchanged(t, fixture.db, second.ref.RecoveryPointID, secondGeneration.ID)
	if err := os.MkdirAll(fixture.targetRoots[1], 0o700); err != nil {
		t.Fatalf("restore sibling target root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.targetRoots[1], "docker-compose.yml"), fixture.payloads[1], 0o600); err != nil {
		t.Fatalf("restore sibling target entry: %v", err)
	}

	// Offline is orthogonal to metadata drift. Mark only point B offline and
	// ensure the call returns the closed capability without touching point A.
	if result := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", fixture.points[1].RepositoryID).
		Updates(map[string]any{"status": string(backupasset.RepositoryOffline)}); result.Error != nil {
		t.Fatalf("mark sibling repository offline: %v", result.Error)
	}
	if result := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.points[1].ID).
		Updates(map[string]any{"physical_availability": string(backupasset.PhysicalOffline)}); result.Error != nil {
		t.Fatalf("mark sibling point offline: %v", result.Error)
	}
	if _, err := fixture.runtime.PreparePreviewSource(ctx, admin, second.ref); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("offline preparation error=%v, want capability unavailable", err)
	}
	assertPreviewPreparationGenerationUnchanged(t, fixture.db, second.ref.RecoveryPointID, secondGeneration.ID)
}

// TestRuntimePreparePreviewSourceRearmsNoActiveMutableCatalog verifies that a
// historically known mutable entry does not become a terminal 404 while its
// Catalog has no active generation. It also covers retryable failed/partial
// generations: preparation supersedes the stale outcome, wakes the existing
// worker, and waits for a fresh active generation.
func TestRuntimePreparePreviewSourceRearmsNoActiveMutableCatalog(t *testing.T) {
	if runtimepkg.GOOS != "linux" {
		t.Skip("local Rsync Provider access requires Linux openat2 support")
	}
	tests := []struct {
		name  string
		state catalog.GenerationState
	}{
		{name: "latest_superseded", state: catalog.GenerationSuperseded},
		{name: "latest_failed", state: catalog.GenerationFailed},
		{name: "latest_partial", state: catalog.GenerationPartial},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPreviewSourcePreparationFixture(t, []byte("healthy mutable payload"), []byte("sibling payload"))
			initial := fixture.entries[0]
			initialGeneration := fixture.generations[0]
			endedAt := fixture.clock.Add(-time.Minute)
			if err := fixture.db.Model(&model.CatalogGeneration{}).Where("id = ?", initialGeneration.ID).
				Updates(map[string]any{
					"state": string(catalog.GenerationSuperseded), "is_active": false,
					"finished_at": endedAt, "updated_at": endedAt,
				}).Error; err != nil {
				t.Fatalf("supersede initial Catalog generation: %v", err)
			}
			if testCase.state != catalog.GenerationSuperseded {
				retryID := fmt.Sprintf("%031x%x", 0x9f00+index, index+1)
				retry := model.CatalogGeneration{
					ID: retryID, RecoveryPointID: initial.ref.RecoveryPointID,
					Generation: initialGeneration.Generation + 1, State: string(testCase.state),
					IsActive: false, SourceFingerprint: initialGeneration.SourceFingerprint,
					ExpectedEntryCount: initialGeneration.ExpectedEntryCount,
					WrittenEntryCount:  initialGeneration.WrittenEntryCount,
					ExpectedDigest:     initialGeneration.ExpectedDigest, WrittenDigest: initialGeneration.WrittenDigest,
					ErrorCode: string(catalog.GenerationErrorSourceChanged),
					StartedAt: endedAt, FinishedAt: &endedAt, CreatedAt: endedAt, UpdatedAt: endedAt,
				}
				if err := fixture.db.Create(&retry).Error; err != nil {
					t.Fatalf("seed %s Catalog generation: %v", testCase.state, err)
				}
			}

			pausedFactory := fixture.startPausedCatalogWorker(t)
			status, err := fixture.runtime.PreparePreviewSource(context.Background(), previewPreparationAdminActor(), initial.ref)
			if err != nil {
				t.Fatalf("prepare no-active %s Catalog: status=%+v err=%v", testCase.state, status, err)
			}
			select {
			case <-pausedFactory.entered:
			case <-time.After(10 * time.Second):
				t.Fatalf("preparation did not wake Catalog worker for %s state", testCase.state)
			}
			gap := previewPreparationCatalogStatus(t, fixture.runtime.CatalogService(), initial.ref.RecoveryPointID)
			if gap.Generation != nil || gap.Coverage.Status != catalog.CoverageBuilding ||
				gap.LatestBuild == nil || gap.LatestBuild.State != catalog.GenerationBuilding {
				t.Fatalf("no-active %s Catalog was not projected as pending/building: %+v", testCase.state, gap)
			}
			if !gap.ContentAvailability.Available {
				t.Fatalf("no-active %s Catalog was mislabeled file-unavailable: %+v", testCase.state, gap)
			}
			pausedFactory.Release()
			refreshedGeneration, refreshedEntry := waitForPreviewFirstCatalog(
				t, fixture.db, initial.ref.RecoveryPointID, initialGeneration.ID, fixture.payloads[0],
			)
			if refreshedGeneration.Generation <= initialGeneration.Generation || refreshedEntry.EntryID != initial.modelEntry.EntryID {
				t.Fatalf("no-active %s retry lost entry lineage: generation=%+v entry=%+v", testCase.state, refreshedGeneration, refreshedEntry)
			}
			if testCase.state != catalog.GenerationSuperseded {
				var retry model.CatalogGeneration
				if err := fixture.db.Where("recovery_point_id = ? AND generation = ?",
					initial.ref.RecoveryPointID, initialGeneration.Generation+1).First(&retry).Error; err != nil {
					t.Fatalf("reload retry generation: %v", err)
				}
				if retry.State != string(catalog.GenerationSuperseded) || retry.IsActive {
					t.Fatalf("retry generation was not superseded before rebuild: %+v", retry)
				}
			}
		})
	}
}

// TestRuntimePreparePreviewSourceRearmsOrphanedMutableCatalogBuild verifies
// that a no-active mutable point does not wait for the normal abandoned-build
// scan when its latest Building generation has no live Catalog lease. A live
// paused builder is the opposite case and must be joined rather than stolen.
func TestRuntimePreparePreviewSourceRearmsOrphanedMutableCatalogBuild(t *testing.T) {
	if runtimepkg.GOOS != "linux" {
		t.Skip("local Rsync Provider access requires Linux openat2 support")
	}
	tests := []struct {
		name        string
		leaseStatus backupasset.LeaseStatus
	}{
		{name: "missing_lease"},
		{name: "expired_lease_status", leaseStatus: backupasset.LeaseExpired},
		{name: "active_but_expired_lease", leaseStatus: backupasset.LeaseActive},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPreviewSourcePreparationFixture(t, []byte("orphan payload"), []byte("sibling payload"))
			initial := fixture.entries[0]
			initialGeneration := fixture.generations[0]
			supersedePreviewPreparationGeneration(t, fixture, initialGeneration)
			catalogConfig, err := fixture.runtime.foundation.CatalogConfig()
			if err != nil {
				t.Fatalf("load Catalog config for orphan build: %v", err)
			}
			startedAt := fixture.clock.Add(-2 * catalogConfig.BuildTimeout)
			orphan := seedPreviewPreparationBuildingGeneration(t, fixture, initialGeneration, startedAt, 0x552210+index)
			if testCase.leaseStatus != "" {
				seedPreviewPreparationExpiredLease(
					t, fixture, initial.ref.RecoveryPointID, startedAt, 0x552220+index, testCase.leaseStatus,
				)
			}
			pausedFactory, workerCtx := fixture.configurePausedCatalogWorker(t)
			status, err := fixture.runtime.PreparePreviewSource(
				context.Background(), previewPreparationAdminActor(), initial.ref,
			)
			if err != nil {
				t.Fatalf("prepare orphan %s Catalog: status=%+v err=%v", testCase.name, status, err)
			}
			if status.Generation != nil || status.Coverage.Status != catalog.CoverageBuilding ||
				!status.ContentAvailability.Available {
				t.Fatalf("orphan %s preparation status=%+v, want pending available Catalog", testCase.name, status)
			}
			var orphanAfterPrepare model.CatalogGeneration
			if err := fixture.db.First(&orphanAfterPrepare, "id = ?", orphan.ID).Error; err != nil {
				t.Fatalf("reload orphan %s Catalog generation after preparation: %v", testCase.name, err)
			}
			if orphanAfterPrepare.State != string(catalog.GenerationSuperseded) || orphanAfterPrepare.IsActive {
				t.Fatalf("orphan %s Catalog generation was not rearmed promptly: %+v", testCase.name, orphanAfterPrepare)
			}
			go fixture.runtime.catalogWorker.Run(workerCtx)
			select {
			case <-pausedFactory.entered:
			case <-time.After(10 * time.Second):
				t.Fatalf("orphan %s preparation did not reach replacement Catalog build", testCase.name)
			}
			pausedFactory.Release()
			refreshedGeneration, refreshedEntry := waitForPreviewFirstCatalog(
				t, fixture.db, initial.ref.RecoveryPointID, initialGeneration.ID, fixture.payloads[0],
			)
			if refreshedGeneration.Generation <= orphan.Generation ||
				refreshedEntry.EntryID != initial.modelEntry.EntryID {
				t.Fatalf("orphan %s replacement lost Catalog lineage: generation=%+v orphan=%+v entry=%+v",
					testCase.name, refreshedGeneration, orphan, refreshedEntry)
			}
			current := loadPreviewFirstCurrentEntry(t, fixture.runtime.CatalogService(), fixture.db, initial.ref.RecoveryPointID)
			issueAndServePreviewFirst(t, fixture.runtime.ContentBroker(), fixture.db, current, fixture.clock, fixture.payloads[0])
		})
	}

	t.Run("live_paused_builder_joins", func(t *testing.T) {
		fixture := newPreviewSourcePreparationFixture(t, []byte("live paused payload"), []byte("sibling payload"))
		initial := fixture.entries[0]
		initialGeneration := fixture.generations[0]
		supersedePreviewPreparationGeneration(t, fixture, initialGeneration)
		pausedFactory, _ := fixture.configurePausedCatalogWorker(t)
		buildDone := make(chan struct {
			generation model.CatalogGeneration
			err        error
		}, 1)
		go func() {
			generation, err := fixture.runtime.catalogIndexer.Build(context.Background(), catalog.BuildRequest{
				RepositoryID:    fixture.points[0].RepositoryID,
				RecoveryPointID: initial.ref.RecoveryPointID,
				CorrelationID:   "preview-preparation-live-paused",
			})
			buildDone <- struct {
				generation model.CatalogGeneration
				err        error
			}{generation: generation, err: err}
		}()
		select {
		case <-pausedFactory.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for legitimate paused Catalog builder")
		}
		status, err := fixture.runtime.PreparePreviewSource(
			context.Background(), previewPreparationAdminActor(), initial.ref,
		)
		if err != nil {
			t.Fatalf("prepare while legitimate paused builder is live: status=%+v err=%v", status, err)
		}
		if status.Generation != nil || status.Coverage.Status != catalog.CoverageBuilding ||
			!status.ContentAvailability.Available {
			t.Fatalf("legitimate paused builder was not projected as pending: %+v", status)
		}
		var building model.CatalogGeneration
		if err := fixture.db.Where("recovery_point_id = ? AND state = ? AND is_active = ?",
			initial.ref.RecoveryPointID, catalog.GenerationBuilding, false).
			Order("generation DESC").First(&building).Error; err != nil {
			t.Fatalf("load legitimate paused Catalog generation after preparation: %v", err)
		}
		if building.State != string(catalog.GenerationBuilding) || building.IsActive {
			t.Fatalf("legitimate paused Catalog builder was not preserved: %+v", building)
		}
		pausedFactory.Release()
		select {
		case result := <-buildDone:
			if result.err != nil || result.generation.State != string(catalog.GenerationComplete) || !result.generation.IsActive {
				t.Fatalf("legitimate paused Catalog build result=%+v err=%v", result.generation, result.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for legitimate paused Catalog builder completion")
		}
		current := loadPreviewFirstCurrentEntry(t, fixture.runtime.CatalogService(), fixture.db, initial.ref.RecoveryPointID)
		if current.generationID == initialGeneration.ID {
			t.Fatalf("legitimate paused builder did not replace superseded generation: %+v", current)
		}
		issueAndServePreviewFirst(t, fixture.runtime.ContentBroker(), fixture.db, current, fixture.clock, fixture.payloads[0])
	})
}

// TestRuntimePreparePreviewSourceRearmsAgedStaleActiveCatalog verifies that a
// healthy mutable source does not merely wake a worker behind a failed retry
// backoff. Preparation must invalidate the stale active generation through the
// existing CAS path, then the ordinary first Issue must serve the exact bytes
// from the newly active generation.
func TestRuntimePreparePreviewSourceRearmsAgedStaleActiveCatalog(t *testing.T) {
	if runtimepkg.GOOS != "linux" {
		t.Skip("local Rsync Provider access requires Linux openat2 support")
	}
	fixture := newPreviewSourcePreparationFixture(t, []byte("aged stale payload"), []byte("sibling payload"))
	initial := fixture.entries[0]
	initialGeneration := fixture.generations[0]
	oldObservedAt := fixture.clock.Add(-2 * time.Hour)
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", initial.ref.RecoveryPointID).
		Update("observed_at", oldObservedAt).Error; err != nil {
		t.Fatalf("age mutable source observation: %v", err)
	}
	var pointBefore model.RecoveryPoint
	if err := fixture.db.First(&pointBefore, "id = ?", initial.ref.RecoveryPointID).Error; err != nil {
		t.Fatalf("load aged mutable point: %v", err)
	}
	if pointBefore.ObservedAt == nil || !pointBefore.ObservedAt.Equal(oldObservedAt) {
		t.Fatalf("aged mutable point observation=%v, want %v", pointBefore.ObservedAt, oldObservedAt)
	}
	failedAt := fixture.clock
	failed := model.CatalogGeneration{
		ID: strings.Repeat("c", 31) + "1", RecoveryPointID: initial.ref.RecoveryPointID,
		Generation: initialGeneration.Generation + 1, State: string(catalog.GenerationFailed),
		IsActive: false, SourceFingerprint: initialGeneration.SourceFingerprint,
		ErrorCode: string(catalog.GenerationErrorBuildFailed),
		StartedAt: failedAt.Add(-time.Minute), FinishedAt: &failedAt,
		CreatedAt: failedAt.Add(-time.Minute), UpdatedAt: failedAt,
	}
	if err := fixture.db.Create(&failed).Error; err != nil {
		t.Fatalf("seed latest failed Catalog attempt: %v", err)
	}
	before := previewPreparationCatalogStatus(t, fixture.runtime.CatalogService(), initial.ref.RecoveryPointID)
	if before.Generation == nil || before.Staleness.Status != catalog.StalenessStale ||
		before.LatestBuild == nil || before.LatestBuild.State != catalog.GenerationFailed {
		t.Fatalf("failed to seed aged stale active Catalog: %+v", before)
	}

	pausedFactory, workerCtx := fixture.configurePausedCatalogWorker(t)
	status, err := fixture.runtime.PreparePreviewSource(context.Background(), previewPreparationAdminActor(), initial.ref)
	if err != nil {
		t.Fatalf("prepare aged stale active Catalog: status=%+v err=%v", status, err)
	}
	var pointAfterPrepare model.RecoveryPoint
	if err := fixture.db.First(&pointAfterPrepare, "id = ?", initial.ref.RecoveryPointID).Error; err != nil {
		t.Fatalf("reload mutable point immediately after stale preparation: %v", err)
	}
	if pointAfterPrepare.SourceFingerprint != pointBefore.SourceFingerprint || pointAfterPrepare.ObservedAt == nil ||
		!pointAfterPrepare.ObservedAt.Equal(*pointBefore.ObservedAt) {
		t.Fatalf("healthy stale preparation refreshed mutable source evidence: before=%+v after=%+v", pointBefore, pointAfterPrepare)
	}
	go fixture.runtime.catalogWorker.Run(workerCtx)
	select {
	case <-pausedFactory.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("healthy stale preparation only woke a backoff-blocked worker")
	}
	gap := previewPreparationCatalogStatus(t, fixture.runtime.CatalogService(), initial.ref.RecoveryPointID)
	if gap.Generation != nil || gap.Coverage.Status != catalog.CoverageBuilding ||
		gap.LatestBuild == nil || gap.LatestBuild.State != catalog.GenerationBuilding {
		t.Fatalf("aged stale active Catalog was not CAS-invalidated into pending/building: %+v", gap)
	}
	if !gap.ContentAvailability.Available {
		t.Fatalf("aged stale mutable source was mislabeled file-unavailable: %+v", gap)
	}
	pausedFactory.Release()
	refreshedGeneration, refreshedEntry := waitForPreviewFirstCatalog(
		t, fixture.db, initial.ref.RecoveryPointID, initialGeneration.ID, fixture.payloads[0],
	)
	if refreshedGeneration.Generation <= failed.Generation || refreshedEntry.EntryID != initial.modelEntry.EntryID {
		t.Fatalf("aged stale rebuild lost exact entry lineage: generation=%+v entry=%+v", refreshedGeneration, refreshedEntry)
	}
	var failedAfter model.CatalogGeneration
	if err := fixture.db.First(&failedAfter, "id = ?", failed.ID).Error; err != nil {
		t.Fatalf("reload superseded failed Catalog attempt: %v", err)
	}
	if failedAfter.State != string(catalog.GenerationSuperseded) || failedAfter.IsActive {
		t.Fatalf("failed Catalog attempt was not superseded by preparation: %+v", failedAfter)
	}
	var initialAfter model.CatalogGeneration
	if err := fixture.db.First(&initialAfter, "id = ?", initialGeneration.ID).Error; err != nil {
		t.Fatalf("reload superseded active Catalog generation: %v", err)
	}
	if initialAfter.State != string(catalog.GenerationSuperseded) || initialAfter.IsActive {
		t.Fatalf("active Catalog generation was not superseded by preparation: %+v", initialAfter)
	}
	issueAndServePreviewFirst(t, fixture.runtime.ContentBroker(), fixture.db, initial, fixture.clock, fixture.payloads[0])
	assertPreviewPreparationGenerationUnchanged(t, fixture.db, fixture.generations[1].RecoveryPointID, fixture.generations[1].ID)
}

// TestRuntimePreparePreviewSourcePreservesFreshCatalogAcrossPendingAuthorization
// exercises the race where pending authorization observes no active generation,
// a fresh complete replacement becomes active before the CAS, and the original
// request resumes. It must re-authorize the exact ref instead of invalidating
// the fresh generation again.
func TestRuntimePreparePreviewSourcePreservesFreshCatalogAcrossPendingAuthorization(t *testing.T) {
	if runtimepkg.GOOS != "linux" {
		t.Skip("local Rsync Provider access requires Linux openat2 support")
	}
	tests := []struct {
		name        string
		deleteEntry bool
	}{
		{name: "entry_exists"},
		{name: "deleted_exact_entry", deleteEntry: true},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPreviewSourcePreparationFixture(t, []byte("pending race payload"), []byte("sibling payload"))
			initial := fixture.entries[0]
			initialGeneration := fixture.generations[0]
			supersedePreviewPreparationGeneration(t, fixture, initialGeneration)
			if testCase.deleteEntry {
				if err := os.Remove(filepath.Join(fixture.targetRoots[0], "docker-compose.yml")); err != nil {
					t.Fatalf("delete target entry before fresh Catalog build: %v", err)
				}
				if err := os.Remove(filepath.Join(fixture.sourceRoots[0], "docker-compose.yml")); err != nil {
					t.Fatalf("delete source entry before fresh Catalog build: %v", err)
				}
				if err := os.Chtimes(fixture.targetRoots[0], fixture.rootInfos[0].ModTime(), fixture.rootInfos[0].ModTime()); err != nil {
					t.Fatalf("preserve target root metadata after exact entry deletion: %v", err)
				}
			}
			pausedFactory, _ := fixture.configurePausedCatalogWorker(t)
			buildDone := make(chan struct {
				generation model.CatalogGeneration
				err        error
			}, 1)
			go func() {
				generation, err := fixture.runtime.catalogIndexer.Build(context.Background(), catalog.BuildRequest{
					RepositoryID:    fixture.points[0].RepositoryID,
					RecoveryPointID: initial.ref.RecoveryPointID,
					CorrelationID:   fmt.Sprintf("preview-preparation-race-%d", index),
				})
				buildDone <- struct {
					generation model.CatalogGeneration
					err        error
				}{generation: generation, err: err}
			}()
			select {
			case <-pausedFactory.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for fresh Catalog builder before pending authorization")
			}
			casEntered := make(chan struct{})
			casResume := make(chan struct{})
			removePause := registerPreviewPreparationCASPause(t, fixture.db, casEntered, casResume)
			var resumeOnce sync.Once
			releaseCAS := func() { resumeOnce.Do(func() { close(casResume) }) }
			defer func() {
				releaseCAS()
				removePause()
			}()
			prepareDone := make(chan struct {
				status catalog.StatusDTO
				err    error
			}, 1)
			go func() {
				status, err := fixture.runtime.PreparePreviewSource(
					context.Background(), previewPreparationAdminActor(), initial.ref,
				)
				prepareDone <- struct {
					status catalog.StatusDTO
					err    error
				}{status: status, err: err}
			}()
			select {
			case <-casEntered:
			case <-time.After(10 * time.Second):
				t.Fatal("pending preview preparation did not pause before mutable CAS")
			}
			pausedFactory.Release()
			var built struct {
				generation model.CatalogGeneration
				err        error
			}
			select {
			case built = <-buildDone:
			case <-time.After(10 * time.Second):
				t.Fatal("timed out activating fresh Catalog generation during pending preparation")
			}
			if built.err != nil || built.generation.State != string(catalog.GenerationComplete) || !built.generation.IsActive {
				t.Fatalf("fresh Catalog build result=%+v err=%v", built.generation, built.err)
			}
			if built.generation.ID == initialGeneration.ID {
				t.Fatalf("fresh Catalog build reused superseded generation: %+v", built.generation)
			}
			if testCase.deleteEntry {
				if _, err := fixture.runtime.CatalogService().GetEntry(
					context.Background(), initial.ref.RecoveryPointID, initial.ref.EntryID,
					catalog.AuthorizationScope{Role: "admin", UserID: 1},
				); !errors.Is(err, backupasset.ErrNotFound) {
					t.Fatalf("fresh deleted Catalog exact entry lookup error=%v, want not found", err)
				}
			} else {
				current := loadPreviewFirstCurrentEntry(t, fixture.runtime.CatalogService(), fixture.db, initial.ref.RecoveryPointID)
				if current.generationID != built.generation.ID || current.ref.EntryID != initial.ref.EntryID {
					t.Fatalf("fresh Catalog exact entry=%+v, want generation=%s ref=%+v", current, built.generation.ID, initial.ref)
				}
			}
			releaseCAS()
			result := <-prepareDone
			if testCase.deleteEntry {
				if !errors.Is(result.err, backupasset.ErrNotFound) {
					t.Fatalf("pending deleted exact entry result status=%+v err=%v, want not found", result.status, result.err)
				}
			} else {
				if result.err != nil || result.status.Generation == nil ||
					result.status.Generation.ID != built.generation.ID ||
					result.status.Coverage.Status != catalog.CoverageComplete ||
					!result.status.ContentAvailability.Available {
					t.Fatalf("pending exact entry result status=%+v err=%v", result.status, result.err)
				}
				current := loadPreviewFirstCurrentEntry(t, fixture.runtime.CatalogService(), fixture.db, initial.ref.RecoveryPointID)
				issueAndServePreviewFirst(t, fixture.runtime.ContentBroker(), fixture.db, current, fixture.clock, fixture.payloads[0])
			}
			var active model.CatalogGeneration
			if err := fixture.db.Where("recovery_point_id = ? AND is_active = ?", initial.ref.RecoveryPointID, true).
				First(&active).Error; err != nil {
				t.Fatalf("load active fresh Catalog generation after pending preparation: %v", err)
			}
			if active.ID != built.generation.ID || active.State != string(catalog.GenerationComplete) {
				t.Fatalf("pending preparation invalidated fresh Catalog generation: %+v", active)
			}
			var old model.CatalogGeneration
			if err := fixture.db.First(&old, "id = ?", initialGeneration.ID).Error; err != nil {
				t.Fatalf("reload superseded initial Catalog generation: %v", err)
			}
			if old.State != string(catalog.GenerationSuperseded) || old.IsActive {
				t.Fatalf("initial Catalog generation was not superseded by fresh build: %+v", old)
			}
		})
	}
}

// TestRuntimePreparePreviewSourceSkipsImmutableManagedRsyncStat verifies that
// an immutable managed Rsync point returns its existing Catalog status without
// probing or repairing the provider source. Removing both real roots makes any
// accidental stat/repair observable while the immutable Catalog remains valid.
func TestRuntimePreparePreviewSourceSkipsImmutableManagedRsyncStat(t *testing.T) {
	if runtimepkg.GOOS != "linux" {
		t.Skip("local Rsync Provider access requires Linux openat2 support")
	}
	fixture := newPreviewSourcePreparationFixture(t, []byte("immutable payload"), []byte("sibling payload"))
	initial := fixture.entries[0]
	initialGeneration := fixture.generations[0]
	if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", fixture.points[0].RepositoryID).
		Updates(map[string]any{
			"version_mode":       string(backupasset.VersionHardlinkTree),
			"immutability_level": string(backupasset.ImmutabilityXirangManaged),
		}).Error; err != nil {
		t.Fatalf("promote Rsync repository to immutable managed mode: %v", err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.points[0].ID).
		Updates(map[string]any{
			"semantics":          string(backupasset.PointXirangManifest),
			"state":              string(backupasset.RecoveryPointCommitted),
			"immutability_level": string(backupasset.ImmutabilityXirangManaged),
			"observed_at":        nil,
		}).Error; err != nil {
		t.Fatalf("promote Rsync point to immutable managed mode: %v", err)
	}
	if err := os.RemoveAll(fixture.targetRoots[0]); err != nil {
		t.Fatalf("remove immutable target root: %v", err)
	}
	if err := os.RemoveAll(fixture.sourceRoots[0]); err != nil {
		t.Fatalf("remove immutable source root: %v", err)
	}
	status, err := fixture.runtime.PreparePreviewSource(context.Background(), previewPreparationAdminActor(), initial.ref)
	if err != nil {
		t.Fatalf("prepare immutable managed Rsync point: status=%+v err=%v", status, err)
	}
	if status.Generation == nil || status.Generation.ID != initialGeneration.ID ||
		status.Coverage.Status != catalog.CoverageComplete ||
		status.Staleness.Status != catalog.StalenessFresh || !status.ContentAvailability.Available {
		t.Fatalf("immutable managed Rsync status=%+v", status)
	}
	assertPreviewPreparationGenerationUnchanged(t, fixture.db, initial.ref.RecoveryPointID, initialGeneration.ID)
	assertPreviewPreparationGenerationUnchanged(t, fixture.db, fixture.generations[1].RecoveryPointID, fixture.generations[1].ID)
}

func supersedePreviewPreparationGeneration(t *testing.T, fixture *previewSourcePreparationFixture, generation model.CatalogGeneration) {
	t.Helper()
	endedAt := fixture.clock
	if err := fixture.db.Model(&model.CatalogGeneration{}).Where("id = ?", generation.ID).
		Updates(map[string]any{
			"state": string(catalog.GenerationSuperseded), "is_active": false,
			"finished_at": endedAt, "updated_at": endedAt,
		}).Error; err != nil {
		t.Fatalf("supersede Catalog generation %s: %v", generation.ID, err)
	}
}

func seedPreviewPreparationBuildingGeneration(
	t *testing.T,
	fixture *previewSourcePreparationFixture,
	initial model.CatalogGeneration,
	startedAt time.Time,
	seed int,
) model.CatalogGeneration {
	t.Helper()
	generation := model.CatalogGeneration{
		ID: fmt.Sprintf("%032x", seed), RecoveryPointID: initial.RecoveryPointID,
		Generation: initial.Generation + 1, State: string(catalog.GenerationBuilding),
		IsActive: false, SourceFingerprint: initial.SourceFingerprint,
		ExpectedEntryCount: initial.ExpectedEntryCount, WrittenEntryCount: initial.WrittenEntryCount,
		ExpectedDigest: initial.ExpectedDigest, WrittenDigest: initial.WrittenDigest,
		CorrelationID: "preview-preparation-orphan",
		StartedAt:     startedAt, CreatedAt: startedAt, UpdatedAt: startedAt,
	}
	if err := fixture.db.Create(&generation).Error; err != nil {
		t.Fatalf("seed orphan Catalog generation: %v", err)
	}
	return generation
}

func seedPreviewPreparationExpiredLease(
	t *testing.T,
	fixture *previewSourcePreparationFixture,
	pointID string,
	startedAt time.Time,
	seed int,
	status backupasset.LeaseStatus,
) {
	t.Helper()
	now := fixture.clock
	lease := model.RecoveryPointLease{
		ID: fmt.Sprintf("%032x", seed), RecoveryPointID: pointID,
		HolderType: string(backupasset.LeaseHolderCatalogBuild), OwnerID: "catalog:" + pointID,
		AttemptID: fmt.Sprintf("%032x", seed+1), FenceToken: strings.Repeat("f", 64),
		Status: string(status), LeaseExpiresAt: now.Add(-time.Minute),
		AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: startedAt,
		CreatedAt: startedAt, UpdatedAt: startedAt,
	}
	if err := fixture.db.Create(&lease).Error; err != nil {
		t.Fatalf("seed expired Catalog lease: %v", err)
	}
}

func registerPreviewPreparationCASPause(
	t *testing.T,
	db *gorm.DB,
	entered chan<- struct{},
	resume <-chan struct{},
) func() {
	t.Helper()
	name := fmt.Sprintf("runtime:preview_preparation_cas_pause:%d", time.Now().UnixNano())
	var pauseOnce sync.Once
	callback := func(tx *gorm.DB) {
		table := strings.ToLower(tx.Statement.Table)
		sql := strings.ToLower(tx.Statement.SQL.String())
		if !strings.Contains(table, "task_repository_links") && !strings.Contains(sql, "task_repository_links") {
			return
		}
		pauseOnce.Do(func() {
			close(entered)
			<-resume
		})
	}
	if err := db.Callback().Query().After("gorm:after_query").Register(name, callback); err != nil {
		t.Fatalf("register preview preparation CAS pause: %v", err)
	}
	return func() {
		if err := db.Callback().Query().Remove(name); err != nil {
			t.Fatalf("remove preview preparation CAS pause: %v", err)
		}
	}
}

type previewSourcePreparationFixture struct {
	db          *gorm.DB
	runtime     *Runtime
	clock       time.Time
	payloads    [][]byte
	tasks       []model.Task
	points      []model.RecoveryPoint
	generations []model.CatalogGeneration
	entries     []previewFirstCatalogEntry
	targetRoots []string
	sourceRoots []string
	rootInfos   []os.FileInfo
}

func newPreviewSourcePreparationFixture(t *testing.T, payloads ...[]byte) *previewSourcePreparationFixture {
	t.Helper()
	if len(payloads) != 2 {
		t.Fatal("preview preparation fixture requires exactly two isolated Rsync sources")
	}
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_PREVIEW_PREPARATION_RUNTIME_DATA_KEY_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	clock := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	db, err := database.Open(config.Config{DBType: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "preview-source-preparation.db")})
	if err != nil {
		t.Fatalf("open SQLite WAL runtime database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.SystemSetting{}, &model.BackupAssetDeliveryGrant{}, &model.BackupAssetDeliveryRequest{},
		&model.BackupAssetDeliveryUsage{}, &model.RecoveryPointLease{}, &model.User{}, &model.Node{}, &model.Task{},
		&model.BackupRepository{}, &model.RepositoryAccessBinding{}, &model.TaskRepositoryLink{}, &model.RecoveryPoint{},
		&model.RecoveryPointLifecycleAttempt{}, &model.CatalogGeneration{}, &model.CatalogEntry{}, &model.WrappedDomainKey{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{}, &model.BackupAssetAuditCheckpoint{},
		&model.BackupAssetAuditEvent{}, &model.BackupAssetInstallation{}, &model.BackupAssetInventoryRun{},
		&model.BackupAssetRepositoryConflict{},
	); err != nil {
		t.Fatalf("migrate preview preparation schema: %v", err)
	}
	if err := db.Create(&model.User{ID: 1, Username: "preview-admin", PasswordHash: "unused", Role: "admin", TokenVersion: 1}).Error; err != nil {
		t.Fatalf("seed preview user: %v", err)
	}
	settingsService := settings.NewService(db)
	for key, value := range map[string]string{
		"backup_assets.enabled":               "true",
		"backup_assets.content_cache_enabled": "false",
	} {
		if err := settingsService.Update(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	readinessDigest := strings.Repeat("e", 64)
	if err := db.Create(&model.BackupAssetInstallation{
		ID: "preview-preparation-installation", Slot: 1, Class: string(ga.InstallationFresh),
		Readiness: string(ga.ReadinessReady), InventoryDigest: readinessDigest, CreatedAt: clock, UpdatedAt: clock,
	}).Error; err != nil {
		t.Fatalf("seed backup asset installation readiness: %v", err)
	}
	if err := db.Create(&model.BackupAssetInventoryRun{
		ID: "preview-preparation-inventory-run", Digest: readinessDigest, Status: ga.InventoryRunComplete,
		CountsJSON: "{}", CreatedAt: clock, UpdatedAt: clock,
	}).Error; err != nil {
		t.Fatalf("seed backup asset inventory readiness: %v", err)
	}

	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settingsService, Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{}, Metrics: publication.NoopMetrics{}, ContentMetrics: content.NoopMetrics{},
		SessionRevocations: &runtimeSessionRevocationsFake{}, Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatalf("construct backup asset runtime: %v", err)
	}
	if _, err := runtime.keyring.EnsureRequiredDomains(context.Background()); err != nil {
		t.Fatalf("ensure runtime key domains: %v", err)
	}
	if err := runtime.admission.InitializeManaged(context.Background()); err != nil {
		t.Fatalf("initialize managed admission: %v", err)
	}
	if err := runtime.contentManager.Startup(context.Background()); err != nil {
		t.Fatalf("start content runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })

	rootBase := t.TempDir()
	node := model.Node{
		Name: "preview-preparation-node", Host: "127.0.0.1", Port: 22, Username: "root",
		AuthType: "password", Password: "FAKE_PREVIEW_PREPARATION_NODE_PASSWORD_FOR_TEST_ONLY",
		BasePath: rootBase, BackupDir: filepath.Join(rootBase, "node-backup"),
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create shared-node fixture: %v", err)
	}
	targetRoots := make([]string, len(payloads))
	sourceRoots := make([]string, len(payloads))
	rootInfos := make([]os.FileInfo, len(payloads))
	tasks := make([]model.Task, len(payloads))
	for index, payload := range payloads {
		targetRoots[index] = filepath.Join(rootBase, fmt.Sprintf("target-%d", index))
		sourceRoots[index] = filepath.Join(rootBase, fmt.Sprintf("source-%d", index))
		for _, root := range []string{targetRoots[index], sourceRoots[index]} {
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatalf("create local Rsync root %q: %v", root, err)
			}
			if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), payload, 0o600); err != nil {
				t.Fatalf("seed local Rsync payload %q: %v", root, err)
			}
		}
		info, err := os.Stat(targetRoots[index])
		if err != nil {
			t.Fatalf("stat target root %d: %v", index, err)
		}
		rootInfos[index] = info
		tasks[index] = model.Task{
			Name: fmt.Sprintf("preview-preparation-task-%d", index), NodeID: node.ID,
			ExecutorType: string(backupasset.ProviderRsync), RsyncSource: sourceRoots[index],
			RsyncTarget: targetRoots[index], Status: "pending", Enabled: true,
		}
		if err := db.Create(&tasks[index]).Error; err != nil {
			t.Fatalf("create task %d: %v", index, err)
		}
	}

	points := make([]model.RecoveryPoint, len(tasks))
	generations := make([]model.CatalogGeneration, len(tasks))
	entries := make([]previewFirstCatalogEntry, len(tasks))
	for index := range tasks {
		connected, connectErr := runtime.RepositoryService().Connect(context.Background(), backuprepository.ConnectRequest{TaskID: tasks[index].ID}, backuprepository.RequestContext{
			CorrelationID: fmt.Sprintf("preview-preparation-connect-%d", index),
		})
		if connectErr != nil || connected.MutablePoint == nil {
			t.Fatalf("connect task %d result=%+v err=%v", index, connected, connectErr)
		}
		if err := db.First(&points[index], "id = ?", connected.MutablePoint.ID).Error; err != nil {
			t.Fatalf("load connected point %d: %v", index, err)
		}
		generation, buildErr := runtime.catalogIndexer.Build(context.Background(), catalog.BuildRequest{
			RepositoryID: connected.Repository.ID, RecoveryPointID: connected.MutablePoint.ID,
			CorrelationID: fmt.Sprintf("preview-preparation-initial-%d", index),
		})
		if buildErr != nil || generation.State != string(catalog.GenerationComplete) || !generation.IsActive {
			t.Fatalf("initial Catalog build %d generation=%+v err=%v", index, generation, buildErr)
		}
		generations[index] = generation
		entries[index] = loadPreviewFirstCurrentEntry(t, runtime.CatalogService(), db, points[index].ID)
	}
	return &previewSourcePreparationFixture{
		db: db, runtime: runtime, clock: clock, payloads: payloads, tasks: tasks,
		points: points, generations: generations, entries: entries,
		targetRoots: targetRoots, sourceRoots: sourceRoots, rootInfos: rootInfos,
	}
}

func (fixture *previewSourcePreparationFixture) configurePausedCatalogWorker(t *testing.T) (*previewFirstPausingCatalogFactory, context.Context) {
	t.Helper()
	catalogConfig, err := fixture.runtime.foundation.CatalogConfig()
	if err != nil {
		t.Fatalf("load Catalog config for paused worker: %v", err)
	}
	factory := &previewFirstPausingCatalogFactory{
		inner: fixture.runtime.RepositoryService(), entered: make(chan struct{}), release: make(chan struct{}),
	}
	lease, err := backupasset.NewLeaseService(fixture.db, func() time.Time { return fixture.clock }, catalogConfig.Lease)
	if err != nil {
		t.Fatalf("construct paused Catalog lease: %v", err)
	}
	indexer, err := catalog.NewIndexer(catalog.IndexerDependencies{
		DB: fixture.db, Factory: factory, Lease: lease, IdentityKeys: fixture.runtime.keyring,
		Now: func() time.Time { return fixture.clock },
		Config: catalog.IndexerConfig{
			BatchSize: catalogConfig.BatchSize, BuildTimeout: catalogConfig.BuildTimeout,
			MaxEntries: catalogConfig.MaxEntries, HeartbeatInterval: catalogConfig.Lease.Heartbeat,
		},
	})
	if err != nil {
		t.Fatalf("construct paused Catalog indexer: %v", err)
	}
	fixture.runtime.catalogIndexer = indexer
	fixture.runtime.catalogWorker.backend = indexer
	fixture.runtime.catalogWorker.after = func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	workerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		factory.Release()
	})
	return factory, workerCtx
}

func (fixture *previewSourcePreparationFixture) startPausedCatalogWorker(t *testing.T) *previewFirstPausingCatalogFactory {
	t.Helper()
	factory, workerCtx := fixture.configurePausedCatalogWorker(t)
	go fixture.runtime.catalogWorker.Run(workerCtx)
	return factory
}

func previewPreparationAdminActor() content.DeliveryActor {
	return content.DeliveryActor{UserID: 1, Username: "preview-admin", Role: "admin"}
}

func previewPreparationCatalogStatus(t *testing.T, service *catalog.Service, pointID string) catalog.StatusDTO {
	t.Helper()
	status, err := service.GetCatalogStatus(context.Background(), pointID, catalog.AuthorizationScope{Role: "admin", UserID: 1})
	if err != nil {
		t.Fatalf("load Catalog status for point %s: %v", pointID, err)
	}
	return status
}

func waitForPreviewPreparationMissingEntry(
	t *testing.T,
	db *gorm.DB,
	service *catalog.Service,
	ref backupasset.AssetRef,
	oldGenerationID string,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var generations []model.CatalogGeneration
		if err := db.Where("recovery_point_id = ?", ref.RecoveryPointID).
			Order("generation DESC").Find(&generations).Error; err != nil {
			t.Fatalf("poll deleted-entry Catalog generations: %v", err)
		}
		for _, generation := range generations {
			if generation.ID == oldGenerationID || generation.State != string(catalog.GenerationComplete) || !generation.IsActive {
				continue
			}
			var count int64
			if err := db.Model(&model.CatalogEntry{}).
				Where("generation_id = ? AND recovery_point_id = ? AND entry_id = ?", generation.ID, ref.RecoveryPointID, ref.EntryID).
				Count(&count).Error; err != nil {
				t.Fatalf("count deleted-entry Catalog row: %v", err)
			}
			if count != 0 {
				continue
			}
			var old model.CatalogGeneration
			if err := db.First(&old, "id = ?", oldGenerationID).Error; err != nil {
				t.Fatalf("reload superseded old Catalog generation: %v", err)
			}
			if old.State != string(catalog.GenerationSuperseded) || old.IsActive {
				t.Fatalf("old Catalog generation was not superseded before exact 404 proof: %+v", old)
			}
			_, err := service.GetEntry(context.Background(), ref.RecoveryPointID, ref.EntryID, catalog.AuthorizationScope{Role: "admin", UserID: 1})
			if errors.Is(err, backupasset.ErrNotFound) {
				return
			}
			if err != nil {
				t.Fatalf("lookup deleted exact current entry: %v", err)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for exact deleted entry to disappear from current Catalog")
}

func assertPreviewPreparationGenerationUnchanged(t *testing.T, db *gorm.DB, pointID, expectedID string) {
	t.Helper()
	var generations []model.CatalogGeneration
	if err := db.Where("recovery_point_id = ?", pointID).Order("generation ASC").Find(&generations).Error; err != nil {
		t.Fatalf("load Catalog generations for point %s: %v", pointID, err)
	}
	if len(generations) != 1 || generations[0].ID != expectedID || generations[0].State != string(catalog.GenerationComplete) || !generations[0].IsActive {
		t.Fatalf("unexpected Catalog mutation for point %s: %+v", pointID, generations)
	}
}
