package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestExportCommitZeroDeadlinePersistsExactLeaseAndReplays(t *testing.T) {
	harness := newServiceHarness(t)
	ctx := context.Background()
	pointID := frozenItemFixture().Ref.RecoveryPointID

	other, err := harness.lease.Acquire(ctx, backupasset.AcquireLeaseRequest{
		RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderCatalogBuild,
		OwnerID: "catalog-history-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.lease.Release(ctx, other.Fence); err != nil {
		t.Fatal(err)
	}
	activeOther, err := harness.lease.Acquire(ctx, backupasset.AcquireLeaseRequest{
		RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderProcessingJob,
		OwnerID: "processing-active-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = harness.lease.Release(context.Background(), activeOther.Fence) })

	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	request := CommitCreateRequest{
		Actor: SelectionActor{UserID: 41, Role: "admin"}, Selection: selection,
		IdempotencyKey: "export-create-key-0001", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	}
	created, err := harness.service.CommitCreate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Replay || created.JobID == "" {
		t.Fatalf("created=%+v", created)
	}
	if len(harness.leaseSpy.requests) != 1 || !harness.leaseSpy.requests[0].AbsoluteDeadline.IsZero() {
		t.Fatalf("AcquireTx requests=%+v", harness.leaseSpy.requests)
	}
	var source model.BackupAssetExportSourceLease
	if err := harness.db.Where("job_id = ?", created.JobID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	if !source.AbsoluteDeadline.Equal(harness.leaseSpy.leases[0].AbsoluteDeadline) {
		t.Fatalf("persisted deadline=%s returned=%s", source.AbsoluteDeadline, harness.leaseSpy.leases[0].AbsoluteDeadline)
	}
	var job model.BackupAssetExportJob
	if err := harness.db.Where("id = ?", created.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.LifecycleEnqueueSequence != 1 {
		t.Fatalf("first Export job lifecycle sequence=%d, want globally allocated 1", job.LifecycleEnqueueSequence)
	}
	for _, target := range []any{
		&model.BackupAssetExportJob{}, &model.BackupAssetExportKey{}, &model.BackupAssetExportItem{},
		&model.BackupAssetExportSourceLease{}, &model.BackupAssetExportIdempotency{},
	} {
		var count int64
		if err := harness.db.Model(target).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("%T count=%d err=%v", target, count, err)
		}
	}
	var reservations []model.BackupAssetExportReservation
	if err := harness.db.Where("job_id = ?", created.JobID).Order("bucket_id ASC, kind ASC").Find(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if len(reservations) != 4 {
		t.Fatalf("quota reservations=%d, want global/user job+store", len(reservations))
	}
	itemSpoolBytes, err := ciphertextSizeV1(selection.Items[0].LogicalSize, harness.config.ChunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	worstCaseStore := harness.config.MaxCiphertextBytes + itemSpoolBytes
	kinds := map[string]int{}
	for _, reservation := range reservations {
		kinds[reservation.Kind]++
		switch reservation.Kind {
		case "job":
			if reservation.ReservedSlots != 1 || reservation.ReservedStoreBytes != 0 {
				t.Fatalf("invalid job reservation: %+v", reservation)
			}
		case "store":
			if reservation.ReservedSlots != 0 || reservation.ReservedStoreBytes != worstCaseStore {
				t.Fatalf("invalid store reservation: %+v", reservation)
			}
		default:
			t.Fatalf("unexpected reservation kind: %+v", reservation)
		}
	}
	if kinds["job"] != 2 || kinds["store"] != 2 {
		t.Fatalf("reservation kinds=%v", kinds)
	}
	var buckets []model.BackupAssetExportQuotaBucket
	if err := harness.db.Where("scope IN ?", []string{"global", "user"}).Order("scope ASC").Find(&buckets).Error; err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("quota buckets=%+v", buckets)
	}
	for _, bucket := range buckets {
		if bucket.ActiveJobs != 1 || bucket.ReservedStoreBytes != worstCaseStore {
			t.Fatalf("quota bucket not reserved atomically: %+v", bucket)
		}
		switch bucket.Scope {
		case "global":
			if bucket.Subject != "global" || bucket.LifecycleNextSequence != 2 ||
				bucket.LifecycleSweepCursor != 0 || bucket.LifecycleSweepHighWater != 0 ||
				bucket.LifecycleSweepRevision != 0 || bucket.LifecycleSweepLeaseExpiresAt != nil {
				t.Fatalf("global lifecycle scheduler control plane was not allocated independently: %+v", bucket)
			}
		case "user":
			if bucket.LifecycleNextSequence != 1 || bucket.LifecycleSweepCursor != 0 ||
				bucket.LifecycleSweepHighWater != 0 || bucket.LifecycleSweepRevision != 0 ||
				bucket.LifecycleSweepLeaseExpiresAt != nil {
				t.Fatalf("user quota bucket scheduler fields are not inert defaults: %+v", bucket)
			}
		default:
			t.Fatalf("unexpected quota bucket scope: %+v", bucket)
		}
	}

	replayed, err := harness.service.CommitCreate(ctx, request)
	if err != nil || !replayed.Replay || replayed.JobID != created.JobID || len(harness.leaseSpy.requests) != 1 {
		t.Fatalf("replay=%+v err=%v acquire_calls=%d", replayed, err, len(harness.leaseSpy.requests))
	}
	request.ArchiveFormat = ArchiveTAR
	request.ArchiveProfile = "tar_none_v1"
	if _, err := harness.service.CommitCreate(ctx, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("same key different intent error=%v", err)
	}
}

func TestExportCommitCreateDiscardsExpiredReceiptBeforeComparingIntent(t *testing.T) {
	harness := newServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	request := CommitCreateRequest{
		Actor:          SelectionActor{UserID: 41, Role: "admin"},
		Selection:      selection,
		IdempotencyKey: "export-expired-receipt-reuse-key",
		ArchiveFormat:  ArchiveZIP,
		ArchiveProfile: "zip_deflate_v1",
	}
	keyDigest, err := IdempotencyKeyDigest(IdempotencyDomainExportCreate, request.Actor.UserID, request.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	now := harness.service.now().UTC()
	oldJobID := strings.Repeat("e", 32)
	oldJob := model.BackupAssetExportJob{
		ID: oldJobID, OwnerUserID: request.Actor.UserID, SelectionDigest: selection.Digest,
		CreatedAt: now.Add(-2 * time.Hour), AbsoluteDeadline: now.Add(time.Hour),
	}
	createExportTestJobWithLifecycleSequence(t, harness.db, &oldJob)
	oldReceiptID := strings.Repeat("f", 32)
	if err := harness.db.Create(&model.BackupAssetExportIdempotency{
		ID: oldReceiptID, OwnerUserID: request.Actor.UserID, Endpoint: exportCreateEndpoint,
		KeyDigest: keyDigest, RequestIntentDigest: strings.Repeat("d", 64), State: "committed", ResultJobID: &oldJobID,
		ExpiresAt: now, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}

	created, err := harness.service.CommitCreate(context.Background(), request)
	if err != nil {
		t.Fatalf("expired receipt create: %v", err)
	}
	if created.Replay || created.JobID == oldJobID {
		t.Fatalf("expired receipt replayed old job: %+v", created)
	}
	var receipt model.BackupAssetExportIdempotency
	if err := harness.db.Where("owner_user_id = ? AND endpoint = ? AND key_digest = ?", request.Actor.UserID, exportCreateEndpoint, keyDigest).
		Take(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.ID == oldReceiptID || receipt.ResultJobID == nil || *receipt.ResultJobID != created.JobID {
		t.Fatalf("expired receipt was not safely replaced: %+v created=%+v", receipt, created)
	}
	var receiptCount int64
	if err := harness.db.Model(&model.BackupAssetExportIdempotency{}).Count(&receiptCount).Error; err != nil || receiptCount != 1 {
		t.Fatalf("receipt count=%d err=%v, want one fresh slot", receiptCount, err)
	}
}

func TestExportCommitCreateUsesConfiguredIdempotencyReceiptTTL(t *testing.T) {
	harness := newServiceHarness(t)
	harness.service.config.IdempotencyTTL = 37 * time.Minute
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	request := CommitCreateRequest{
		Actor:          SelectionActor{UserID: 41, Role: "admin"},
		Selection:      selection,
		IdempotencyKey: "export-configured-idempotency-ttl",
		ArchiveFormat:  ArchiveZIP,
		ArchiveProfile: "zip_deflate_v1",
	}
	created, err := harness.service.CommitCreate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var receipt model.BackupAssetExportIdempotency
	if err := harness.db.Where("result_job_id = ?", created.JobID).Take(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	want := harness.service.now().UTC().Add(37 * time.Minute)
	if !receipt.ExpiresAt.Equal(want) {
		t.Fatalf("receipt expiry=%s, want configured idempotency TTL expiry %s", receipt.ExpiresAt, want)
	}
}

func TestExportCreateRejectsConfiguredIdempotencyKeyCeilingBeforeResolvingSelection(t *testing.T) {
	harness := newServiceHarness(t)
	harness.service.config.IdempotencyKeyMaxBytes = 32
	item := frozenItemFixture()
	_, err := harness.service.Create(context.Background(), CreateRequest{
		Actor: SelectionActor{UserID: 41, Role: "admin"},
		Selection: CreateSelectionV1{
			SchemaVersion: 1,
			Kind:          SelectionExplicit,
			Refs:          []backupasset.AssetRef{item.Ref},
		},
		IdempotencyKey: strings.Repeat("k", 33),
		ArchiveFormat:  ArchiveZIP,
		ArchiveProfile: "zip_deflate_v1",
	})
	if !errors.Is(err, ErrInvalidIdempotency) {
		t.Fatalf("configured idempotency ceiling error=%v, want ErrInvalidIdempotency", err)
	}
	if harness.resolver.explicitCalls != 0 {
		t.Fatalf("configured idempotency ceiling resolved selection %d times", harness.resolver.explicitCalls)
	}
}

func TestExportServiceClearsCallerOwnedKeyMaterial(t *testing.T) {
	t.Run("create success", func(t *testing.T) {
		harness := newServiceHarness(t)
		keys := &zeroTrackingExportKeySource{inner: harness.service.keys}
		harness.service.keys = keys
		selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
			Actor: SelectionActor{UserID: 41, Role: "admin"}, Selection: selection,
			IdempotencyKey: "export-zeroize-create-success", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
		}); err != nil {
			t.Fatal(err)
		}
		assertZeroedExportKeyMaterial(t, keys.returned, 2)
	})

	t.Run("create transaction failure", func(t *testing.T) {
		harness := newServiceHarness(t)
		keys := &zeroTrackingExportKeySource{inner: harness.service.keys}
		harness.service.keys = keys
		harness.resolver.err = errors.New("injected frozen selection revalidation failure")
		selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
			Actor: SelectionActor{UserID: 41, Role: "admin"}, Selection: selection,
			IdempotencyKey: "export-zeroize-create-failure", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
		}); !errors.Is(err, harness.resolver.err) {
			t.Fatalf("CommitCreate error=%v, want injected revalidation error", err)
		}
		assertZeroedExportKeyMaterial(t, keys.returned, 2)
	})

	t.Run("item cursor encode and decode", func(t *testing.T) {
		harness := newServiceHarness(t)
		keys := &zeroTrackingExportKeySource{inner: harness.service.keys}
		harness.service.keys = keys
		want := itemCursorV1{
			SchemaVersion:   itemCursorSchemaV1,
			JobID:           strings.Repeat("a", 32),
			SelectionDigest: strings.Repeat("b", 64),
			NextOrdinal:     1,
		}
		token, err := harness.service.encodeItemCursor(context.Background(), want)
		if err != nil {
			t.Fatal(err)
		}
		assertZeroedExportKeyMaterial(t, keys.returned, 1)
		keys.returned = nil
		got, err := harness.service.decodeItemCursor(context.Background(), token)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("decoded cursor=%+v, want %+v", got, want)
		}
		assertZeroedExportKeyMaterial(t, keys.returned, 1)
	})
}

func TestExportCommitCreateReservesExactFinalAndRegularItemCiphertext(t *testing.T) {
	harness := newServiceHarness(t)
	items := []FrozenItem{frozenItemFixture()}
	addItem := func(entryID, name string, entryType backupasset.CatalogEntryType, logicalSize int64) {
		item := frozenItemFixture()
		item.Ref.EntryID = strings.Repeat(entryID, 64)
		item.EntryType = entryType
		item.LogicalSize = logicalSize
		item.ArchiveComponents = []string{"root", name}
		items = append(items, item)
	}
	addItem("b", "second.bin", backupasset.CatalogEntryFile, harness.config.ChunkBytes+1)
	addItem("c", "empty.bin", backupasset.CatalogEntryFile, 0)
	addItem("d", "empty-dir", backupasset.CatalogEntryDirectory, 0)
	addItem("e", "link", backupasset.CatalogEntrySymlink, 0)
	addItem("f", "special", backupasset.CatalogEntrySpecial, 0)

	selection, err := FreezeSelection(items, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	expectedStoreBytes := harness.config.MaxCiphertextBytes
	for _, item := range selection.Items {
		if item.EntryType != backupasset.CatalogEntryFile {
			continue
		}
		spoolBytes, err := ciphertextSizeV1(item.LogicalSize, harness.config.ChunkBytes)
		if err != nil {
			t.Fatal(err)
		}
		expectedStoreBytes += spoolBytes
	}
	request := CommitCreateRequest{
		Actor: SelectionActor{UserID: 41, Role: "admin"}, Selection: selection,
		IdempotencyKey: "export-create-exact-store-reservation", ArchiveFormat: ArchiveZIP,
		ArchiveProfile: "zip_deflate_v1",
	}
	created, err := harness.service.CommitCreate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	assertExactStoreReservation := func() {
		t.Helper()
		var reservations []model.BackupAssetExportReservation
		if err := harness.db.Where("job_id = ? AND kind = ?", created.JobID, "store").
			Order("bucket_id ASC").Find(&reservations).Error; err != nil {
			t.Fatal(err)
		}
		if len(reservations) != 2 {
			t.Fatalf("store reservations=%d, want global and user", len(reservations))
		}
		for _, reservation := range reservations {
			if reservation.ReservedStoreBytes != expectedStoreBytes {
				t.Fatalf("store reservation=%d, want exact final+spools %d", reservation.ReservedStoreBytes, expectedStoreBytes)
			}
		}
		var buckets []model.BackupAssetExportQuotaBucket
		if err := harness.db.Where("scope IN ?", []string{"global", "user"}).Order("scope ASC").Find(&buckets).Error; err != nil {
			t.Fatal(err)
		}
		if len(buckets) != 2 {
			t.Fatalf("quota buckets=%d, want global and user", len(buckets))
		}
		for _, bucket := range buckets {
			if bucket.ActiveJobs != 1 || bucket.ReservedStoreBytes != expectedStoreBytes {
				t.Fatalf("quota bucket=%+v, want one job and exact store=%d", bucket, expectedStoreBytes)
			}
		}
	}
	assertExactStoreReservation()

	replayed, err := harness.service.CommitCreate(context.Background(), request)
	if err != nil || !replayed.Replay || replayed.JobID != created.JobID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	assertExactStoreReservation()
}

func TestExportCommitCreateRejectsRegularItemOverMaxBeforeDurableWork(t *testing.T) {
	harness := newServiceHarness(t)
	harness.service.config.MaxItemBytes = 64
	item := frozenItemFixture()
	item.LogicalSize = 65
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}

	_, err = harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 41, Role: "admin"}, Selection: selection,
		IdempotencyKey: "export-create-over-max-item", ArchiveFormat: ArchiveZIP,
		ArchiveProfile: "zip_deflate_v1",
	})
	if !errors.Is(err, ErrSelectionLimit) {
		t.Fatalf("CommitCreate error=%v, want ErrSelectionLimit", err)
	}
	if len(harness.leaseSpy.requests) != 0 {
		t.Fatalf("oversized item acquired %d source leases", len(harness.leaseSpy.requests))
	}
	for _, target := range []any{
		&model.BackupAssetExportJob{}, &model.BackupAssetExportKey{}, &model.BackupAssetExportItem{},
		&model.BackupAssetExportSourceLease{}, &model.BackupAssetExportIdempotency{},
		&model.BackupAssetExportQuotaBucket{}, &model.BackupAssetExportReservation{},
	} {
		var count int64
		if err := harness.db.Model(target).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%T count=%d err=%v, want zero durable work", target, count, err)
		}
	}
}

func TestValidServiceConfigRequiresArchiveAndMaximumRegularSpoolCiphertext(t *testing.T) {
	harness := newServiceHarness(t)
	config := harness.config
	config.MaxCiphertextBytes = expectedArchiveCiphertextCapacityV1(
		t, config.Selection.MaxLogicalBytes, config.Selection.MaxItems, config.ChunkBytes,
	)
	exactBoundary, err := maximumRegularSpoolPeakStoreBytesV1(config)
	if err != nil {
		t.Fatal(err)
	}
	config.Quota.GlobalStoreBytes = exactBoundary
	config.Quota.UserStoreBytes = exactBoundary
	if !validServiceConfig(config) {
		t.Fatalf("exact final+regular-spool peak boundary=%d rejected", exactBoundary)
	}

	config.Quota.UserStoreBytes = exactBoundary - 1
	if validServiceConfig(config) {
		t.Fatalf("below exact regular-spool peak boundary=%d accepted below required boundary=%d",
			config.Quota.UserStoreBytes, exactBoundary)
	}
}

func TestNewServiceRejectsUserStoreBelowTwoRegularSpoolPeakBeforeWork(t *testing.T) {
	harness := newServiceHarness(t)
	config := harness.config
	config.Selection.MaxItems = 2
	config.Selection.MaxSourcePoints = 1
	config.Selection.MaxLogicalBytes = 2 * config.MaxItemBytes
	config.MaxProviderBytes = config.Selection.MaxLogicalBytes
	config.MaxCiphertextBytes = expectedArchiveCiphertextCapacityV1(
		t, config.Selection.MaxLogicalBytes, config.Selection.MaxItems, config.ChunkBytes,
	)
	regularSpoolBytes, err := ciphertextSizeV1(config.MaxItemBytes, config.ChunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	config.Quota.UserStoreBytes = config.MaxCiphertextBytes + regularSpoolBytes
	config.Quota.GlobalStoreBytes = config.Quota.UserStoreBytes

	service, err := NewService(ServiceDependencies{
		DB: harness.db, Leases: harness.leaseSpy, Keys: harness.service.keys,
		Resolver: harness.resolver, Config: config,
	})
	if service != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("NewService error=%v service=%v, want ErrUnavailable before create work", err, service)
	}
	if len(harness.leaseSpy.requests) != 0 {
		t.Fatalf("NewService acquired %d source leases", len(harness.leaseSpy.requests))
	}
	assertNoExportCreateWrites(t, harness.db)
}

func TestNewServiceUsesLogicalByteLimitedExactRegularSpoolPeak(t *testing.T) {
	harness := newServiceHarness(t)
	config := harness.config
	config.Selection.MaxItems = 3
	config.Selection.MaxSourcePoints = 1
	config.Selection.MaxLogicalBytes = 15
	config.MaxItemBytes = 10
	config.MaxProviderBytes = config.Selection.MaxLogicalBytes
	config.ChunkBytes = 8
	config.MaxCiphertextBytes = expectedArchiveCiphertextCapacityV1(
		t, config.Selection.MaxLogicalBytes, config.Selection.MaxItems, config.ChunkBytes,
	)
	peakStoreBytes := config.MaxCiphertextBytes
	for _, logicalBytes := range []int64{10, 4, 1} {
		spoolBytes, err := ciphertextSizeV1(logicalBytes, config.ChunkBytes)
		if err != nil {
			t.Fatal(err)
		}
		peakStoreBytes += spoolBytes
	}
	config.Quota.UserStoreBytes = peakStoreBytes
	config.Quota.GlobalStoreBytes = peakStoreBytes

	if _, err := NewService(ServiceDependencies{
		DB: harness.db, Leases: harness.leaseSpy, Keys: harness.service.keys,
		Resolver: harness.resolver, Config: config,
	}); err != nil {
		t.Fatalf("NewService exact logical-byte-limited peak error=%v", err)
	}

	config.Quota.UserStoreBytes--
	config.Quota.GlobalStoreBytes--
	if _, err := NewService(ServiceDependencies{
		DB: harness.db, Leases: harness.leaseSpy, Keys: harness.service.keys,
		Resolver: harness.resolver, Config: config,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("NewService below logical-byte-limited peak error=%v, want ErrUnavailable", err)
	}
}

func TestMaximumRegularSpoolPeakStoreBytesMatchesSmallLegalSelections(t *testing.T) {
	for _, maxItems := range []int{1, 2, 3, 4} {
		for _, maxItemBytes := range []int64{1, 2, 3, 5} {
			for _, maxLogicalBytes := range []int64{1, 2, 3, 5, 8} {
				for _, chunkBytes := range []int64{1, 2, 3, 5} {
					config := ServiceConfig{
						Selection:          SelectionLimits{MaxItems: maxItems, MaxLogicalBytes: maxLogicalBytes},
						MaxItemBytes:       maxItemBytes,
						MaxCiphertextBytes: 1,
						ChunkBytes:         chunkBytes,
					}
					got, err := maximumRegularSpoolPeakStoreBytesV1(config)
					if err != nil {
						t.Fatalf("maximum peak maxItems=%d maxItemBytes=%d maxLogicalBytes=%d chunkBytes=%d: %v",
							maxItems, maxItemBytes, maxLogicalBytes, chunkBytes, err)
					}
					settingsPeak, ok := settings.BackupAssetExportMaximumStorePeakV1(
						config.MaxCiphertextBytes, int64(config.Selection.MaxItems), config.MaxItemBytes,
						config.Selection.MaxLogicalBytes, config.ChunkBytes,
					)
					if !ok {
						t.Fatalf("settings maximum peak rejected maxItems=%d maxItemBytes=%d maxLogicalBytes=%d chunkBytes=%d",
							maxItems, maxItemBytes, maxLogicalBytes, chunkBytes)
					}
					if got != settingsPeak {
						t.Fatalf("export maximum peak=%d differs from settings peak=%d maxItems=%d maxItemBytes=%d maxLogicalBytes=%d chunkBytes=%d",
							got, settingsPeak, maxItems, maxItemBytes, maxLogicalBytes, chunkBytes)
					}

					var wantRegularSpoolBytes int64
					for itemCount := 0; itemCount <= maxItems; itemCount++ {
						logicalBytes := make([]int64, itemCount)
						var enumerate func(int, int64)
						enumerate = func(index int, usedLogicalBytes int64) {
							if index == len(logicalBytes) {
								var totalSpoolBytes int64
								for _, itemLogicalBytes := range logicalBytes {
									spoolBytes, sizeErr := ciphertextSizeV1(itemLogicalBytes, chunkBytes)
									if sizeErr != nil {
										t.Fatal(sizeErr)
									}
									totalSpoolBytes += spoolBytes
								}
								if totalSpoolBytes > wantRegularSpoolBytes {
									wantRegularSpoolBytes = totalSpoolBytes
								}
								return
							}
							for itemLogicalBytes := int64(0); itemLogicalBytes <= maxItemBytes &&
								itemLogicalBytes <= maxLogicalBytes-usedLogicalBytes; itemLogicalBytes++ {
								logicalBytes[index] = itemLogicalBytes
								enumerate(index+1, usedLogicalBytes+itemLogicalBytes)
							}
						}
						enumerate(0, 0)
					}
					want := int64(1) + wantRegularSpoolBytes
					if got != want {
						t.Fatalf("maximum peak=%d want=%d maxItems=%d maxItemBytes=%d maxLogicalBytes=%d chunkBytes=%d",
							got, want, maxItems, maxItemBytes, maxLogicalBytes, chunkBytes)
					}
				}
			}
		}
	}
}

func TestNewServiceRejectsRegularSpoolPeakOverflow(t *testing.T) {
	harness := newServiceHarness(t)
	config := harness.config
	config.Selection.MaxItems = 2
	config.Selection.MaxSourcePoints = 1
	config.Selection.MaxLogicalBytes = 2
	config.MaxItemBytes = 1
	config.MaxProviderBytes = config.Selection.MaxLogicalBytes
	regularSpoolBytes, err := ciphertextSizeV1(config.MaxItemBytes, config.ChunkBytes)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxCiphertextBytes = math.MaxInt64 - regularSpoolBytes
	config.Quota.UserStoreBytes = math.MaxInt64
	config.Quota.GlobalStoreBytes = math.MaxInt64

	if _, err := NewService(ServiceDependencies{
		DB: harness.db, Leases: harness.leaseSpy, Keys: harness.service.keys,
		Resolver: harness.resolver, Config: config,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("NewService peak overflow error=%v, want ErrUnavailable", err)
	}
}

func TestNewServiceRejectsArchiveCiphertextCapacityOverflow(t *testing.T) {
	harness := newServiceHarness(t)
	config := harness.config
	config.Selection.MaxLogicalBytes = math.MaxInt64
	config.MaxItemBytes = 1
	config.MaxProviderBytes = 1
	config.MaxCiphertextBytes = math.MaxInt64 - 1000
	config.Quota.GlobalStoreBytes = math.MaxInt64
	config.Quota.UserStoreBytes = math.MaxInt64

	if validServiceConfig(config) {
		t.Fatal("archive ciphertext capacity overflow accepted")
	}
	if _, err := NewService(ServiceDependencies{
		DB: harness.db, Leases: harness.leaseSpy, Keys: harness.service.keys,
		Resolver: harness.resolver, Config: config,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("NewService overflow error=%v, want ErrUnavailable", err)
	}
}

func TestExportCommitRejectsArchiveCapacityBelowV1MinimumBeforeDurableWrites(t *testing.T) {
	harness := newServiceHarness(t)
	required := expectedArchiveCiphertextCapacityV1(
		t, harness.config.Selection.MaxLogicalBytes, harness.config.Selection.MaxItems, harness.config.ChunkBytes,
	)
	if required <= 1 {
		t.Fatalf("invalid archive ciphertext requirement=%d", required)
	}
	harness.service.config.MaxCiphertextBytes = required - 1
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 41, Role: "admin"}, Selection: selection,
		IdempotencyKey: "export-create-underbudget-archive-capacity", ArchiveFormat: ArchiveZIP,
		ArchiveProfile: ArchiveProfileZIPDeflateV1,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("underbudget create error=%v, want ErrUnavailable", err)
	}
	if len(harness.leaseSpy.requests) != 0 {
		t.Fatalf("underbudget create acquired %d source leases", len(harness.leaseSpy.requests))
	}
	for _, target := range []any{
		&model.BackupAssetExportJob{}, &model.BackupAssetExportKey{}, &model.BackupAssetExportItem{},
		&model.BackupAssetExportSourceLease{}, &model.BackupAssetExportIdempotency{},
		&model.BackupAssetExportQuotaBucket{}, &model.BackupAssetExportReservation{},
	} {
		var count int64
		if err := harness.db.Model(target).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%T count=%d err=%v, want zero durable work", target, count, err)
		}
	}
}

func expectedArchiveCiphertextCapacityV1(t *testing.T, logicalBytes int64, maxItems int, chunkBytes int64) int64 {
	t.Helper()
	compressionSlack := logicalBytes / 8
	if logicalBytes%8 != 0 {
		compressionSlack++
	}
	archivePlaintextBytes := logicalBytes + compressionSlack + int64(maxItems)*(16*4096) + 64*1024*1024
	required, err := ciphertextSizeV1(archivePlaintextBytes, chunkBytes)
	if err != nil {
		t.Fatalf("size expected archive ciphertext capacity: %v", err)
	}
	return required
}

func TestExportServiceCreateRejectsInvalidArchivePairBeforeSelectionResolution(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		format  ArchiveFormat
		profile string
	}{
		{name: "missing format", profile: "zip_deflate_v1"},
		{name: "missing profile", format: ArchiveZIP},
		{name: "unknown format", format: ArchiveFormat("rar"), profile: "zip_deflate_v1"},
		{name: "unknown profile", format: ArchiveZIP, profile: "future_v2"},
		{name: "zip crossed with tar none", format: ArchiveZIP, profile: "tar_none_v1"},
		{name: "zip crossed with tar gzip", format: ArchiveZIP, profile: "tar_gzip_v1"},
		{name: "tar crossed with zip", format: ArchiveTAR, profile: "zip_deflate_v1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			item := frozenItemFixture()
			_, err := harness.service.Create(context.Background(), CreateRequest{
				Actor: SelectionActor{UserID: 41, Role: "admin"},
				Selection: CreateSelectionV1{
					SchemaVersion: 1, Kind: SelectionExplicit, Refs: []backupasset.AssetRef{item.Ref},
				},
				IdempotencyKey: "export-invalid-pair-before-resolver", ArchiveFormat: testCase.format,
				ArchiveProfile: testCase.profile,
			})
			if !errors.Is(err, ErrInvalidSelection) {
				t.Fatalf("Create(%q, %q) error=%v, want ErrInvalidSelection", testCase.format, testCase.profile, err)
			}
			if harness.resolver.explicitCalls != 0 || harness.resolver.savedCalls != 0 {
				t.Fatalf("invalid pair reached selection resolver: explicit=%d saved=%d", harness.resolver.explicitCalls, harness.resolver.savedCalls)
			}
		})
	}
}

func TestExportCommitCreateRejectsInvalidArchivePairBeforeDurableWork(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		format  ArchiveFormat
		profile string
	}{
		{name: "missing format", profile: "zip_deflate_v1"},
		{name: "missing profile", format: ArchiveZIP},
		{name: "unknown format", format: ArchiveFormat("rar"), profile: "zip_deflate_v1"},
		{name: "unknown profile", format: ArchiveZIP, profile: "future_v2"},
		{name: "zip crossed with tar none", format: ArchiveZIP, profile: "tar_none_v1"},
		{name: "zip crossed with tar gzip", format: ArchiveZIP, profile: "tar_gzip_v1"},
		{name: "tar crossed with zip", format: ArchiveTAR, profile: "zip_deflate_v1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
			if err != nil {
				t.Fatal(err)
			}
			_, err = harness.service.CommitCreate(context.Background(), CommitCreateRequest{
				Actor: SelectionActor{UserID: 41, Role: "admin"}, Selection: selection,
				IdempotencyKey: "export-invalid-pair-before-commit", ArchiveFormat: testCase.format,
				ArchiveProfile: testCase.profile,
			})
			if !errors.Is(err, ErrInvalidSelection) {
				t.Fatalf("CommitCreate(%q, %q) error=%v, want ErrInvalidSelection", testCase.format, testCase.profile, err)
			}
			assertNoExportCreateWrites(t, harness.db)
			if len(harness.leaseSpy.requests) != 0 {
				t.Fatalf("invalid pair acquired %d source leases", len(harness.leaseSpy.requests))
			}
		})
	}
}

func TestExportCommitRevalidationFailureLeavesNo068RowsOrLease(t *testing.T) {
	harness := newServiceHarness(t)
	harness.resolver.err = errors.New("saved search changed")
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	_, err = harness.service.CommitCreate(context.Background(), CommitCreateRequest{
		Actor: SelectionActor{UserID: 42, Role: "admin"}, Selection: selection,
		IdempotencyKey: "export-create-key-0002", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err == nil {
		t.Fatal("revalidation failure unexpectedly committed")
	}
	for _, target := range []any{
		&model.BackupAssetExportJob{}, &model.BackupAssetExportKey{}, &model.BackupAssetExportQuotaBucket{},
		&model.BackupAssetExportIdempotency{}, &model.RecoveryPointLease{},
	} {
		var count int64
		query := harness.db.Model(target)
		if _, ok := target.(*model.RecoveryPointLease); ok {
			query = query.Where("holder_type = ?", backupasset.LeaseHolderExportJob)
		}
		if err := query.Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%T count=%d err=%v", target, count, err)
		}
	}
}

func TestExportCommitRejectsKeyDemotedAfterInitialActiveReadAndRetryUsesCurrentVersion(t *testing.T) {
	harness := newServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	ring := backupasset.NewKeyring(harness.db, harness.service.now)
	activeRead := make(chan backupasset.DomainKeyMaterial, 1)
	releaseActive := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseActive) }) }
	t.Cleanup(release)
	keys := &barrierExportKeySource{
		Keyring: ring, activeRead: activeRead, releaseActive: releaseActive,
	}
	harness.service.keys = keys
	request := CommitCreateRequest{
		Actor: SelectionActor{UserID: 42, Role: "admin"}, Selection: selection,
		IdempotencyKey: "export-create-key-rotation-race", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type createResult struct {
		result CommitCreateResult
		err    error
	}
	results := make(chan createResult, 1)
	go func() {
		result, err := harness.service.CommitCreate(ctx, request)
		results <- createResult{result: result, err: err}
	}()

	var initiallyActive backupasset.DomainKeyMaterial
	select {
	case initiallyActive = <-activeRead:
	case <-ctx.Done():
		t.Fatalf("wait for initial active Export key: %v", ctx.Err())
	}
	rotated, err := ring.Rotate(ctx, backupasset.KeyDomainExportStore, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Version != initiallyActive.Version+1 {
		t.Fatalf("rotated version=%d initial=%d", rotated.Version, initiallyActive.Version)
	}
	release()
	first := <-results
	if !errors.Is(first.err, ErrUnavailable) {
		t.Fatalf("create with demoted initial key result=%+v err=%v, want ErrUnavailable", first.result, first.err)
	}
	assertNoExportCreateWrites(t, harness.db)

	retried, err := harness.service.CommitCreate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var persistedKey model.BackupAssetExportKey
	if err := harness.db.Where("job_id = ?", retried.JobID).Take(&persistedKey).Error; err != nil {
		t.Fatal(err)
	}
	if persistedKey.KEKVersion != rotated.Version {
		t.Fatalf("retry key version=%d current=%d", persistedKey.KEKVersion, rotated.Version)
	}
}

func TestExportCommitConcurrentDifferentIntentUniqueCollisionReturnsConflict(t *testing.T) {
	harness := newServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	loserLookupStarted := make(chan struct{}, 1)
	winnerCommitted := make(chan struct{})
	var loserReceiptQueries atomic.Int32
	const beforeCallback = "test:export-different-intent-before-receipt-query"
	if err := harness.db.Callback().Query().Before("gorm:query").Register(beforeCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "backup_asset_export_idempotency" ||
			tx.Statement.Context.Value(exportCreateCollisionContextKey{}) != "loser" {
			return
		}
		if loserReceiptQueries.Add(1) != 1 {
			return
		}
		loserLookupStarted <- struct{}{}
		select {
		case <-winnerCommitted:
		case <-tx.Statement.Context.Done():
			_ = tx.AddError(tx.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = harness.db.Callback().Query().Remove(beforeCallback) })
	const afterCallback = "test:export-different-intent-hide-winner-receipt"
	if err := harness.db.Callback().Query().After("gorm:query").Register(afterCallback, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "backup_asset_export_idempotency" ||
			tx.Statement.Context.Value(exportCreateCollisionContextKey{}) != "loser" || loserReceiptQueries.Load() > 2 {
			return
		}
		row, ok := tx.Statement.Dest.(*model.BackupAssetExportIdempotency)
		if !ok {
			_ = tx.AddError(errors.New("unexpected export idempotency query destination"))
			return
		}
		*row = model.BackupAssetExportIdempotency{}
		tx.RowsAffected = 0
		_ = tx.AddError(gorm.ErrRecordNotFound)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = harness.db.Callback().Query().Remove(afterCallback) })

	type concurrentCreateResult struct {
		result CommitCreateResult
		err    error
	}
	loserResults := make(chan concurrentCreateResult, 1)
	winnerResults := make(chan concurrentCreateResult, 1)
	actor := SelectionActor{UserID: 42, Role: "admin"}
	loserCtx := context.WithValue(ctx, exportCreateCollisionContextKey{}, "loser")
	go func() {
		result, err := harness.service.CommitCreate(loserCtx, CommitCreateRequest{
			Actor: actor, Selection: selection, IdempotencyKey: "export-create-concurrent-different-intent",
			ArchiveFormat: ArchiveTAR, ArchiveProfile: "tar_none_v1",
		})
		loserResults <- concurrentCreateResult{result: result, err: err}
	}()
	select {
	case <-loserLookupStarted:
	case <-ctx.Done():
		t.Fatalf("wait for losing initial replay lookup: %v", ctx.Err())
	}
	go func() {
		result, err := harness.service.CommitCreate(ctx, CommitCreateRequest{
			Actor: actor, Selection: selection, IdempotencyKey: "export-create-concurrent-different-intent",
			ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
		})
		winnerResults <- concurrentCreateResult{result: result, err: err}
	}()
	winner := <-winnerResults
	close(winnerCommitted)
	loser := <-loserResults
	if winner.err != nil || winner.result.JobID == "" || winner.result.Replay {
		t.Fatalf("winner result=%+v err=%v", winner.result, winner.err)
	}
	if !errors.Is(loser.err, ErrConflict) || strings.Contains(strings.ToLower(loser.err.Error()), "unique") {
		t.Fatalf("loser result=%+v err=%v, want stable ErrConflict without DB details", loser.result, loser.err)
	}
	if loserReceiptQueries.Load() != 3 {
		t.Fatalf("loser receipt queries=%d, want preflight, transaction and collision replay", loserReceiptQueries.Load())
	}
	for _, target := range []any{
		&model.BackupAssetExportJob{}, &model.BackupAssetExportKey{}, &model.BackupAssetExportItem{},
		&model.BackupAssetExportSourceLease{}, &model.BackupAssetExportIdempotency{},
	} {
		var count int64
		if err := harness.db.Model(target).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("%T count=%d err=%v, want one durable winner", target, count, err)
		}
	}
	var reservationCount int64
	if err := harness.db.Model(&model.BackupAssetExportReservation{}).Count(&reservationCount).Error; err != nil || reservationCount != 4 {
		t.Fatalf("reservation count=%d err=%v, want winner only", reservationCount, err)
	}
}

func TestExportCommitCollisionFallbackDoesNotLeakOriginalUniqueErrorWhenReplayLookupFails(t *testing.T) {
	tests := []struct {
		name       string
		cancel     bool
		want       error
		lookupErr  error
		contextTag string
	}{
		{
			name: "context_canceled", cancel: true, want: context.Canceled,
			contextTag: "collision_fallback_context_canceled",
		},
		{
			name: "database_error", want: ErrUnavailable, lookupErr: errors.New("post-rollback lookup database detail"),
			contextTag: "collision_fallback_database_error",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
			if err != nil {
				t.Fatal(err)
			}
			key := "export-create-collision-fallback-" + test.name
			winnerRequest := CommitCreateRequest{
				Actor: SelectionActor{UserID: 42, Role: "admin"}, Selection: selection,
				IdempotencyKey: key, ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
			}
			winner, err := harness.service.CommitCreate(context.Background(), winnerRequest)
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.WithValue(context.Background(), exportCreateCollisionContextKey{}, test.contextTag)
			var cancel context.CancelFunc
			if test.cancel {
				ctx, cancel = context.WithCancel(ctx)
				t.Cleanup(cancel)
			}
			var receiptQueries atomic.Int32
			callbackName := "test:export-collision-fallback-" + test.name
			if err := harness.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Table != "backup_asset_export_idempotency" ||
					tx.Statement.Context.Value(exportCreateCollisionContextKey{}) != test.contextTag {
					return
				}
				query := receiptQueries.Add(1)
				row, ok := tx.Statement.Dest.(*model.BackupAssetExportIdempotency)
				if !ok {
					_ = tx.AddError(errors.New("unexpected export idempotency query destination"))
					return
				}
				*row = model.BackupAssetExportIdempotency{}
				tx.RowsAffected = 0
				switch query {
				case 1, 2:
					_ = tx.AddError(gorm.ErrRecordNotFound)
				case 3:
					if cancel != nil {
						cancel()
						_ = tx.AddError(context.Canceled)
					} else {
						_ = tx.AddError(test.lookupErr)
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = harness.db.Callback().Query().Remove(callbackName) })

			loserRequest := winnerRequest
			loserRequest.ArchiveFormat = ArchiveTAR
			loserRequest.ArchiveProfile = "tar_none_v1"
			loser, err := harness.service.CommitCreate(ctx, loserRequest)
			if !errors.Is(err, test.want) {
				t.Fatalf("collision fallback result=%+v error=%v, want %v", loser, err, test.want)
			}
			lowerError := strings.ToLower(err.Error())
			if strings.Contains(lowerError, "unique") || strings.Contains(lowerError, "constraint") ||
				strings.Contains(lowerError, "post-rollback lookup") {
				t.Fatalf("collision fallback exposed database detail: %v", err)
			}
			if receiptQueries.Load() != 3 {
				t.Fatalf("collision fallback receipt queries=%d want=3", receiptQueries.Load())
			}
			var jobs int64
			if err := harness.db.Model(&model.BackupAssetExportJob{}).Count(&jobs).Error; err != nil || jobs != 1 {
				t.Fatalf("collision fallback jobs=%d err=%v winner=%s", jobs, err, winner.JobID)
			}
		})
	}
}

func TestExportCommitRejectsCorruptedPersistedJobKeyTupleAndRollsBackEverything(t *testing.T) {
	tests := []struct {
		name    string
		table   string
		corrupt func(*gorm.DB) error
	}{
		{
			name: "wrapped_dek", table: "backup_asset_export_keys",
			corrupt: func(tx *gorm.DB) error {
				return tx.Exec("UPDATE backup_asset_export_keys SET wrapped_dek = ?", []byte{1}).Error
			},
		},
		{
			name: "envelope_nonce", table: "backup_asset_export_keys",
			corrupt: func(tx *gorm.DB) error {
				return tx.Exec("UPDATE backup_asset_export_keys SET envelope_nonce = ?", []byte{1}).Error
			},
		},
		{
			name: "wrap_algorithm", table: "backup_asset_export_keys",
			corrupt: func(tx *gorm.DB) error {
				return tx.Exec("UPDATE backup_asset_export_keys SET wrap_algorithm = ?", "aes-256-gcm-v2").Error
			},
		},
		{
			name: "kek_version", table: "backup_asset_export_keys",
			corrupt: func(tx *gorm.DB) error {
				return tx.Exec("UPDATE backup_asset_export_keys SET kek_version = kek_version + 1").Error
			},
		},
		{
			name: "key_revision", table: "backup_asset_export_keys",
			corrupt: func(tx *gorm.DB) error {
				return tx.Exec("UPDATE backup_asset_export_keys SET key_revision = key_revision + 1").Error
			},
		},
		{
			name: "created_at", table: "backup_asset_export_keys",
			corrupt: func(tx *gorm.DB) error {
				return tx.Exec("UPDATE backup_asset_export_keys SET created_at = ?", time.Now().UTC().Add(-time.Hour)).Error
			},
		},
		{
			name: "rewrapped_at", table: "backup_asset_export_keys",
			corrupt: func(tx *gorm.DB) error {
				return tx.Exec("UPDATE backup_asset_export_keys SET rewrapped_at = ?", time.Now().UTC()).Error
			},
		},
		{
			name: "destroyed_at", table: "backup_asset_export_keys",
			corrupt: func(tx *gorm.DB) error {
				return tx.Exec("UPDATE backup_asset_export_keys SET destroyed_at = ?", time.Now().UTC()).Error
			},
		},
		{
			name: "selection_digest", table: "backup_asset_export_jobs",
			corrupt: func(tx *gorm.DB) error {
				return tx.Exec("UPDATE backup_asset_export_jobs SET selection_digest = ?", strings.Repeat("f", 64)).Error
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			callbackName := "test:corrupt-export-create-" + test.name
			if err := harness.db.Callback().Create().After("gorm:create").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != test.table {
					return
				}
				_ = tx.AddError(test.corrupt(tx))
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = harness.db.Callback().Create().Remove(callbackName) })

			selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
			if err != nil {
				t.Fatal(err)
			}
			_, err = harness.service.CommitCreate(context.Background(), CommitCreateRequest{
				Actor: SelectionActor{UserID: 42, Role: "admin"}, Selection: selection,
				IdempotencyKey: "export-corrupt-create-" + test.name,
				ArchiveFormat:  ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
			})
			if !errors.Is(err, ErrCipherTampered) {
				t.Fatalf("CommitCreate error=%v, want ErrCipherTampered", err)
			}
			assertNoExportCreateWrites(t, harness.db)
		})
	}
}

func assertNoExportCreateWrites(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, target := range []any{
		&model.BackupAssetExportJob{}, &model.BackupAssetExportKey{}, &model.BackupAssetExportItem{},
		&model.BackupAssetExportSourceLease{}, &model.BackupAssetExportIdempotency{},
		&model.BackupAssetExportQuotaBucket{}, &model.BackupAssetExportReservation{},
	} {
		var count int64
		if err := db.Model(target).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%T count=%d err=%v", target, count, err)
		}
	}
	var leaseCount int64
	if err := db.Model(&model.RecoveryPointLease{}).
		Where("holder_type = ?", backupasset.LeaseHolderExportJob).Count(&leaseCount).Error; err != nil || leaseCount != 0 {
		t.Fatalf("export RecoveryPoint lease count=%d err=%v", leaseCount, err)
	}
}

func TestExportServiceCreateResolvesExplicitSelectionBeforeCommit(t *testing.T) {
	harness := newServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	harness.resolver.explicit = selection
	actor := SelectionActor{UserID: 41, Role: "admin"}
	created, err := harness.service.Create(context.Background(), CreateRequest{
		Actor: actor,
		Selection: CreateSelectionV1{SchemaVersion: 1, Kind: SelectionExplicit, Refs: []backupasset.AssetRef{
			frozenItemFixture().Ref,
		}},
		IdempotencyKey: "export-create-key-use-case-0001",
		ArchiveFormat:  ArchiveZIP,
		ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if harness.resolver.explicitCalls != 1 || created.Replay || created.Job.ID == "" ||
		created.Job.SelectionDigest != selection.Digest || created.Job.ItemCount != 1 || len(created.Job.Items) != 1 {
		t.Fatalf("created=%+v resolver_calls=%d", created, harness.resolver.explicitCalls)
	}
	payload, err := json.Marshal(created.Job)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"created_at":`) {
		t.Fatalf("created job omits created_at: %s", payload)
	}
}

func TestExportServiceCreateReplaysSavedSearchBeforeSelectionResolution(t *testing.T) {
	harness := newServiceHarness(t)
	binding := &SavedSearchCommitBindingV1{
		SavedSearchID:          strings.Repeat("a", 32),
		ExpectedVersion:        7,
		CanonicalQueryDigest:   strings.Repeat("b", 64),
		SearchGenerationDigest: strings.Repeat("c", 64),
	}
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, binding, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	harness.resolver.saved = selection
	request := CreateRequest{
		Actor: SelectionActor{UserID: 41, Role: "admin"},
		Selection: CreateSelectionV1{
			SchemaVersion: 1, Kind: SelectionSavedSearch,
			SavedSearchID: binding.SavedSearchID, SavedSearchVersion: binding.ExpectedVersion,
		},
		IdempotencyKey: "export-saved-search-replay-before-resolution",
		ArchiveFormat:  ArchiveZIP,
		ArchiveProfile: "zip_deflate_v1",
	}
	created, err := harness.service.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Replay || harness.resolver.savedCalls != 1 {
		t.Fatalf("created=%+v saved resolver calls=%d", created, harness.resolver.savedCalls)
	}

	// A replay must use only the receipt's raw request intent, not current Search or config state.
	harness.resolver.err = errors.New("saved search drifted after export creation")
	harness.service.config.ChunkBytes *= 2
	replayed, err := harness.service.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("saved-search replay after drift: %v", err)
	}
	if !replayed.Replay || replayed.Job.ID != created.Job.ID || harness.resolver.savedCalls != 1 {
		t.Fatalf("replayed=%+v created=%+v saved resolver calls=%d", replayed, created, harness.resolver.savedCalls)
	}
}

func TestExportServiceCreateRejectsDifferentRawIntentBeforeSelectionResolution(t *testing.T) {
	harness := newServiceHarness(t)
	binding := &SavedSearchCommitBindingV1{
		SavedSearchID:          strings.Repeat("a", 32),
		ExpectedVersion:        7,
		CanonicalQueryDigest:   strings.Repeat("b", 64),
		SearchGenerationDigest: strings.Repeat("c", 64),
	}
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, binding, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	harness.resolver.saved = selection
	request := CreateRequest{
		Actor: SelectionActor{UserID: 41, Role: "admin"},
		Selection: CreateSelectionV1{
			SchemaVersion: 1, Kind: SelectionSavedSearch,
			SavedSearchID: binding.SavedSearchID, SavedSearchVersion: binding.ExpectedVersion,
		},
		IdempotencyKey: "export-raw-intent-conflict-before-resolution",
		ArchiveFormat:  ArchiveZIP,
		ArchiveProfile: "zip_deflate_v1",
	}
	if _, err := harness.service.Create(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	harness.resolver.err = errors.New("selection resolution must not run for an idempotency conflict")

	differentVersion := request
	differentVersion.Selection.SavedSearchVersion++
	if _, err := harness.service.Create(context.Background(), differentVersion); !errors.Is(err, ErrConflict) {
		t.Fatalf("different saved-search version error=%v, want ErrConflict", err)
	}
	differentArm := request
	differentArm.Selection = CreateSelectionV1{
		SchemaVersion: 1, Kind: SelectionExplicit, Refs: []backupasset.AssetRef{frozenItemFixture().Ref},
	}
	if _, err := harness.service.Create(context.Background(), differentArm); !errors.Is(err, ErrConflict) {
		t.Fatalf("different selection arm error=%v, want ErrConflict", err)
	}
	if harness.resolver.savedCalls != 1 || harness.resolver.explicitCalls != 0 {
		t.Fatalf("saved calls=%d explicit calls=%d", harness.resolver.savedCalls, harness.resolver.explicitCalls)
	}
}

func TestExportServiceStatusUsesOpaqueJobBoundItemCursor(t *testing.T) {
	harness := newServiceHarness(t)
	first := frozenItemFixture()
	second := frozenItemFixture()
	second.Ref.EntryID = strings.Repeat("b", 64)
	second.EntryFingerprint = "entry-fingerprint-v2"
	second.ArchiveComponents = []string{"root", "second.txt"}
	selection, err := FreezeSelection([]FrozenItem{second, first}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	harness.resolver.explicit = selection
	actor := SelectionActor{UserID: 41, Role: "admin"}
	created, err := harness.service.Create(context.Background(), CreateRequest{
		Actor:          actor,
		Selection:      CreateSelectionV1{SchemaVersion: 1, Kind: SelectionExplicit, Refs: []backupasset.AssetRef{first.Ref, second.Ref}},
		IdempotencyKey: "export-create-key-status-0001", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := harness.service.Status(context.Background(), StatusRequest{Actor: actor, JobID: created.Job.ID, ItemsLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Ordinal != 0 || page.NextCursor == "" ||
		strings.Contains(page.NextCursor, created.Job.ID) || strings.Contains(page.NextCursor, first.Ref.EntryID) {
		t.Fatalf("first page=%+v", page)
	}
	next, err := harness.service.Status(context.Background(), StatusRequest{
		Actor: actor, JobID: created.Job.ID, ItemsLimit: 1, ItemsCursor: page.NextCursor,
	})
	if err != nil || len(next.Items) != 1 || next.Items[0].Ordinal != 1 || next.NextCursor != "" {
		t.Fatalf("next page=%+v err=%v", next, err)
	}

	foreignSelection := selection
	foreignSelection.Digest = strings.Repeat("f", 64)
	foreignJob := model.BackupAssetExportJob{
		ID: strings.Repeat("e", 32), OwnerUserID: actor.UserID, SelectionDigest: foreignSelection.Digest,
		SelectionSchemaVersion: 1, ArchiveFormat: string(ArchiveZIP), ArchiveProfile: "zip_deflate_v1",
		LimitsSchemaVersion: 1, ChunkBytes: 65536, MaxItems: 2, MaxSourcePoints: 1,
		MaxItemBytes: 1 << 20, MaxLogicalBytes: 1 << 20, MaxProviderBytes: 2 << 20, MaxCiphertextBytes: 3 << 20,
		MaxOpenReaders: 1, MaxDurationSeconds: 3600, MaxAttempts: 3, RetryBaseSeconds: 1,
		RetryMaxDelaySeconds: 60, LeaseTTLSeconds: 900, LeaseRenewMarginSeconds: 300, ReadyTTLSeconds: 86400,
		ExecutionState: string(ExecutionQueued), CleanupState: string(CleanupNone), AbsoluteDeadline: time.Now().UTC().Add(time.Hour),
		ItemCount: 1, TransitionRevision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	createExportTestJobWithLifecycleSequence(t, harness.db, &foreignJob)
	if _, err := harness.service.Status(context.Background(), StatusRequest{
		Actor: actor, JobID: foreignJob.ID, ItemsLimit: 1, ItemsCursor: page.NextCursor,
	}); !errors.Is(err, ErrInvalidSelection) {
		t.Fatalf("cross-job cursor error=%v", err)
	}
	if _, err := harness.service.Status(context.Background(), StatusRequest{
		Actor: SelectionActor{UserID: 99, Role: "admin"}, JobID: created.Job.ID,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign owner status error=%v", err)
	}
}

func TestExportServiceStatusProjectsPersistedClosedTerminalCategories(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		state    ExecutionState
		category string
	}{
		{name: "source expired", state: ExecutionSourceExpired, category: "source_expired"},
		{name: "worker unavailable", state: ExecutionFailed, category: "worker_unavailable"},
		{name: "provider unavailable", state: ExecutionFailed, category: "provider_unavailable"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			item := frozenItemFixture()
			selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
			if err != nil {
				t.Fatal(err)
			}
			harness.resolver.explicit = selection
			actor := SelectionActor{UserID: 41, Role: "admin"}
			created, err := harness.service.Create(context.Background(), CreateRequest{
				Actor: actor,
				Selection: CreateSelectionV1{
					SchemaVersion: 1, Kind: SelectionExplicit, Refs: []backupasset.AssetRef{item.Ref},
				},
				IdempotencyKey: "export-status-closed-category-" + strings.ReplaceAll(testCase.category, "_", "-"),
				ArchiveFormat:  ArchiveZIP,
				ArchiveProfile: "zip_deflate_v1",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.Job.ID).UpdateColumns(map[string]any{
					"execution_state": string(testCase.state),
					"error_category":  testCase.category,
					"failed_count":    1,
				}).Error; err != nil {
					return err
				}
				return tx.Model(&model.BackupAssetExportItem{}).Where("job_id = ?", created.Job.ID).UpdateColumns(map[string]any{
					"state":          string(ItemFailed),
					"error_category": testCase.category,
				}).Error
			}); err != nil {
				t.Fatal(err)
			}

			status, err := harness.service.Status(context.Background(), StatusRequest{Actor: actor, JobID: created.Job.ID})
			if err != nil {
				t.Fatalf("status error=%v", err)
			}
			if status.ExecutionState != testCase.state || status.ErrorCategory != testCase.category ||
				len(status.Items) != 1 || status.Items[0].ErrorCategory != testCase.category {
				t.Fatalf("status=%+v", status)
			}
		})
	}

	harness := newServiceHarness(t)
	item := frozenItemFixture()
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	harness.resolver.explicit = selection
	actor := SelectionActor{UserID: 42, Role: "admin"}
	created, err := harness.service.Create(context.Background(), CreateRequest{
		Actor: actor,
		Selection: CreateSelectionV1{
			SchemaVersion: 1, Kind: SelectionExplicit, Refs: []backupasset.AssetRef{item.Ref},
		},
		IdempotencyKey: "export-status-reject-unknown-category",
		ArchiveFormat:  ArchiveZIP,
		ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.Job.ID).
		UpdateColumn("error_category", "future_category").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.Status(context.Background(), StatusRequest{Actor: actor, JobID: created.Job.ID}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unknown category status error=%v, want ErrUnavailable", err)
	}
}

func TestExportServiceStatusNormalizesInternalPersistedCategories(t *testing.T) {
	for _, category := range []string{"archive_failed", "heartbeat_lost"} {
		t.Run(category, func(t *testing.T) {
			harness := newServiceHarness(t)
			item := frozenItemFixture()
			selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
			if err != nil {
				t.Fatal(err)
			}
			harness.resolver.explicit = selection
			actor := SelectionActor{UserID: 43, Role: "admin"}
			created, err := harness.service.Create(context.Background(), CreateRequest{
				Actor: actor,
				Selection: CreateSelectionV1{
					SchemaVersion: 1, Kind: SelectionExplicit, Refs: []backupasset.AssetRef{item.Ref},
				},
				IdempotencyKey: "export-status-normalize-" + strings.ReplaceAll(category, "_", "-"),
				ArchiveFormat:  ArchiveZIP,
				ArchiveProfile: "zip_deflate_v1",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.Job.ID).UpdateColumns(map[string]any{
					"execution_state": string(ExecutionFailed),
					"error_category":  category,
					"failed_count":    1,
				}).Error; err != nil {
					return err
				}
				return tx.Model(&model.BackupAssetExportItem{}).Where("job_id = ?", created.Job.ID).UpdateColumns(map[string]any{
					"state":          string(ItemFailed),
					"error_category": category,
				}).Error
			}); err != nil {
				t.Fatal(err)
			}

			status, err := harness.service.Status(context.Background(), StatusRequest{Actor: actor, JobID: created.Job.ID})
			if err != nil {
				t.Fatalf("status error=%v", err)
			}
			if status.ErrorCategory != "internal_failure" || len(status.Items) != 1 ||
				status.Items[0].ErrorCategory != "internal_failure" {
				t.Fatalf("status=%+v", status)
			}
			payload, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(payload), category) {
				t.Fatalf("public status leaked %q: %s", category, payload)
			}
		})
	}
}

func TestPersistedStatusCategoryAcceptsClosedExportCategories(t *testing.T) {
	for _, category := range []string{
		"", "source_changed", "source_expired", ItemErrorLinkMetadataUnavailable, ItemErrorSpecialFileSkipped,
		"artifact_missing", "artifact_tampered", "key_unavailable", "quota_exceeded", "deadline",
		"canceled", "internal_failure", "worker_unavailable", "provider_unavailable", "archive_failed", "heartbeat_lost",
	} {
		if !validPersistedStatusCategory(category) {
			t.Fatalf("validPersistedStatusCategory(%q) = false, want true", category)
		}
	}
	for _, category := range []string{"future_category", "provider_timeout"} {
		if validPersistedStatusCategory(category) {
			t.Fatalf("validPersistedStatusCategory(%q) = true, want false", category)
		}
	}
}

func TestExportServiceStatusRejectsJobCreatedAfterAbsoluteDeadline(t *testing.T) {
	harness := newServiceHarness(t)
	item := frozenItemFixture()
	selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	harness.resolver.explicit = selection
	actor := SelectionActor{UserID: 41, Role: "admin"}
	created, err := harness.service.Create(context.Background(), CreateRequest{
		Actor: actor, Selection: CreateSelectionV1{
			SchemaVersion: 1,
			Kind:          SelectionExplicit,
			Refs:          []backupasset.AssetRef{item.Ref},
		},
		IdempotencyKey: "export-create-key-created-deadline",
		ArchiveFormat:  ArchiveZIP,
		ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.Job.ID).
		UpdateColumn("created_at", created.Job.AbsoluteDeadline.Add(time.Second)).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := harness.service.Status(context.Background(), StatusRequest{
		Actor: actor, JobID: created.Job.ID,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("status error=%v, want unavailable", err)
	}
}

func TestExportServiceStatusAcceptsReadyExpiryAfterExecutionDeadline(t *testing.T) {
	fixture := createPersistentSealedFixture(t)
	published, err := fixture.worker.PublishReady(context.Background(), PersistentPublishRequest{
		JobID: fixture.jobID, AttemptID: fixture.attemptID,
		FenceToken: exportAttemptFenceToken(t, fixture.harness.db, fixture.attemptID), ArtifactID: fixture.artifactID,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := fixture.harness.service.Status(context.Background(), StatusRequest{
		Actor: SelectionActor{UserID: 100, Role: "admin"}, JobID: fixture.jobID,
	})
	if err != nil {
		t.Fatalf("ready status after execution deadline: %v", err)
	}
	if status.ExecutionState != ExecutionReady || status.ExpiresAt == nil ||
		!status.ExpiresAt.Equal(published.ExpiresAt) || !status.ExpiresAt.After(status.AbsoluteDeadline) ||
		!status.CanDownload {
		t.Fatalf("ready status=%+v published=%+v", status, published)
	}
}

func TestExportServiceStatusRejectsInvalidPersistedArchivePair(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		format  string
		profile string
	}{
		{name: "missing format", profile: "zip_deflate_v1"},
		{name: "missing profile", format: "zip"},
		{name: "unknown format", format: "rar", profile: "zip_deflate_v1"},
		{name: "unknown profile", format: "zip", profile: "future_v2"},
		{name: "zip crossed with tar none", format: "zip", profile: "tar_none_v1"},
		{name: "zip crossed with tar gzip", format: "zip", profile: "tar_gzip_v1"},
		{name: "tar crossed with zip", format: "tar", profile: "zip_deflate_v1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newServiceHarness(t)
			item := frozenItemFixture()
			selection, err := FreezeSelection([]FrozenItem{item}, nil, harness.config.Selection)
			if err != nil {
				t.Fatal(err)
			}
			harness.resolver.explicit = selection
			actor := SelectionActor{UserID: 41, Role: "admin"}
			created, err := harness.service.Create(context.Background(), CreateRequest{
				Actor: actor,
				Selection: CreateSelectionV1{
					SchemaVersion: 1, Kind: SelectionExplicit, Refs: []backupasset.AssetRef{item.Ref},
				},
				IdempotencyKey: "export-status-invalid-persisted-pair", ArchiveFormat: ArchiveZIP,
				ArchiveProfile: "zip_deflate_v1",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", created.Job.ID).
				UpdateColumns(map[string]any{"archive_format": testCase.format, "archive_profile": testCase.profile}).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := harness.service.Status(context.Background(), StatusRequest{
				Actor: actor, JobID: created.Job.ID,
			}); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Status(%q, %q) error=%v, want ErrUnavailable", testCase.format, testCase.profile, err)
			}
		})
	}
}

func TestExportServiceCancelIsOwnerBoundAndIdempotent(t *testing.T) {
	harness := newServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	harness.resolver.explicit = selection
	actor := SelectionActor{UserID: 41, Role: "admin"}
	created, err := harness.service.Create(context.Background(), CreateRequest{
		Actor: actor, Selection: CreateSelectionV1{SchemaVersion: 1, Kind: SelectionExplicit, Refs: []backupasset.AssetRef{frozenItemFixture().Ref}},
		IdempotencyKey: "export-create-key-cancel-0001", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.Cancel(context.Background(), SelectionActor{UserID: 99, Role: "admin"}, created.Job.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign cancel error=%v", err)
	}
	canceled, err := harness.service.Cancel(context.Background(), actor, created.Job.ID)
	if err != nil || canceled.ExecutionState != ExecutionCancelRequested || !canceled.CanCancel {
		t.Fatalf("cancel status=%+v err=%v", canceled, err)
	}
	replayed, err := harness.service.Cancel(context.Background(), actor, created.Job.ID)
	if err != nil || replayed.ExecutionState != ExecutionCancelRequested || replayed.TransitionRevision != canceled.TransitionRevision {
		t.Fatalf("cancel replay=%+v err=%v", replayed, err)
	}
}

func TestExportServiceCancelLocksJobRowBeforeTransitionValidation(t *testing.T) {
	harness := newServiceHarness(t)
	selection, err := FreezeSelection([]FrozenItem{frozenItemFixture()}, nil, harness.config.Selection)
	if err != nil {
		t.Fatal(err)
	}
	actor := SelectionActor{UserID: 197, Role: "admin"}
	harness.resolver.explicit = selection
	created, err := harness.service.Create(context.Background(), CreateRequest{
		Actor: actor, Selection: CreateSelectionV1{SchemaVersion: 1, Kind: SelectionExplicit, Refs: []backupasset.AssetRef{frozenItemFixture().Ref}},
		IdempotencyKey: "cancel-job-row-lock", ArchiveFormat: ArchiveZIP, ArchiveProfile: "zip_deflate_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	const callbackName = "test:cancel_locks_job_row"
	var locked bool
	if err := harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "backup_asset_export_jobs" {
			return
		}
		if _, ok := tx.Statement.Clauses["FOR"]; ok {
			locked = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = harness.db.Callback().Query().Remove(callbackName) })
	if _, err := harness.service.Cancel(context.Background(), actor, created.Job.ID); err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("Cancel loaded the job without a FOR UPDATE row lock")
	}
}

type serviceHarness struct {
	db       *gorm.DB
	lease    *backupasset.LeaseService
	leaseSpy *leaseAcquireSpy
	resolver *selectionResolverStub
	config   ServiceConfig
	service  *Service
}

type exportCreateCollisionContextKey struct{}

func newServiceHarness(t *testing.T) serviceHarness {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_EXPORT_SERVICE_KEK_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{NowFunc: func() time.Time { return time.Now().UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close Export service test database: %v", err)
		}
	})
	models := []any{
		&model.RecoveryPoint{}, &model.RecoveryPointLease{}, &model.WrappedDomainKey{},
		&model.BackupAssetExportJob{}, &model.BackupAssetExportKey{}, &model.BackupAssetExportItem{},
		&model.BackupAssetExportAttempt{}, &model.BackupAssetExportItemAttempt{}, &model.BackupAssetExportSourceLease{},
		&model.BackupAssetExportArtifact{}, &model.BackupAssetExportIdempotency{}, &model.BackupAssetExportQuotaBucket{},
		&model.BackupAssetExportReservation{}, &model.BackupAssetExportDeliveryGrant{},
		&model.BackupAssetExportDeliveryRequest{}, &model.BackupAssetArchiveMemberRequest{},
	}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	item := frozenItemFixture()
	if err := db.Create(&model.RecoveryPoint{
		ID: item.Ref.RecoveryPointID, RepositoryID: strings.Repeat("9", 32), State: string(backupasset.RecoveryPointCommitted),
		Semantics: string(backupasset.PointNativeSnapshot), SourceFingerprint: item.SourceFingerprint,
		CapabilityRevision: int(item.ProviderCapabilityRevision), PhysicalAvailability: string(backupasset.PhysicalOnline),
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), HoldState: string(backupasset.HoldNone),
		RetentionUntil: item.RetentionUntil, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 15 * time.Minute, Heartbeat: 5 * time.Minute, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ring := backupasset.NewKeyring(db, func() time.Time { return now })
	if _, err := ring.Ensure(context.Background(), backupasset.KeyDomainExportStore); err != nil {
		t.Fatal(err)
	}
	spy := &leaseAcquireSpy{LeaseService: lease}
	resolver := &selectionResolverStub{}
	selectionLimits := SelectionLimits{MaxItems: 100, MaxSourcePoints: 10, MaxLogicalBytes: 1 << 20}
	maxCiphertextBytes, err := minimumArchiveCiphertextBytesV1(
		selectionLimits.MaxLogicalBytes, selectionLimits.MaxItems, 65536,
	)
	if err != nil {
		t.Fatal(err)
	}
	maxItemCiphertextBytes, err := ciphertextSizeV1(1<<20, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if maxItemCiphertextBytes > math.MaxInt64-maxCiphertextBytes {
		t.Fatal("service harness peak store reservation overflows")
	}
	peakStoreBytes := maxCiphertextBytes + maxItemCiphertextBytes
	config := ServiceConfig{
		Selection:  selectionLimits,
		ChunkBytes: 65536, MaxItemBytes: 1 << 20, MaxProviderBytes: 2 << 20, MaxCiphertextBytes: maxCiphertextBytes,
		MaxOpenReaders: 2, MaxDuration: time.Hour, MaxAttempts: 3, RetryBase: time.Second,
		RetryMaxDelay: time.Minute, LeaseTTL: 15 * time.Minute, LeaseRenewMargin: 5 * time.Minute,
		ReadyTTL: 24 * time.Hour, IdempotencyTTL: 24 * time.Hour, IdempotencyKeyMaxBytes: 128,
		Quota: QuotaLimits{
			GlobalActiveJobs: 8, UserActiveJobs: 2, GlobalStoreBytes: peakStoreBytes * 8, UserStoreBytes: peakStoreBytes * 2,
		},
	}
	service, err := NewService(ServiceDependencies{
		DB: db, Now: func() time.Time { return now }, Leases: spy, Keys: ring, Resolver: resolver, Config: config,
	})
	if err != nil {
		t.Fatal(err)
	}
	return serviceHarness{db: db, lease: lease, leaseSpy: spy, resolver: resolver, config: config, service: service}
}

type leaseAcquireSpy struct {
	*backupasset.LeaseService
	requests []backupasset.AcquireLeaseRequest
	leases   []backupasset.Lease
}

func (spy *leaseAcquireSpy) AcquireTx(ctx context.Context, tx *gorm.DB, request backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
	spy.requests = append(spy.requests, request)
	lease, err := spy.LeaseService.AcquireTx(ctx, tx, request)
	if err == nil {
		spy.leases = append(spy.leases, lease)
	}
	return lease, err
}

type selectionResolverStub struct {
	err           error
	explicit      FrozenSelection
	saved         FrozenSelection
	explicitCalls int
	savedCalls    int
}

type barrierExportKeySource struct {
	*backupasset.Keyring
	activeRead    chan<- backupasset.DomainKeyMaterial
	releaseActive <-chan struct{}
	once          sync.Once
}

type zeroTrackingExportKeySource struct {
	inner    ExportKeySource
	returned [][]byte
}

func (source *zeroTrackingExportKeySource) Active(
	ctx context.Context, domain backupasset.KeyDomain,
) (backupasset.DomainKeyMaterial, error) {
	material, err := source.inner.Active(ctx, domain)
	if err != nil {
		return material, err
	}
	return source.track(material), nil
}

func (source *zeroTrackingExportKeySource) ByVersion(
	ctx context.Context, domain backupasset.KeyDomain, version int,
) (backupasset.DomainKeyMaterial, error) {
	material, err := source.inner.ByVersion(ctx, domain, version)
	if err != nil {
		return material, err
	}
	return source.track(material), nil
}

func (source *zeroTrackingExportKeySource) LockActiveTx(
	ctx context.Context, tx *gorm.DB, expected backupasset.DomainKeyMaterial,
) (backupasset.DomainKeyMaterial, error) {
	material, err := source.inner.LockActiveTx(ctx, tx, expected)
	if err != nil {
		return material, err
	}
	return source.track(material), nil
}

func (source *zeroTrackingExportKeySource) track(material backupasset.DomainKeyMaterial) backupasset.DomainKeyMaterial {
	original := material.Key
	material.Key = append([]byte(nil), original...)
	clear(original)
	source.returned = append(source.returned, material.Key)
	return material
}

func assertZeroedExportKeyMaterial(t *testing.T, materials [][]byte, wantCount int) {
	t.Helper()
	if len(materials) != wantCount {
		t.Fatalf("caller-owned key material count=%d, want %d", len(materials), wantCount)
	}
	for index, material := range materials {
		if len(material) != 32 || !allZeroExportKeyBytes(material) {
			t.Fatalf("caller-owned key material %d was retained: %x", index, material)
		}
	}
}

func allZeroExportKeyBytes(value []byte) bool {
	for _, current := range value {
		if current != 0 {
			return false
		}
	}
	return true
}

func (source *barrierExportKeySource) Active(
	ctx context.Context, domain backupasset.KeyDomain,
) (backupasset.DomainKeyMaterial, error) {
	material, err := source.Keyring.Active(ctx, domain)
	if err != nil {
		return backupasset.DomainKeyMaterial{}, err
	}
	source.once.Do(func() {
		select {
		case source.activeRead <- material:
		case <-ctx.Done():
			return
		}
		select {
		case <-source.releaseActive:
		case <-ctx.Done():
		}
	})
	return material, nil
}

func (stub *selectionResolverStub) ResolveExplicit(context.Context, SelectionActor, []backupasset.AssetRef, SelectionLimits) (FrozenSelection, error) {
	stub.explicitCalls++
	if stub.err != nil {
		return FrozenSelection{}, stub.err
	}
	return stub.explicit, nil
}
func (stub *selectionResolverStub) ResolveSavedSearch(context.Context, SelectionActor, string, int64, SelectionLimits) (FrozenSelection, error) {
	stub.savedCalls++
	if stub.err != nil {
		return FrozenSelection{}, stub.err
	}
	return stub.saved, nil
}
func (stub *selectionResolverStub) RevalidateFrozenTx(context.Context, *gorm.DB, SelectionActor, FrozenSelection) error {
	return stub.err
}
func (*selectionResolverStub) RevalidateMetadataTx(context.Context, *gorm.DB, FrozenItem) error {
	return nil
}
