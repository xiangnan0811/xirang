package search

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

func TestContentIndexIngestPublishesAtomicOpaqueProjection(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	entryID := strings.Repeat("6", 64)
	pointID, catalogID := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: entryID, NormalizedPath: "content.txt", Name: "content.txt", EntryType: "file", SecurityState: "unknown",
	}})
	searchGeneration, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
	if err != nil {
		t.Fatal(err)
	}
	ingest, lease := newContentIngestForHarness(t, harness)
	projection := ContentProjection{
		Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, Field: SearchFieldContent,
		Terms: []TermFrequency{{Term: "Needle", Frequency: 3}}, SourceFingerprint: "source-" + pointID,
		CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
		ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
		ExpectedClassificationRevision: 1, Classification: SensitivitySecret, ClassificationRevision: 2,
		CoverageRevision: 2, PipelineRevision: 2, IndexRevision: 2,
		ExcerptRef: stringPointer(strings.Repeat("a", 32)), Coverage: FieldCoverageComplete,
	}
	if err := ingest.PublishContentProjection(context.Background(), projection); err != nil {
		t.Fatalf("PublishContentProjection: %v", err)
	}
	var postings []model.BackupAssetSearchPosting
	if err := harness.db.Where("search_generation_id = ? AND document_id = ? AND field = ?", searchGeneration.ID, entryID, SearchFieldContent).
		Find(&postings).Error; err != nil {
		t.Fatal(err)
	}
	if len(postings) != 1 || postings[0].TermFrequency != 3 || len(postings[0].TokenHMAC) != 64 ||
		strings.Contains(strings.ToLower(postings[0].TokenHMAC), "needle") {
		t.Fatalf("content posting is not opaque: %+v", postings)
	}
	var field model.BackupAssetSearchDocumentField
	if err := harness.db.Where("search_generation_id = ? AND document_id = ? AND field = ?", searchGeneration.ID, entryID, SearchFieldContent).
		Take(&field).Error; err != nil {
		t.Fatal(err)
	}
	if field.State != string(FieldCoverageComplete) || field.ExcerptRef == nil || *field.ExcerptRef != strings.Repeat("a", 32) ||
		field.ClassificationRevision != 2 || field.PipelineRevision != 2 || field.IndexRevision != 2 {
		t.Fatalf("content field CAS mismatch: %+v", field)
	}
	var document model.BackupAssetSearchDocument
	if err := harness.db.Where("search_generation_id = ? AND document_id = ?", searchGeneration.ID, entryID).Take(&document).Error; err != nil {
		t.Fatal(err)
	}
	if document.Sensitivity != string(SensitivitySecret) || document.ClassificationRevision != 2 {
		t.Fatalf("classification CAS mismatch: %+v", document)
	}
}

func TestContentIndexIngestClassificationChangeInvalidatesSiblingField(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	entryID := strings.Repeat("9", 64)
	pointID, catalogID := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: entryID, NormalizedPath: "classified.txt", Name: "classified.txt", EntryType: "file", SecurityState: "non_secret",
	}})
	searchGeneration, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
	if err != nil {
		t.Fatal(err)
	}
	ingest, lease := newContentIngestForHarness(t, harness)
	base := ContentProjection{
		Ref:               backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID},
		SourceFingerprint: "source-" + pointID, CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
		ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
		ExpectedClassificationRevision: 1, Classification: SensitivityNonSecret, ClassificationRevision: 1,
		CoverageRevision: 2, PipelineRevision: 2, IndexRevision: 2, Coverage: FieldCoverageComplete,
	}
	ocr := base
	ocr.Field = SearchFieldOCR
	ocr.Terms = []TermFrequency{{Term: "stale-ocr", Frequency: 1}}
	ocr.ExcerptRef = stringPointer(strings.Repeat("c", 32))
	if err := ingest.PublishContentProjection(context.Background(), ocr); err != nil {
		t.Fatalf("publish initial OCR projection: %v", err)
	}

	content := base
	content.Field = SearchFieldContent
	content.Terms = []TermFrequency{{Term: "classified-content", Frequency: 1}}
	content.Classification = SensitivitySecret
	content.ClassificationRevision = 2
	content.ExcerptRef = stringPointer(strings.Repeat("d", 32))
	if err := ingest.PublishContentProjection(context.Background(), content); err != nil {
		t.Fatalf("publish classification change: %v", err)
	}

	var sibling model.BackupAssetSearchDocumentField
	if err := harness.db.Where("search_generation_id = ? AND document_id = ? AND field = ?",
		searchGeneration.ID, entryID, SearchFieldOCR).Take(&sibling).Error; err != nil {
		t.Fatal(err)
	}
	var siblingPostings int64
	if err := harness.db.Model(&model.BackupAssetSearchPosting{}).
		Where("search_generation_id = ? AND document_id = ? AND field = ?", searchGeneration.ID, entryID, SearchFieldOCR).
		Count(&siblingPostings).Error; err != nil {
		t.Fatal(err)
	}
	if sibling.State != string(FieldCoverageUnavailable) || sibling.ClassificationRevision != 2 ||
		sibling.ExcerptRef != nil || siblingPostings != 0 {
		t.Fatalf("classification change left stale sibling projection: field=%+v postings=%d", sibling, siblingPostings)
	}

	ocr.ExpectedClassificationRevision = 2
	ocr.Classification = SensitivitySecret
	ocr.ClassificationRevision = 2
	ocr.CoverageRevision = 3
	ocr.PipelineRevision = 3
	ocr.IndexRevision = 3
	ocr.ExcerptRef = stringPointer(strings.Repeat("e", 32))
	if err := ingest.PublishContentProjection(context.Background(), ocr); err != nil {
		t.Fatalf("republish sibling at new classification revision: %v", err)
	}
}

func TestContentIndexIngestRejectsMetadataStaleFenceAndRollsBack(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	entryID := strings.Repeat("7", 64)
	pointID, catalogID := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: entryID, NormalizedPath: "rollback.txt", Name: "rollback.txt", EntryType: "file", SecurityState: "non_secret",
	}})
	searchGeneration, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
	if err != nil {
		t.Fatal(err)
	}
	ingest, lease := newContentIngestForHarness(t, harness)
	valid := ContentProjection{
		Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, Field: SearchFieldOCR,
		Terms: []TermFrequency{{Term: "private-marker", Frequency: 1}}, SourceFingerprint: "source-" + pointID,
		CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
		ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
		ExpectedClassificationRevision: 1, Classification: SensitivityNonSecret, ClassificationRevision: 1,
		CoverageRevision: 2, PipelineRevision: 2, IndexRevision: 2, Coverage: FieldCoverageComplete,
	}
	testCases := []struct {
		name   string
		mutate func(*ContentProjection)
		want   error
	}{
		{name: "metadata field", mutate: func(value *ContentProjection) { value.Field = SearchFieldName }, want: ErrInvalidContentProjection},
		{name: "source", mutate: func(value *ContentProjection) { value.SourceFingerprint = "drift" }, want: ErrSearchSourceChanged},
		{name: "classification", mutate: func(value *ContentProjection) { value.ExpectedClassificationRevision = 2 }, want: ErrContentProjectionStale},
		{name: "fence", mutate: func(value *ContentProjection) { value.FenceToken = strings.Repeat("0", 64) }, want: backupasset.ErrLeaseFenceLost},
		{name: "excerpt ref", mutate: func(value *ContentProjection) { value.ExcerptRef = stringPointer("not-opaque") }, want: ErrInvalidContentProjection},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			projection := valid
			testCase.mutate(&projection)
			before := contentProjectionSnapshot(t, harness, searchGeneration.ID, entryID, SearchFieldOCR)
			err := ingest.PublishContentProjection(context.Background(), projection)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("got %v, want %v", err, testCase.want)
			}
			if strings.Contains(strings.ToLower(err.Error()), "private-marker") {
				t.Fatalf("error leaked content term: %v", err)
			}
			after := contentProjectionSnapshot(t, harness, searchGeneration.ID, entryID, SearchFieldOCR)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected ingest changed projection: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestProjectionRevokeRemovesPostingsExcerptAndCoverage(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	entryID := strings.Repeat("8", 64)
	pointID, catalogID := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: entryID, NormalizedPath: "revoke.txt", Name: "revoke.txt", EntryType: "file", SecurityState: "non_secret",
	}})
	searchGeneration, _ := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
	ingest, lease := newContentIngestForHarness(t, harness)
	publish := ContentProjection{
		Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, Field: SearchFieldContent,
		Terms: []TermFrequency{{Term: "revoked", Frequency: 1}}, SourceFingerprint: "source-" + pointID,
		CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
		ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
		ExpectedClassificationRevision: 1, Classification: SensitivityNonSecret, ClassificationRevision: 1,
		CoverageRevision: 2, PipelineRevision: 2, IndexRevision: 2,
		ExcerptRef: stringPointer(strings.Repeat("b", 32)), Coverage: FieldCoverageComplete,
	}
	if err := ingest.PublishContentProjection(context.Background(), publish); err != nil {
		t.Fatal(err)
	}
	if err := ingest.RevokeContentProjection(context.Background(), RevokeProjection{
		Ref: publish.Ref, Field: publish.Field, SourceFingerprint: publish.SourceFingerprint,
		CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
		ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
		ExpectedClassificationRevision: 1, CoverageRevision: 3, PipelineRevision: 3, IndexRevision: 3,
	}); err != nil {
		t.Fatalf("RevokeContentProjection: %v", err)
	}
	snapshot := contentProjectionSnapshot(t, harness, searchGeneration.ID, entryID, SearchFieldContent)
	if snapshot.Postings != 0 || snapshot.State != string(FieldCoverageUnavailable) || snapshot.ExcerptRef != "" || snapshot.IndexRevision != 3 {
		t.Fatalf("revoked projection remains visible: %+v", snapshot)
	}
}

type contentSnapshot struct {
	Postings      int64
	State         string
	ExcerptRef    string
	IndexRevision int
	ProjectionRev int64
}

func contentProjectionSnapshot(t *testing.T, harness *indexerTestHarness, searchID, entryID string, field SearchField) contentSnapshot {
	t.Helper()
	var snapshot contentSnapshot
	if err := harness.db.Model(&model.BackupAssetSearchPosting{}).
		Where("search_generation_id = ? AND document_id = ? AND field = ?", searchID, entryID, field).Count(&snapshot.Postings).Error; err != nil {
		t.Fatal(err)
	}
	var fieldRow model.BackupAssetSearchDocumentField
	if err := harness.db.Where("search_generation_id = ? AND document_id = ? AND field = ?", searchID, entryID, field).Take(&fieldRow).Error; err != nil {
		t.Fatal(err)
	}
	snapshot.State = fieldRow.State
	snapshot.IndexRevision = fieldRow.IndexRevision
	if fieldRow.ExcerptRef != nil {
		snapshot.ExcerptRef = *fieldRow.ExcerptRef
	}
	if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).Select("projection_revision").Where("id = ?", searchID).Scan(&snapshot.ProjectionRev).Error; err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func newContentIngestForHarness(t *testing.T, harness *indexerTestHarness) (*ContentIngestService, backupasset.Lease) {
	t.Helper()
	leaseService, err := backupasset.NewLeaseService(harness.db, func() time.Time { return harness.now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := harness.db.Order("created_at DESC").Take(&point).Error; err != nil {
		t.Fatal(err)
	}
	lease, err := leaseService.Acquire(context.Background(), backupasset.AcquireLeaseRequest{
		RecoveryPointID: point.ID, HolderType: backupasset.LeaseHolderProcessingJob, OwnerID: "content-worker",
	})
	if err != nil {
		t.Fatalf("acquire processing lease: %v", err)
	}
	ingest, err := NewContentIngestService(ContentIngestDependencies{
		DB: harness.db, Keys: harness.ring, Lease: leaseService, Now: func() time.Time { return harness.now }, Limits: DefaultContentIngestLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return ingest, lease
}

func stringPointer(value string) *string { return &value }
