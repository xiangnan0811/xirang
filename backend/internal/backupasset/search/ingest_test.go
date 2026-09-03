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

	"gorm.io/gorm"
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

func TestContentIndexIngestRejectsOlderNormalizerGeneration(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	entryID := strings.Repeat("8", 64)
	pointID, catalogID := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: entryID, NormalizedPath: "content.txt", Name: "content.txt", EntryType: "file", SecurityState: "unknown",
	}})
	searchGeneration, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).
		Where("id = ?", searchGeneration.ID).
		Update("normalizer_version", NormalizerVersion-1).Error; err != nil {
		t.Fatalf("age Search normalizer version: %v", err)
	}
	ingest, lease := newContentIngestForHarness(t, harness)
	err = ingest.PublishContentProjection(context.Background(), ContentProjection{
		Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, Field: SearchFieldContent,
		Terms: []TermFrequency{{Term: "needle", Frequency: 1}}, SourceFingerprint: "source-" + pointID,
		CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
		ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
		ExpectedClassificationRevision: 1, Classification: SensitivityNonSecret, ClassificationRevision: 1,
		CoverageRevision: 2, PipelineRevision: 2, IndexRevision: 2, Coverage: FieldCoverageComplete,
	})
	if !errors.Is(err, ErrContentProjectionStale) {
		t.Fatalf("old normalizer generation error=%v, want ErrContentProjectionStale", err)
	}
}

func TestLifecycleLateOutputRejectsSearchContentAndOCRIngest(t *testing.T) {
	for _, field := range []SearchField{SearchFieldContent, SearchFieldOCR} {
		t.Run(string(field), func(t *testing.T) {
			indexer, harness := newIndexerTestHarness(t)
			entryID := strings.Repeat("7", 64)
			pointID, catalogID := harness.seedCatalog(t, []model.CatalogEntry{{
				EntryID: entryID, NormalizedPath: "late.txt", Name: "late.txt", EntryType: "file", SecurityState: "non_secret",
			}})
			searchGeneration, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
			if err != nil {
				t.Fatal(err)
			}
			ingest, lease := newContentIngestForHarness(t, harness)
			projection := ContentProjection{
				Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, Field: field,
				Terms: []TermFrequency{{Term: "late-output", Frequency: 1}}, SourceFingerprint: "source-" + pointID,
				CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
				ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
				ExpectedClassificationRevision: 1, Classification: SensitivityNonSecret, ClassificationRevision: 1,
				CoverageRevision: 2, PipelineRevision: 2, IndexRevision: 2, Coverage: FieldCoverageComplete,
			}
			attempt := model.RecoveryPointLifecycleAttempt{
				ID: strings.Repeat("e", 32), RecoveryPointID: pointID,
				Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking),
			}
			if err := harness.db.Create(&attempt).Error; err != nil {
				t.Fatalf("seed lifecycle attempt: %v", err)
			}
			if err := ingest.PublishContentProjection(context.Background(), projection); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("late %s ingest error=%v, want ErrConflict", field, err)
			}
			var postings int64
			if err := harness.db.Model(&model.BackupAssetSearchPosting{}).
				Where("search_generation_id = ? AND document_id = ? AND field = ?", searchGeneration.ID, entryID, field).
				Count(&postings).Error; err != nil || postings != 0 {
				t.Fatalf("late %s posting count=%d err=%v, want zero", field, postings, err)
			}
		})
	}
}

func TestContentIndexIngestPublishesEmptyTextCoverageWithoutSyntheticPosting(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	entryID := strings.Repeat("3", 64)
	pointID, catalogID := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: entryID, NormalizedPath: "empty.txt", Name: "empty.txt", EntryType: "file", SecurityState: "non_secret",
	}})
	searchGeneration, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
	if err != nil {
		t.Fatal(err)
	}
	ingest, lease := newContentIngestForHarness(t, harness)
	excerpt := strings.Repeat("3", 32)
	if err := ingest.PublishContentProjection(context.Background(), ContentProjection{
		Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, Field: SearchFieldContent,
		Terms: nil, SourceFingerprint: "source-" + pointID, CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
		ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
		ExpectedClassificationRevision: 1, Classification: SensitivityNonSecret, ClassificationRevision: 1,
		CoverageRevision: 2, PipelineRevision: 2, IndexRevision: 2, ExcerptRef: &excerpt, Coverage: FieldCoverageComplete,
	}); err != nil {
		t.Fatalf("publish empty content coverage: %v", err)
	}
	snapshot := contentProjectionSnapshot(t, harness, searchGeneration.ID, entryID, SearchFieldContent)
	if snapshot.Postings != 0 || snapshot.State != string(FieldCoverageComplete) || snapshot.ExcerptRef != excerpt || snapshot.IndexRevision != 2 {
		t.Fatalf("empty content projection=%+v", snapshot)
	}
}

func TestContentIndexIngestTxUsesCallerTransactionAndRejectsRootHandle(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	entryID := strings.Repeat("5", 64)
	pointID, catalogID := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: entryID, NormalizedPath: "caller-tx.txt", Name: "caller-tx.txt", EntryType: "file", SecurityState: "non_secret",
	}})
	searchGeneration, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
	if err != nil {
		t.Fatal(err)
	}
	ingest, lease := newContentIngestForHarness(t, harness)
	projection := ContentProjection{
		Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, Field: SearchFieldContent,
		Terms: []TermFrequency{{Term: "caller-transaction", Frequency: 1}}, SourceFingerprint: "source-" + pointID,
		CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
		ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
		ExpectedClassificationRevision: 1, Classification: SensitivityNonSecret, ClassificationRevision: 1,
		CoverageRevision: 2, PipelineRevision: 2, IndexRevision: 2, Coverage: FieldCoverageComplete,
	}
	prepared, err := ingest.PrepareContentProjection(context.Background(), projection)
	if err != nil {
		t.Fatal(err)
	}
	if err := ingest.PublishContentProjectionTx(context.Background(), harness.db, prepared); !errors.Is(err, ErrInvalidContentProjection) {
		t.Fatalf("root DB handle error=%v", err)
	}

	before := contentProjectionSnapshot(t, harness, searchGeneration.ID, entryID, SearchFieldContent)
	rollback := errors.New("force outer rollback")
	err = harness.db.Transaction(func(tx *gorm.DB) error {
		if err := ingest.PublishContentProjectionTx(context.Background(), tx, prepared); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("outer transaction error=%v", err)
	}
	after := contentProjectionSnapshot(t, harness, searchGeneration.ID, entryID, SearchFieldContent)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("caller rollback leaked Search state: before=%+v after=%+v", before, after)
	}
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		return ingest.PublishContentProjectionTx(context.Background(), tx, prepared)
	}); err != nil {
		t.Fatalf("caller transaction publish: %v", err)
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

func TestClassificationIndexIngestUsesCallerTransactionAndInvalidatesBothFields(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	entryID := strings.Repeat("a", 64)
	pointID, catalogID := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: entryID, NormalizedPath: "classified-evidence.txt", Name: "classified-evidence.txt",
		EntryType: "file", SecurityState: "non_secret",
	}})
	searchGeneration, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
	if err != nil {
		t.Fatal(err)
	}
	ingest, lease := newContentIngestForHarness(t, harness)
	for index, field := range []SearchField{SearchFieldContent, SearchFieldOCR} {
		excerpt := strings.Repeat(string(rune('b'+index)), 32)
		if err := ingest.PublishContentProjection(context.Background(), ContentProjection{
			Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, Field: field,
			Terms: []TermFrequency{{Term: "must-disappear", Frequency: 1}}, SourceFingerprint: "source-" + pointID,
			CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
			ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
			ExpectedClassificationRevision: 1, Classification: SensitivityNonSecret, ClassificationRevision: 1,
			CoverageRevision: 2, PipelineRevision: 2, IndexRevision: 2,
			ExcerptRef: &excerpt, Coverage: FieldCoverageComplete,
		}); err != nil {
			t.Fatalf("publish %s fixture: %v", field, err)
		}
	}
	projection := ClassificationProjection{
		Ref:               backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID},
		SourceFingerprint: "source-" + pointID, CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
		ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
		ExpectedClassificationRevision: 1, Classification: SensitivitySecret, ClassificationRevision: 2,
		EvidenceArtifactID: strings.Repeat("d", 32),
	}

	for _, testCase := range []struct {
		name   string
		mutate func(*ClassificationProjection)
		want   error
	}{
		{name: "source", mutate: func(value *ClassificationProjection) { value.SourceFingerprint = "drift" }, want: ErrSearchSourceChanged},
		{name: "fence", mutate: func(value *ClassificationProjection) { value.FenceToken = strings.Repeat("0", 64) }, want: backupasset.ErrLeaseFenceLost},
		{name: "classification revision", mutate: func(value *ClassificationProjection) {
			value.ExpectedClassificationRevision = 2
			value.ClassificationRevision = 3
		}, want: ErrContentProjectionStale},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := projection
			testCase.mutate(&candidate)
			prepared, prepareErr := ingest.PrepareClassificationProjection(context.Background(), candidate)
			if prepareErr != nil {
				t.Fatalf("prepare rejected syntactically valid stale classification: %v", prepareErr)
			}
			before := classificationProjectionSnapshot(t, harness, searchGeneration.ID, entryID)
			err := harness.db.Transaction(func(tx *gorm.DB) error {
				return ingest.PublishClassificationProjectionTx(context.Background(), tx, prepared)
			})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("classification error=%v, want %v", err, testCase.want)
			}
			if after := classificationProjectionSnapshot(t, harness, searchGeneration.ID, entryID); !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected classification changed Search: before=%+v after=%+v", before, after)
			}
		})
	}

	prepared, err := ingest.PrepareClassificationProjection(context.Background(), projection)
	if err != nil {
		t.Fatal(err)
	}
	if err := ingest.PublishClassificationProjectionTx(context.Background(), harness.db, prepared); !errors.Is(err, ErrInvalidContentProjection) {
		t.Fatalf("root DB classification error=%v", err)
	}
	before := classificationProjectionSnapshot(t, harness, searchGeneration.ID, entryID)
	rollback := errors.New("force classification rollback")
	err = harness.db.Transaction(func(tx *gorm.DB) error {
		if err := ingest.PublishClassificationProjectionTx(context.Background(), tx, prepared); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("classification outer rollback error=%v", err)
	}
	if after := classificationProjectionSnapshot(t, harness, searchGeneration.ID, entryID); !reflect.DeepEqual(before, after) {
		t.Fatalf("classification rollback leaked Search: before=%+v after=%+v", before, after)
	}
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		return ingest.PublishClassificationProjectionTx(context.Background(), tx, prepared)
	}); err != nil {
		t.Fatalf("publish classification: %v", err)
	}
	after := classificationProjectionSnapshot(t, harness, searchGeneration.ID, entryID)
	if after.Document.Sensitivity != string(SensitivitySecret) || after.Document.ClassificationRevision != 2 || after.Postings != 0 {
		t.Fatalf("classification document/postings=%+v", after)
	}
	for _, field := range after.Fields {
		if field.State != string(FieldCoverageUnavailable) || field.ClassificationRevision != 2 || field.ExcerptRef != nil ||
			field.CoverageRevision != 3 || field.IndexRevision != 3 {
			t.Fatalf("classification field remained visible: %+v", field)
		}
	}
}

func TestClassificationProjectionValidationRequiresEvidenceForProofOnly(t *testing.T) {
	service := &ContentIngestService{db: &gorm.DB{}}
	projection := ClassificationProjection{
		Ref:               backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64)},
		SourceFingerprint: "classification-source", CatalogGenerationID: strings.Repeat("3", 32),
		SearchGenerationID: strings.Repeat("4", 32), ProcessingLeaseID: strings.Repeat("5", 32),
		AttemptID: strings.Repeat("6", 32), FenceToken: strings.Repeat("7", 64),
		ExpectedClassificationRevision: 1, ClassificationRevision: 2,
	}
	projection.Classification = SensitivityUnknown
	if err := service.validateClassificationProjection(projection); err != nil {
		t.Fatalf("evidence-free unknown revocation rejected: %v", err)
	}
	projection.Classification = SensitivitySecret
	if err := service.validateClassificationProjection(projection); !errors.Is(err, ErrInvalidContentProjection) {
		t.Fatalf("evidence-free secret error=%v", err)
	}
	projection.Classification = SensitivityNonSecret
	if err := service.validateClassificationProjection(projection); !errors.Is(err, ErrInvalidContentProjection) {
		t.Fatalf("evidence-free non-secret error=%v", err)
	}
	projection.EvidenceArtifactID = strings.Repeat("8", 32)
	for _, sensitivity := range []Sensitivity{SensitivityNonSecret, SensitivitySecret, SensitivityUnknown} {
		projection.Classification = sensitivity
		if err := service.validateClassificationProjection(projection); err != nil {
			t.Fatalf("classification %q with evidence rejected: %v", sensitivity, err)
		}
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
	revoke := RevokeProjection{
		Ref: publish.Ref, Field: publish.Field, SourceFingerprint: publish.SourceFingerprint,
		CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
		ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
		ExpectedClassificationRevision: 1, CoverageRevision: 3, PipelineRevision: 3, IndexRevision: 3,
	}
	if err := ingest.RevokeContentProjectionTx(context.Background(), harness.db, revoke); !errors.Is(err, ErrInvalidContentProjection) {
		t.Fatalf("root DB revoke error=%v", err)
	}
	before := contentProjectionSnapshot(t, harness, searchGeneration.ID, entryID, SearchFieldContent)
	rollback := errors.New("force revoke rollback")
	err := harness.db.Transaction(func(tx *gorm.DB) error {
		if err := ingest.RevokeContentProjectionTx(context.Background(), tx, revoke); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("outer revoke transaction error=%v", err)
	}
	if afterRollback := contentProjectionSnapshot(t, harness, searchGeneration.ID, entryID, SearchFieldContent); !reflect.DeepEqual(before, afterRollback) {
		t.Fatalf("caller revoke rollback leaked Search state: before=%+v after=%+v", before, afterRollback)
	}
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		return ingest.RevokeContentProjectionTx(context.Background(), tx, revoke)
	}); err != nil {
		t.Fatalf("RevokeContentProjectionTx: %v", err)
	}
	snapshot := contentProjectionSnapshot(t, harness, searchGeneration.ID, entryID, SearchFieldContent)
	if snapshot.Postings != 0 || snapshot.State != string(FieldCoverageUnavailable) || snapshot.ExcerptRef != "" || snapshot.IndexRevision != 3 {
		t.Fatalf("revoked projection remains visible: %+v", snapshot)
	}
}

func TestProjectionRevokeAndRebuildKeepSameActivePipelineRevision(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	entryID := strings.Repeat("4", 64)
	pointID, catalogID := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: entryID, NormalizedPath: "same-pipeline.txt", Name: "same-pipeline.txt", EntryType: "file", SecurityState: "non_secret",
	}})
	searchGeneration, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
	if err != nil {
		t.Fatal(err)
	}
	ingest, lease := newContentIngestForHarness(t, harness)
	projection := ContentProjection{
		Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, Field: SearchFieldContent,
		Terms: []TermFrequency{{Term: "first", Frequency: 1}}, SourceFingerprint: "source-" + pointID,
		CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
		ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
		ExpectedClassificationRevision: 1, Classification: SensitivityNonSecret, ClassificationRevision: 1,
		CoverageRevision: 2, PipelineRevision: 2, IndexRevision: 2,
		ExcerptRef: stringPointer(strings.Repeat("1", 32)), Coverage: FieldCoverageComplete,
	}
	if err := ingest.PublishContentProjection(context.Background(), projection); err != nil {
		t.Fatal(err)
	}
	if err := ingest.RevokeContentProjection(context.Background(), RevokeProjection{
		Ref: projection.Ref, Field: projection.Field, SourceFingerprint: projection.SourceFingerprint,
		CatalogGenerationID: catalogID, SearchGenerationID: searchGeneration.ID,
		ProcessingLeaseID: lease.ID, AttemptID: lease.Fence.AttemptID, FenceToken: lease.Fence.FenceToken,
		ExpectedClassificationRevision: 1, CoverageRevision: 3, PipelineRevision: 2, IndexRevision: 3,
	}); err != nil {
		t.Fatalf("same-pipeline revoke: %v", err)
	}
	projection.Terms = []TermFrequency{{Term: "second", Frequency: 1}}
	projection.CoverageRevision = 4
	projection.PipelineRevision = 2
	projection.IndexRevision = 4
	projection.ExcerptRef = stringPointer(strings.Repeat("2", 32))
	if err := ingest.PublishContentProjection(context.Background(), projection); err != nil {
		t.Fatalf("same-pipeline rebuild: %v", err)
	}
	snapshot := contentProjectionSnapshot(t, harness, searchGeneration.ID, entryID, SearchFieldContent)
	if snapshot.Postings == 0 || snapshot.State != string(FieldCoverageComplete) || snapshot.ExcerptRef != strings.Repeat("2", 32) ||
		snapshot.IndexRevision != 4 {
		t.Fatalf("same-pipeline rebuild snapshot=%+v", snapshot)
	}
}

type contentSnapshot struct {
	Postings      int64
	State         string
	ExcerptRef    string
	IndexRevision int
	ProjectionRev int64
}

type classificationSnapshot struct {
	Document      model.BackupAssetSearchDocument
	Fields        []model.BackupAssetSearchDocumentField
	Postings      int64
	ProjectionRev int64
}

func classificationProjectionSnapshot(
	t *testing.T,
	harness *indexerTestHarness,
	searchID string,
	entryID string,
) classificationSnapshot {
	t.Helper()
	var snapshot classificationSnapshot
	if err := harness.db.Where("search_generation_id = ? AND document_id = ?", searchID, entryID).
		Take(&snapshot.Document).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Where("search_generation_id = ? AND document_id = ? AND field IN ?", searchID, entryID,
		[]SearchField{SearchFieldContent, SearchFieldOCR}).Order("field ASC").Find(&snapshot.Fields).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetSearchPosting{}).
		Where("search_generation_id = ? AND document_id = ? AND field IN ?", searchID, entryID,
			[]SearchField{SearchFieldContent, SearchFieldOCR}).Count(&snapshot.Postings).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetSearchGeneration{}).Select("projection_revision").
		Where("id = ?", searchID).Scan(&snapshot.ProjectionRev).Error; err != nil {
		t.Fatal(err)
	}
	return snapshot
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
