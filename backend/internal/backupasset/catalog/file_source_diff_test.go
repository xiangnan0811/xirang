package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/retention"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type comparisonFailingKeys struct {
	CursorKeySource
	versionErr error
	activeErr  error
}

func (keys comparisonFailingKeys) ByVersion(ctx context.Context, domain backupasset.KeyDomain, version int) (backupasset.DomainKeyMaterial, error) {
	if keys.versionErr != nil {
		return backupasset.DomainKeyMaterial{}, keys.versionErr
	}
	return keys.CursorKeySource.ByVersion(ctx, domain, version)
}

func (keys comparisonFailingKeys) Active(ctx context.Context, domain backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error) {
	if keys.activeErr != nil {
		return backupasset.DomainKeyMaterial{}, keys.activeErr
	}
	return keys.CursorKeySource.Active(ctx, domain)
}

func runFileSourceComparisonBehavior(t *testing.T, db, observer *gorm.DB) {
	t.Helper()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)
	var original model.RecoveryPoint
	if err := db.Where("id = ?", fixture.ownedPointID).Take(&original).Error; err != nil {
		t.Fatal(err)
	}
	task := seedCatalogOwnershipTask(t, db, original.ProducingNodeIDSnapshot, "comparison-task", now)
	repositories := []model.BackupRepository{
		{ID: strings.Repeat("7", 32), ProviderKind: string(backupasset.ProviderRclone), VersionMode: string(backupasset.VersionVersionedPrefix)},
		{ID: strings.Repeat("8", 32), ProviderKind: string(backupasset.ProviderRsync), VersionMode: string(backupasset.VersionFullCopyTree)},
	}
	for index := range repositories {
		repository := &repositories[index]
		repository.DisplayName = "comparison-" + repository.ProviderKind
		repository.Status = string(backupasset.RepositoryOnline)
		repository.CapabilityRevision = 1
		repository.CapabilitiesJSON = `{"list":true,"open_sequential":true}`
		repository.ImmutabilityLevel = string(backupasset.ImmutabilityXirangManaged)
		repository.CreatedAt, repository.UpdatedAt = now, now
		if err := db.Create(repository).Error; err != nil {
			t.Fatal(err)
		}
	}
	switchAt := now.Add(time.Minute)
	baseLink := seedCatalogOwnershipLink(t, db, strings.Repeat("6", 32), repositories[0].ID, task.ID, &switchAt, now.Add(-time.Hour))
	baseRun := seedCatalogOwnershipRun(t, db, task.ID, now)
	base := seedFileSourceTaskPoint(t, db, 80, repositories[0], task, baseRun, baseLink, backupasset.PointXirangManifest, "", now)
	compareLink := seedCatalogOwnershipLink(t, db, strings.Repeat("9", 32), repositories[1].ID, task.ID, nil, switchAt)
	compareAt := switchAt.Add(time.Minute)
	compareRun := seedCatalogOwnershipRun(t, db, task.ID, compareAt)
	compare := seedFileSourceTaskPoint(t, db, 81, repositories[1], task, compareRun, compareLink, backupasset.PointXirangManifest, "", compareAt)
	baseGeneration := seedCatalogServiceGeneration(t, db, base.ID, 1, true, GenerationComplete, now)
	compareGeneration := seedCatalogServiceGeneration(t, db, compare.ID, 1, true, GenerationComplete, now)
	for _, pair := range []struct {
		generation  model.CatalogGeneration
		fingerprint string
	}{{baseGeneration, base.SourceFingerprint}, {compareGeneration, compare.SourceFingerprint}} {
		if err := db.Model(&model.CatalogGeneration{}).Where("id = ?", pair.generation.ID).Updates(map[string]any{
			"source_fingerprint": pair.fingerprint, "expected_digest": strings.Repeat("b", 64), "written_digest": strings.Repeat("b", 64),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	baseDir := seedCatalogServiceEntry(t, db, baseGeneration, fmt.Sprintf("%064x", 1000), nil, "folder", "folder", backupasset.CatalogEntryDirectory, 0, now)
	producerKey := []byte(strings.Repeat("k", 32))
	compareDir := seedCatalogServiceEntry(t, db, compareGeneration, deriveEntryID(producerKey, compare.ID, "folder"), nil, "folder", "folder", backupasset.CatalogEntryDirectory, 0, now)
	// Shared weak values differ but do not prove a content change.
	weakBase := seedCatalogServiceEntry(t, db, baseGeneration, fmt.Sprintf("%064x", 1001), &baseDir.EntryID, "folder/weak", "weak", backupasset.CatalogEntryFile, 4, now)
	weakCompare := seedCatalogServiceEntry(t, db, compareGeneration, fmt.Sprintf("%064x", 2001), &compareDir.EntryID, "folder/weak", "weak", backupasset.CatalogEntryFile, 4, now)
	if err := db.Model(&model.CatalogEntry{}).Where("entry_id IN ?", []string{weakBase.EntryID, weakCompare.EntryID}).Updates(map[string]any{"fingerprint_strength": "weak"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CatalogEntry{}).Where("generation_id = ? AND entry_id = ?", compareGeneration.ID, weakCompare.EntryID).Update("fingerprint", "weak-other").Error; err != nil {
		t.Fatal(err)
	}
	strongBase := seedCatalogServiceEntry(t, db, baseGeneration, fmt.Sprintf("%064x", 1002), &baseDir.EntryID, "folder/strong", "strong", backupasset.CatalogEntryFile, 5, now)
	strongCompare := seedCatalogServiceEntry(t, db, compareGeneration, fmt.Sprintf("%064x", 2002), &compareDir.EntryID, "folder/strong", "strong", backupasset.CatalogEntryFile, 5, now)
	if err := db.Model(&model.CatalogEntry{}).Where("generation_id = ? AND entry_id = ?", compareGeneration.ID, strongCompare.EntryID).Update("fingerprint", strings.Repeat("e", 64)).Error; err != nil {
		t.Fatal(err)
	}
	for index := range 205 {
		name := fmt.Sprintf("new-%03d", index)
		if index == 0 {
			record := sealedCatalogRecordForTest(t, "folder/"+name, "folder", name, backupasset.CatalogEntryFile, 1)
			record.Owner, record.Mode, record.MIMEType = "FAKE_PRIVATE_METADATA_CANARY", "secret-mode", "secret-mime"
			indexer := &Indexer{now: func() time.Time { return now }}
			entry, err := indexer.catalogEntryFromRecord(producerKey, compareGeneration, record, &IdentityRegistry{}, map[string]string{"folder": compareDir.EntryID})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&entry).Error; err != nil {
				t.Fatal(err)
			}
		} else {
			seedCatalogServiceEntry(t, db, compareGeneration, fmt.Sprintf("%064x", 3000+index), &compareDir.EntryID, "folder/"+name, name, backupasset.CatalogEntryFile, 1, now)
		}
	}
	for _, pair := range []struct {
		point      model.RecoveryPoint
		generation model.CatalogGeneration
		count      int64
		manifestID string
	}{
		{base, baseGeneration, 3, strings.Repeat("d", 31) + "1"},
		{compare, compareGeneration, 208, strings.Repeat("d", 31) + "2"},
	} {
		manifest := model.RecoveryPointManifest{ID: pair.manifestID, RecoveryPointID: pair.point.ID, Revision: 1,
			DigestAlgorithm: "sha256", Digest: pair.point.ManifestDigest, Generator: "comparison-test", GeneratorVersion: "1",
			Completeness: string(backupasset.ManifestComplete), EntryCount: pair.count, FidelityJSON: "{}", IsActive: true, CreatedAt: now, UpdatedAt: now}
		if err := db.Create(&manifest).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", pair.point.ID).Update("entry_count", pair.count).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.CatalogGeneration{}).Where("id = ?", pair.generation.ID).Updates(map[string]any{
			"manifest_id": pair.manifestID, "expected_entry_count": pair.count, "written_entry_count": pair.count,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
	resolved, err := service.ResolveFileSourceRecoveryPoint(context.Background(), base.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	setID := resolved.BackupSetID
	versions, err := service.ListFileSourceVersions(context.Background(), setID, scope, FileSourcePageRequest{})
	if err != nil || len(versions.Items) != 2 {
		t.Fatalf("versions=%+v error=%v", versions, err)
	}
	for _, version := range versions.Items {
		if version.ComparisonState != FileSourceComparisonEligible {
			t.Fatalf("candidate=%+v", version)
		}
	}
	request := FileSourceDiffRequest{BaseRecoveryPointID: base.ID, CompareRecoveryPointID: compare.ID, Limit: 100}
	var all []FileSourceDiffItem
	var firstCursor string
	for {
		page, err := service.DiffFileSource(context.Background(), setID, scope, request)
		if err != nil {
			t.Fatalf("comparison page: %v", err)
		}
		if page.BaseRecoveryPointID != base.ID || page.CompareRecoveryPointID != compare.ID {
			t.Fatalf("wrong pair: %+v", page)
		}
		all = append(all, page.Items...)
		if firstCursor == "" {
			firstCursor = page.NextCursor
		}
		if page.NextCursor == "" {
			break
		}
		request.Cursor = page.NextCursor
	}
	if len(all) != 206 || firstCursor == "" {
		t.Fatalf("expected 205 additions and one strong change, got %d", len(all))
	}
	for _, item := range all {
		if item.Compare == nil || len(item.Compare.Breadcrumb) != 1 || item.Compare.Breadcrumb[0].EntryID != compareDir.EntryID {
			t.Fatalf("invalid ancestor projection: %+v", item)
		}
		if item.Compare.EntryID == strongCompare.EntryID {
			if item.Base == nil || item.Base.EntryID != strongBase.EntryID || item.ContentEquality != ContentDifferent || len(item.ChangedFields) != 1 || item.ChangedFields[0] != "content" {
				t.Fatalf("strong comparison=%+v", item)
			}
		} else if item.Kind != DiffAdded || item.Base != nil || item.ContentEquality != ContentUnknown || len(item.ChangedFields) != 0 {
			t.Fatalf("addition=%+v", item)
		}
	}
	payload, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"folder/", strongBase.Fingerprint, "provider_locator", "normalized_path", "FAKE_PRIVATE_METADATA_CANARY", "secret-mode", "secret-mime", `"owner"`, `"mode"`, `"mime_type"`} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("comparison disclosed %q", secret)
		}
	}
	if strings.Contains(string(payload), `"omitted_ancestor_names"`) {
		t.Fatal("untruncated comparison disclosed omitted ancestor names")
	}
	request.Cursor = firstCursor
	// Use the runtime pool that did not run migrations. The PostgreSQL migrate
	// driver retains a dedicated connection on the pool that ran Up.
	oneConnection := newCatalogServiceForTest(t, observer, now)
	sqlDB, err := observer.DB()
	if err != nil {
		t.Fatal(err)
	}
	previousMax := sqlDB.Stats().MaxOpenConnections
	sqlDB.SetMaxOpenConns(1)
	before := sqlDB.Stats()
	page, comparisonErr := oneConnection.DiffFileSource(context.Background(), setID, scope, request)
	after := sqlDB.Stats()
	sqlDB.SetMaxOpenConns(previousMax)
	if comparisonErr != nil || len(page.Items) != request.Limit || page.NextCursor == "" || before.InUse != 0 || after.InUse != 0 {
		t.Fatalf("one-connection continuation: items=%d cursor=%t error=%v pool before=%+v after=%+v", len(page.Items), page.NextCursor != "", comparisonErr, before, after)
	}
	if err := db.Where("node_id = ? AND user_id = ?", task.NodeID, fixture.operatorID).Delete(&model.NodeOwner{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.DiffFileSource(context.Background(), setID, scope, request); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("revoked ownership: %v", err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: task.NodeID, UserID: fixture.operatorID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	holds, err := retention.NewHoldService(retention.HoldServiceDependencies{DB: db, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	actor := backupasset.AuditActor{UserID: fixture.operatorID, Username: "catalog-operator", Role: "admin"}
	hold, err := holds.Create(context.Background(), retention.CreateHoldRequest{Actor: actor, RecoveryPointID: compare.ID, HoldType: backupasset.RecoveryPointHoldLegal, Reason: "comparison hold"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DiffFileSource(context.Background(), setID, scope, request); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("hold creation continuation: %v", err)
	}
	request.Cursor = ""
	held, err := service.DiffFileSource(context.Background(), setID, scope, request)
	if err != nil || held.NextCursor == "" {
		t.Fatalf("held page: %v", err)
	}
	if _, err := holds.Release(context.Background(), retention.ReleaseHoldRequest{Actor: actor, RecoveryPointID: compare.ID, HoldID: hold.ID, Reason: "comparison release"}); err != nil {
		t.Fatal(err)
	}
	request.Cursor = held.NextCursor
	if _, err := service.DiffFileSource(context.Background(), setID, scope, request); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("hold release continuation: %v", err)
	}
	request.Cursor = ""
	fresh, err := service.DiffFileSource(context.Background(), setID, scope, request)
	if err != nil || fresh.NextCursor == "" {
		t.Fatalf("fresh page: %v", err)
	}
	firstCursor, request.Cursor = fresh.NextCursor, fresh.NextCursor
	transient := errors.New("injected transient database failure")
	originalCodec := service.cursor
	codec := *originalCodec
	codec.keys = comparisonFailingKeys{CursorKeySource: originalCodec.keys, versionErr: transient}
	service.cursor = &codec
	if _, err := service.DiffFileSource(context.Background(), setID, scope, request); !errors.Is(err, ErrCatalogUnavailable) || !errors.Is(err, transient) {
		t.Fatalf("cursor key transient: %v", err)
	}
	service.cursor = originalCodec
	if err := db.Callback().Query().Before("gorm:query").Register("comparison:anchor-failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "catalog_entries" {
			tx.AddError(transient)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, anchorErr := service.DiffFileSource(context.Background(), setID, scope, request)
	if err := db.Callback().Query().Remove("comparison:anchor-failure"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(anchorErr, ErrCatalogUnavailable) || !errors.Is(anchorErr, transient) {
		t.Fatalf("anchor transient: %v", anchorErr)
	}
	if _, err := service.DiffFileSource(context.Background(), setID, scope, request); err != nil {
		t.Fatalf("retry same cursor: %v", err)
	}
	codec.keys = comparisonFailingKeys{CursorKeySource: originalCodec.keys, activeErr: transient}
	service.cursor = &codec
	firstRequest := request
	firstRequest.Cursor = ""
	if _, err := service.DiffFileSource(context.Background(), setID, scope, firstRequest); !errors.Is(err, ErrCatalogUnavailable) || !errors.Is(err, transient) {
		t.Fatalf("cursor signing transient: %v", err)
	}
	service.cursor = originalCodec
	anchorClaims, err := originalCodec.decodeAuthenticated(context.Background(), request.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	anchorID := anchorClaims.Anchor.CompareEntryID
	var savedAnchor model.CatalogEntry
	if err := db.Where("generation_id = ? AND entry_id = ?", compareGeneration.ID, anchorID).Take(&savedAnchor).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("generation_id = ? AND entry_id = ?", compareGeneration.ID, anchorID).Delete(&model.CatalogEntry{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.DiffFileSource(context.Background(), setID, scope, request); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("missing anchor: %v", err)
	}
	if err := db.Create(&savedAnchor).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.DiffFileSource(context.Background(), setID, scope, FileSourceDiffRequest{BaseRecoveryPointID: base.ID, CompareRecoveryPointID: base.ID}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("same point error=%v", err)
	}
	if _, err := service.DiffFileSource(context.Background(), strings.Repeat("f", 32), scope, FileSourceDiffRequest{BaseRecoveryPointID: base.ID, CompareRecoveryPointID: compare.ID}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("different set error=%v", err)
	}
	if _, err := service.DiffFileSource(context.Background(), setID, scope, FileSourceDiffRequest{BaseRecoveryPointID: base.ID, CompareRecoveryPointID: fixture.unlinkedPointID}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("other lineage error=%v", err)
	}
	if _, err := service.DiffFileSource(context.Background(), setID, scope, FileSourceDiffRequest{BaseRecoveryPointID: base.ID, CompareRecoveryPointID: fixture.unownedPointID}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("unowned point error=%v", err)
	}
	if _, err := service.DiffFileSource(context.Background(), setID, AuthorizationScope{Role: "operator", UserID: fixture.operatorID + 1}, request); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("cursor ownership replay error=%v", err)
	}
	tampered := request
	tampered.Cursor = firstCursor + "!"
	if _, err := service.DiffFileSource(context.Background(), setID, scope, tampered); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tampered cursor error=%v", err)
	}
	if err := db.Model(&model.CatalogGeneration{}).Where("id = ?", compareGeneration.ID).Updates(map[string]any{"is_active": false, "state": GenerationSuperseded}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.DiffFileSource(context.Background(), setID, scope, request); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("generation continuation error=%v", err)
	}
	versions, err = service.ListFileSourceVersions(context.Background(), setID, scope, FileSourcePageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range versions.Items {
		if version.RecoveryPointID == compare.ID && version.ComparisonState != FileSourceComparisonIndexing {
			t.Fatalf("indexing candidate=%+v", version)
		}
	}
	if err := db.Model(&model.CatalogGeneration{}).Where("id = ?", compareGeneration.ID).Updates(map[string]any{"is_active": true, "state": GenerationComplete}).Error; err != nil {
		t.Fatal(err)
	}
	if err := observer.Model(&model.BackupRepository{}).Where("id = ?", compare.RepositoryID).Update("status", backupasset.RepositoryOffline).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.DiffFileSource(context.Background(), setID, scope, request); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("availability change error=%v", err)
	}
	request.Cursor = ""
	offline, err := service.DiffFileSource(context.Background(), setID, scope, request)
	if err != nil || offline.CompareContentAvailability.Available || offline.CompareContentAvailability.Reason == nil || offline.CompareContentAvailability.Reason.Code != backupasset.CapabilityRepositoryOffline {
		t.Fatalf("offline comparison=%+v error=%v", offline, err)
	}
	request.Cursor = offline.NextCursor
	identityKey, err := service.fileSourceIdentityKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claims, err := service.cursor.decodeAuthenticated(context.Background(), request.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	err = database.WithReadonlySnapshot(context.Background(), db, func(tx *gorm.DB) error {
		snapshot := *service
		snapshot.db = tx
		snapshot.ownership = &Ownership{db: tx}
		if _, err := snapshot.ownership.AuthorizedPointIDs(context.Background(), scope, []string{base.ID, compare.ID}); err != nil {
			return err
		}
		if err := observer.Model(&model.BackupRepository{}).Where("id = ?", compare.RepositoryID).Update("status", backupasset.RepositoryOnline).Error; err != nil {
			return err
		}
		var next *CursorScope
		page, err := snapshot.diffFileSourceSnapshot(context.Background(), setID, scope, request, 100, identityKey, &claims, &next)
		if err != nil {
			return err
		}
		if page.CompareContentAvailability.Available {
			return fmt.Errorf("snapshot observed changed repository")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("in-flight snapshot: %v", err)
	}
	if _, err := service.DiffFileSource(context.Background(), setID, scope, request); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("fresh snapshot error=%v", err)
	}
	if err := observer.Model(&model.BackupRepository{}).Where("id = ?", compare.RepositoryID).Update("status", backupasset.RepositoryOffline).Error; err != nil {
		t.Fatal(err)
	}
	attempt := model.RecoveryPointLifecycleAttempt{ID: strings.Repeat("e", 32), RecoveryPointID: compare.ID, Operation: string(backupasset.LifecycleExplicitPurge), Phase: string(backupasset.LifecyclePhaseRevoking), TransitionRevision: 1, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	request.Cursor = offline.NextCursor
	if _, err := service.DiffFileSource(context.Background(), setID, scope, request); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("lifecycle continuation error=%v", err)
	}
	firstWhileBlocked := request
	firstWhileBlocked.Cursor = ""
	if _, err := service.DiffFileSource(context.Background(), setID, scope, firstWhileBlocked); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("lifecycle first page: %v", err)
	}
	versions, err = service.ListFileSourceVersions(context.Background(), setID, scope, FileSourcePageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range versions.Items {
		if version.RecoveryPointID == compare.ID && version.ComparisonState != FileSourceComparisonIneligible {
			t.Fatalf("lifecycle candidate=%+v", version)
		}
	}
	if err := db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", attempt.ID).Updates(map[string]any{"phase": backupasset.LifecyclePhaseComplete, "completed_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("generation_id = ? AND name LIKE ?", compareGeneration.ID, "new-%").Delete(&model.CatalogEntry{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CatalogEntry{}).Where("generation_id = ? AND entry_id = ?", compareGeneration.ID, strongCompare.EntryID).Update("fingerprint", strongBase.Fingerprint).Error; err != nil {
		t.Fatal(err)
	}
	setCount := func(point model.RecoveryPoint, generation model.CatalogGeneration, count int64) {
		t.Helper()
		if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("entry_count", count).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.RecoveryPointManifest{}).Where("recovery_point_id = ? AND is_active = ?", point.ID, true).Update("entry_count", count).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.CatalogGeneration{}).Where("id = ?", generation.ID).Updates(map[string]any{"expected_entry_count": count, "written_entry_count": count}).Error; err != nil {
			t.Fatal(err)
		}
	}
	setCount(compare, compareGeneration, 3)
	request.Cursor = ""
	empty, err := service.DiffFileSource(context.Background(), setID, scope, request)
	if err != nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Fatalf("empty comparison=%+v error=%v", empty, err)
	}
	if err := db.Model(&model.CatalogEntry{}).Where("generation_id = ? AND entry_id = ?", compareGeneration.ID, weakCompare.EntryID).Updates(map[string]any{"size": 6, "fingerprint_strength": "strong"}).Error; err != nil {
		t.Fatal(err)
	}
	unknown, err := service.DiffFileSource(context.Background(), setID, scope, request)
	if err != nil || len(unknown.Items) != 1 || unknown.Items[0].ContentEquality != ContentUnknown || len(unknown.Items[0].ChangedFields) != 1 || unknown.Items[0].ChangedFields[0] != "size" {
		t.Fatalf("weak comparison=%+v error=%v", unknown, err)
	}
	if err := db.Model(&model.CatalogEntry{}).Where("generation_id = ? AND entry_id = ?", compareGeneration.ID, weakCompare.EntryID).Updates(map[string]any{"size": 4, "fingerprint_strength": "weak"}).Error; err != nil {
		t.Fatal(err)
	}
	for sideIndex, generation := range []model.CatalogGeneration{baseGeneration, compareGeneration} {
		for directoryIndex, name := range []string{"left", "right"} {
			directoryID := fmt.Sprintf("%064x", 5000+sideIndex*100+directoryIndex*10)
			directory := seedCatalogServiceEntry(t, db, generation, directoryID, nil, name, name, backupasset.CatalogEntryDirectory, 0, now)
			size := int64(1)
			if sideIndex == 1 && name == "right" {
				size = 2
			}
			seedCatalogServiceEntry(t, db, generation, fmt.Sprintf("%064x", 5001+sideIndex*100+directoryIndex*10), &directory.EntryID, name+"/same", "same", backupasset.CatalogEntryFile, size, now)
		}
	}
	setCount(base, baseGeneration, 7)
	setCount(compare, compareGeneration, 7)
	nested, err := service.DiffFileSource(context.Background(), setID, scope, request)
	if err != nil || len(nested.Items) != 1 || nested.Items[0].Compare == nil || len(nested.Items[0].Compare.Breadcrumb) != 1 || nested.Items[0].Compare.Breadcrumb[0].Name != "right" {
		t.Fatalf("same basename comparison=%+v error=%v", nested, err)
	}
	seedCatalogServiceEntry(t, db, baseGeneration, fmt.Sprintf("%064x", 7000), nil, "old-name", "old-name", backupasset.CatalogEntryFile, 1, now)
	seedCatalogServiceEntry(t, db, compareGeneration, fmt.Sprintf("%064x", 7001), nil, "new-name", "new-name", backupasset.CatalogEntryFile, 1, now)
	seedCatalogServiceEntry(t, db, baseGeneration, fmt.Sprintf("%064x", 7002), nil, "type", "type", backupasset.CatalogEntryFile, 1, now)
	seedCatalogServiceEntry(t, db, compareGeneration, fmt.Sprintf("%064x", 7003), nil, "type", "type", backupasset.CatalogEntryDirectory, 0, now)
	setCount(base, baseGeneration, 9)
	setCount(compare, compareGeneration, 9)
	directed, err := service.DiffFileSource(context.Background(), setID, scope, request)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]DiffChangeKind{}
	for _, item := range directed.Items {
		if item.Base != nil {
			kinds[item.Base.Name] = item.Kind
		} else {
			kinds[item.Compare.Name] = item.Kind
		}
	}
	if kinds["old-name"] != DiffRemoved || kinds["new-name"] != DiffAdded || kinds["type"] != DiffTypeChanged {
		t.Fatalf("directed changes: %+v", kinds)
	}
	reverse, err := service.DiffFileSource(context.Background(), setID, scope, FileSourceDiffRequest{BaseRecoveryPointID: compare.ID, CompareRecoveryPointID: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range reverse.Items {
		if item.Base != nil && item.Base.Name == "new-name" && item.Kind != DiffRemoved {
			t.Fatalf("reverse removal: %+v", item)
		}
		if item.Compare != nil && item.Compare.Name == "old-name" && item.Kind != DiffAdded {
			t.Fatalf("reverse addition: %+v", item)
		}
	}
	duplicate := seedCatalogServiceEntry(t, db, compareGeneration, fmt.Sprintf("%064x", 7004), nil, "type", "type", backupasset.CatalogEntryDirectory, 0, now)
	if _, err := service.DiffFileSource(context.Background(), setID, scope, request); !errors.Is(err, ErrInvalidCatalogContract) {
		t.Fatalf("duplicate path: %v", err)
	}
	if err := db.Where("generation_id = ? AND entry_id = ?", compareGeneration.ID, duplicate.EntryID).Delete(&model.CatalogEntry{}).Error; err != nil {
		t.Fatal(err)
	}
	var parent *string
	path := "deep"
	for index := range 257 {
		if index > 0 {
			path += "/d"
		}
		name := "d"
		if index == 0 {
			name = "deep"
		}
		dir := seedCatalogServiceEntry(t, db, compareGeneration, fmt.Sprintf("%064x", 10000+index), parent, path, name, backupasset.CatalogEntryDirectory, 0, now)
		parent = &dir.EntryID
	}
	leaf := seedCatalogServiceEntry(t, db, compareGeneration, fmt.Sprintf("%064x", 11000), parent, path+"/ ", " ", backupasset.CatalogEntryFile, 1, now)
	setCount(compare, compareGeneration, 267)
	request.Cursor = ""
	request.Limit = 200
	var found bool
	for {
		page, err := service.DiffFileSource(context.Background(), setID, scope, request)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.Compare != nil && item.Compare.EntryID == leaf.EntryID {
				found = true
				if item.Compare.Name != " " || !item.Compare.BreadcrumbTruncated || len(item.Compare.OmittedAncestorNames) != 1 || item.Compare.OmittedAncestorNames[0] != "deep" || len(item.Compare.Breadcrumb) != 256 || item.Compare.Breadcrumb[0].Name != "d" || item.Compare.Breadcrumb[0].RecoveryPointID != compare.ID || item.Compare.Breadcrumb[255].EntryID != *parent {
					t.Fatalf("deep breadcrumb: %+v", item.Compare)
				}
				wire, err := json.Marshal(item.Compare)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(wire), `"omitted_ancestor_names":["deep"]`) || strings.Contains(string(wire), "deep/d/") {
					t.Fatalf("invalid truncated comparison projection: %s", wire)
				}
			}
		}
		if page.NextCursor == "" {
			break
		}
		request.Cursor = page.NextCursor
	}
	if !found {
		t.Fatal("deep entry missing")
	}
	if err := db.Model(&model.CatalogEntry{}).Where("generation_id = ? AND entry_id = ?", compareGeneration.ID, leaf.EntryID).Update("name", "invalid/name").Error; err != nil {
		t.Fatal(err)
	}
	request.Cursor = ""
	for {
		page, err := service.DiffFileSource(context.Background(), setID, scope, request)
		if errors.Is(err, ErrInvalidCatalogContract) {
			break
		}
		if err != nil || page.NextCursor == "" {
			t.Fatalf("corrupt name was not rejected: %v", err)
		}
		request.Cursor = page.NextCursor
	}
	assertSharedDeepComparisonBreadcrumbs(t, db, service, compareGeneration, now)
	assertArchivedFileSourceComparison(t, db, service, fixture, now)
}

func assertArchivedFileSourceComparison(t *testing.T, db *gorm.DB, service *Service, fixture catalogOwnershipFixture, now time.Time) {
	t.Helper()
	var base model.RecoveryPoint
	if err := db.Where("id = ?", fixture.archivedPointID).Take(&base).Error; err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err := db.Where("id = ?", *base.ProducingTaskID).Take(&task).Error; err != nil {
		t.Fatal(err)
	}
	lineage, err := backupasset.DecodePublicationLineage(base.LineageJSON)
	if err != nil {
		t.Fatal(err)
	}
	var link model.TaskRepositoryLink
	if err := db.Where("id = ?", lineage.TaskRepositoryLinkID).Take(&link).Error; err != nil {
		t.Fatal(err)
	}
	var repository model.BackupRepository
	if err := db.Where("id = ?", base.RepositoryID).Take(&repository).Error; err != nil {
		t.Fatal(err)
	}
	run := seedCatalogOwnershipRun(t, db, task.ID, now)
	compare := seedFileSourceTaskPoint(t, db, 82, repository, task, run, link, backupasset.PointNativeSnapshot, "", now)
	for index, point := range []model.RecoveryPoint{base, compare} {
		manifestID := fmt.Sprintf("%032x", 88000+index)
		manifest := model.RecoveryPointManifest{ID: manifestID, RecoveryPointID: point.ID, Revision: 1, DigestAlgorithm: "sha256",
			Digest: point.ManifestDigest, Generator: "comparison-test", GeneratorVersion: "1", Completeness: string(backupasset.ManifestComplete),
			FidelityJSON: "{}", IsActive: true, CreatedAt: now, UpdatedAt: now}
		if err := db.Create(&manifest).Error; err != nil {
			t.Fatal(err)
		}
		generation := model.CatalogGeneration{ID: fmt.Sprintf("%032x", 89000+index), RecoveryPointID: point.ID,
			Generation: 1, State: string(GenerationComplete), IsActive: true, SourceFingerprint: point.SourceFingerprint,
			ExpectedDigest: point.ManifestDigest, WrittenDigest: strings.Repeat("a", 64), StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now}
		if err := db.Create(&generation).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.CatalogGeneration{}).Where("id = ?", generation.ID).Updates(map[string]any{
			"manifest_id": manifestID, "source_fingerprint": point.SourceFingerprint, "expected_entry_count": 0,
			"written_entry_count": 0, "expected_digest": point.ManifestDigest, "written_digest": strings.Repeat("a", 64),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	admin := AuthorizationScope{Role: "admin", UserID: 1}
	resolved, err := service.ResolveFileSourceRecoveryPoint(context.Background(), base.ID, admin)
	if err != nil {
		t.Fatal(err)
	}
	request := FileSourceDiffRequest{BaseRecoveryPointID: base.ID, CompareRecoveryPointID: compare.ID}
	page, err := service.DiffFileSource(context.Background(), resolved.BackupSetID, admin, request)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("archived administrator comparison=%+v error=%v", page, err)
	}
	if _, err := service.DiffFileSource(context.Background(), resolved.BackupSetID, AuthorizationScope{Role: "operator", UserID: fixture.operatorID}, request); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("archived operator comparison error=%v", err)
	}
}

// Exercise a page sharing long chains, including paths that differ only above
// the 256-reference tail. Also check the SQL parent/path guard.
func assertSharedDeepComparisonBreadcrumbs(t *testing.T, db *gorm.DB, service *Service, generation model.CatalogGeneration, now time.Time) {
	t.Helper()
	var entries []model.CatalogEntry
	var selected []diffRow
	var leaves [2]string
	var parents [2]string
	roots := []string{"left ", "right "}
	shared := strings.Repeat("c", 240)
	for branch, root := range roots {
		var parent *string
		path := ""
		for depth := range 282 {
			name := "d"
			if depth == 0 {
				name = root
			} else if depth <= 25 {
				name = shared
			}
			if path != "" {
				path += "/"
			}
			path += name
			id := fmt.Sprintf("%064x", 20000+branch*1000+depth)
			entries = append(entries, model.CatalogEntry{GenerationID: generation.ID, RecoveryPointID: generation.RecoveryPointID,
				EntryID: id, ParentEntryID: parent, NormalizedPath: path, Name: name, EntryType: string(backupasset.CatalogEntryDirectory), CreatedAt: now})
			parent = &id
		}
		parents[branch] = *parent
		for index := range 12 {
			name := fmt.Sprintf("file-%02d", index)
			id := fmt.Sprintf("%064x", 30000+branch*1000+index)
			entries = append(entries, model.CatalogEntry{GenerationID: generation.ID, RecoveryPointID: generation.RecoveryPointID,
				EntryID: id, ParentEntryID: parent, NormalizedPath: path + "/" + name, Name: name,
				EntryType: string(backupasset.CatalogEntryFile), Size: 1, CreatedAt: now})
			selected = append(selected, diffRow{RelativePath: path + "/" + name, CompareEntryID: &id})
			if index == 0 {
				leaves[branch] = id
			}
		}
	}
	if err := db.CreateInBatches(entries, 100).Error; err != nil {
		t.Fatal(err)
	}
	crumbs, err := service.loadComparisonBreadcrumbs(context.Background(), generation.RecoveryPointID, generation.ID, selected, false)
	if err != nil {
		t.Fatal(err)
	}
	for branch, leaf := range leaves {
		crumb := crumbs[leaf]
		if !crumb.truncated || len(crumb.items) != 256 || len(crumb.omittedNames) != 26 || crumb.omittedNames[0] != roots[branch] {
			t.Fatalf("shared deep ancestry branch %d did not preserve its omitted names", branch)
		}
		for _, name := range crumb.omittedNames[1:] {
			if name != shared {
				t.Fatal("unexpected omitted ancestor name")
			}
		}
		for _, item := range crumb.items {
			if item.Name != "d" || item.RecoveryPointID != generation.RecoveryPointID {
				t.Fatalf("unexpected deep breadcrumb: %+v", item)
			}
		}
	}
	if err := db.Model(&model.CatalogEntry{}).Where("generation_id = ? AND entry_id = ?", generation.ID, leaves[0]).Update("parent_entry_id", parents[1]).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.loadComparisonBreadcrumbs(context.Background(), generation.RecoveryPointID, generation.ID, selected[:1], false); !errors.Is(err, ErrInvalidCatalogContract) {
		t.Fatalf("mismatched parent path: %v", err)
	}
}

// Set XIRANG_COMPARE_SCALE=1 to exercise the actual comparison SQL against
// isolated, migrated databases with ten thousand entries on each side.
func TestFileSourceComparisonScaleSQLite(t *testing.T) {
	if os.Getenv("XIRANG_COMPARE_SCALE") != "1" {
		t.Skip("set XIRANG_COMPARE_SCALE=1 for the isolated comparison scale probe")
	}
	db, _ := openCatalogBehaviorSQLite(t)
	runFileSourceComparisonScale(t, db)
}

func runFileSourceComparisonScale(t *testing.T, db *gorm.DB) {
	t.Helper()
	db = db.Session(&gorm.Session{Logger: logger.Discard})
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	base := model.RecoveryPoint{ID: fixture.ownedPointID}
	compare := model.RecoveryPoint{ID: fixture.unlinkedPointID}
	baseGeneration := seedCatalogServiceGeneration(t, db, base.ID, 1, true, GenerationComplete, now)
	compareGeneration := seedCatalogServiceGeneration(t, db, compare.ID, 1, true, GenerationComplete, now)
	for _, side := range []struct {
		generation model.CatalogGeneration
		changed    bool
	}{{baseGeneration, false}, {compareGeneration, true}} {
		entries := make([]model.CatalogEntry, 0, 10000)
		for index := range 10000 {
			name := fmt.Sprintf("file-%05d", index)
			size := int64(1)
			if side.changed && index%100 == 0 {
				size = 2
			}
			entries = append(entries, model.CatalogEntry{GenerationID: side.generation.ID, RecoveryPointID: side.generation.RecoveryPointID,
				EntryID: fmt.Sprintf("%064x", index+1), NormalizedPath: name, Name: name, EntryType: "file", Size: size,
				Fingerprint: strings.Repeat("a", 64), FingerprintStrength: "strong", EncryptedProviderLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY", SecurityState: "sealed", CreatedAt: now})
		}
		if err := db.CreateInBatches(entries, 250).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := newCatalogServiceForTest(t, db, now)
	runPage := func(label string, anchor *diffAnchor, limit int) []diffRow {
		t.Helper()
		query, args := service.fileSourceDiffRowsQuery(base, compare, baseGeneration, compareGeneration, anchor, limit+1, true)
		if db.Name() == "postgres" {
			plan, err := db.Raw("EXPLAIN "+query, args...).Rows()
			if err != nil {
				t.Fatal(err)
			}
			for plan.Next() {
				var step string
				if err := plan.Scan(&step); err != nil {
					_ = plan.Close()
					t.Fatal(err)
				}
				if strings.Contains(step, "file-") {
					step = "[synthetic path predicate redacted]"
				}
				t.Logf("%s plan: %s", label, step)
			}
			if err := plan.Err(); err != nil {
				_ = plan.Close()
				t.Fatal(err)
			}
			if err := plan.Close(); err != nil {
				t.Fatal(err)
			}
		} else {
			var plan []struct {
				ID, Parent int
				Detail     string
			}
			if err := db.Raw("EXPLAIN QUERY PLAN "+query, args...).Scan(&plan).Error; err != nil {
				t.Fatal(err)
			}
			for _, step := range plan {
				t.Logf("%s plan: %s", label, step.Detail)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		start := time.Now()
		rows, err := service.queryFileSourceDiffRows(ctx, base, compare, baseGeneration, compareGeneration, anchor, limit+1, true)
		if err != nil {
			t.Fatalf("%s comparison: %v", label, err)
		}
		t.Logf("%s: %d rows in %s", label, len(rows), time.Since(start))
		return rows
	}
	anchorAfter := func(row diffRow) *diffAnchor {
		return &diffAnchor{relativePath: row.RelativePath, rank: row.ChangeRank, baseID: pointerValue(row.BaseEntryID), compareID: pointerValue(row.CompareEntryID)}
	}
	sparseFirst := runPage("sparse first", nil, 50)
	if len(sparseFirst) != 51 {
		t.Fatalf("sparse first page rows=%d", len(sparseFirst))
	}
	sparseNext := runPage("sparse continuation", anchorAfter(sparseFirst[49]), 50)
	if len(sparseNext) != 50 {
		t.Fatalf("sparse continuation rows=%d; want total 100", len(sparseNext))
	}
	if sparseFirst[50].RelativePath != sparseNext[0].RelativePath {
		t.Fatal("sparse continuation skipped or duplicated the page boundary")
	}
	if err := db.Model(&model.CatalogEntry{}).Where("generation_id = ?", compareGeneration.ID).Update("size", 2).Error; err != nil {
		t.Fatal(err)
	}
	denseFirst := runPage("dense first", nil, 200)
	if len(denseFirst) != 201 {
		t.Fatalf("dense first page rows=%d", len(denseFirst))
	}
	denseNext := runPage("dense continuation", anchorAfter(denseFirst[199]), 200)
	if len(denseNext) != 201 {
		t.Fatalf("dense continuation rows=%d", len(denseNext))
	}
	if denseFirst[200].RelativePath != denseNext[0].RelativePath {
		t.Fatal("dense continuation skipped or duplicated the page boundary")
	}
}
