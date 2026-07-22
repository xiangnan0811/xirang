package search

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/model"
)

func TestSearchServiceMetadataRankingCoverageAndCursor(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{
		{EntryID: strings.Repeat("1", 64), NormalizedPath: "A/Report.txt", Name: "Report.txt", EntryType: "file", Size: 10, SecurityState: "non_secret"},
		{EntryID: strings.Repeat("2", 64), NormalizedPath: "Z/Other.txt", Name: "Other.txt", EntryType: "file", Size: 20, SecurityState: "non_secret"},
	})
	if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); err != nil {
		t.Fatalf("build Search projection: %v", err)
	}
	service := newSearchServiceForHarness(t, harness, map[string]bool{pointID: true}, nil)
	request := SearchRequest{
		SchemaVersion: QuerySchemaVersion, Root: QueryNode{Op: QueryOpType, Values: []string{"file"}},
		Scope: SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:  SearchSortRelevance, Limit: 1,
	}
	first, err := service.Search(context.Background(), SearchActor{Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1}}, request)
	if err != nil {
		t.Fatalf("Search first page: %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].Ref.EntryID != strings.Repeat("1", 64) || first.NextCursor == "" ||
		first.Total == nil || *first.Total != 2 || first.TotalRelation != TotalRelationExact || first.AuthoritativeEmpty {
		t.Fatalf("invalid first page: %+v", first)
	}
	if !reflect.DeepEqual(first.Items[0].HitFields, []SearchField{SearchFieldType}) || first.Items[0].Score <= 0 {
		t.Fatalf("invalid hit facts: %+v", first.Items[0])
	}
	request.Cursor = first.NextCursor
	second, err := service.Search(context.Background(), SearchActor{Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1}}, request)
	if err != nil {
		t.Fatalf("Search second page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Ref.EntryID != strings.Repeat("2", 64) || second.NextCursor != "" ||
		second.QueryGeneration != first.QueryGeneration {
		t.Fatalf("invalid second page: %+v", second)
	}

	emptyRequest := request
	emptyRequest.Cursor = ""
	emptyRequest.Root = QueryNode{Op: QueryOpTerm, Field: SearchFieldName, Text: "missing"}
	empty, err := service.Search(context.Background(), SearchActor{Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1}}, emptyRequest)
	if err != nil || len(empty.Items) != 0 || empty.Total == nil || *empty.Total != 0 || !empty.AuthoritativeEmpty {
		t.Fatalf("complete empty result was not authoritative: result=%+v err=%v", empty, err)
	}
}

func TestSearchServiceUsesPositivePostingCandidatesBeforeLimit(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{
		{EntryID: strings.Repeat("8", 64), NormalizedPath: "needle.txt", Name: "needle.txt", EntryType: "file", SecurityState: "non_secret"},
		{EntryID: strings.Repeat("9", 64), NormalizedPath: "other.txt", Name: "other.txt", EntryType: "file", SecurityState: "non_secret"},
		{EntryID: strings.Repeat("a", 64), NormalizedPath: "third.txt", Name: "third.txt", EntryType: "file", SecurityState: "non_secret"},
	})
	if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); err != nil {
		t.Fatalf("build Search projection: %v", err)
	}
	service := newSearchServiceForHarness(t, harness, map[string]bool{pointID: true}, nil)
	service.limits.MaxCandidates = 1
	service.limits.Query.MaxCandidates = 1
	service.limits.Query.MaxPageSize = 1

	response, err := service.Search(context.Background(), SearchActor{
		Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1},
	}, SearchRequest{
		SchemaVersion: QuerySchemaVersion,
		Root:          QueryNode{Op: QueryOpTerm, Field: SearchFieldName, Text: "needle"},
		Scope:         SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:          SearchSortRelevance,
		Limit:         1,
	})
	if err != nil {
		t.Fatalf("selective Search exceeded the whole-projection limit: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Ref.EntryID != strings.Repeat("8", 64) ||
		response.Total == nil || *response.Total != 1 || response.Coverage.Status != CoverageComplete {
		t.Fatalf("selective Search result mismatch: %+v", response)
	}
}

func TestSearchServiceAnyUsesPositiveCandidatesBeforeLimit(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{
		{EntryID: strings.Repeat("d", 64), NormalizedPath: "needle.txt", Name: "needle.txt", EntryType: "file", SecurityState: "non_secret"},
		{EntryID: strings.Repeat("e", 64), NormalizedPath: "other.txt", Name: "other.txt", EntryType: "file", SecurityState: "non_secret"},
		{EntryID: strings.Repeat("f", 64), NormalizedPath: "third.txt", Name: "third.txt", EntryType: "file", SecurityState: "non_secret"},
	})
	if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); err != nil {
		t.Fatalf("build Search projection: %v", err)
	}
	service := newSearchServiceForHarness(t, harness, map[string]bool{pointID: true}, nil)
	service.limits.MaxCandidates = 1
	service.limits.Query.MaxCandidates = 1
	service.limits.Query.MaxPageSize = 1

	response, err := service.Search(context.Background(), SearchActor{
		Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1},
	}, SearchRequest{
		SchemaVersion: QuerySchemaVersion,
		Root:          QueryNode{Op: QueryOpTerm, Field: SearchFieldAny, Text: "needle"},
		Scope:         SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:          SearchSortRelevance,
		Limit:         1,
	})
	if err != nil {
		t.Fatalf("selective any Search exceeded the whole-projection limit: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Ref.EntryID != strings.Repeat("d", 64) {
		t.Fatalf("selective any Search result mismatch: %+v", response)
	}
}

func TestSearchServiceUsesOwnerTagCandidatesBeforeLimit(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	targetEntryID := strings.Repeat("3", 64)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{
		{EntryID: strings.Repeat("1", 64), NormalizedPath: "first.txt", Name: "first.txt", EntryType: "file", SecurityState: "non_secret"},
		{EntryID: strings.Repeat("2", 64), NormalizedPath: "second.txt", Name: "second.txt", EntryType: "file", SecurityState: "non_secret"},
		{EntryID: targetEntryID, NormalizedPath: "target.txt", Name: "target.txt", EntryType: "file", SecurityState: "non_secret"},
	})
	if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); err != nil {
		t.Fatalf("build Search projection: %v", err)
	}
	service := newSearchServiceForHarness(t, harness, map[string]bool{pointID: true}, nil)
	targetRef := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: targetEntryID}
	service.tags = &candidateTagResolverFake{
		revision: strings.Repeat("a", 64),
		matches:  map[backupasset.AssetRef]bool{targetRef: true},
	}
	service.limits.MaxCandidates = 1
	service.limits.Query.MaxCandidates = 1
	service.limits.Query.MaxPageSize = 1

	response, err := service.Search(context.Background(), SearchActor{
		Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1},
	}, SearchRequest{
		SchemaVersion: QuerySchemaVersion,
		Root:          QueryNode{Op: QueryOpTerm, Field: SearchFieldTag, Text: "finance"},
		Scope:         SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:          SearchSortRelevance,
		Limit:         1,
	})
	if err != nil {
		t.Fatalf("selective tag Search exceeded the whole-projection limit: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Ref != targetRef {
		t.Fatalf("selective tag Search result mismatch: %+v", response)
	}
}

func TestSearchServicePathLeafProximityAffectsRelevance(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	deepEntryID := strings.Repeat("4", 64)
	nearEntryID := strings.Repeat("5", 64)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{
		{EntryID: deepEntryID, NormalizedPath: "a/target/z/file.txt", Name: "file.txt", EntryType: "file", SecurityState: "non_secret"},
		{EntryID: nearEntryID, NormalizedPath: "z/target", Name: "target", EntryType: "file", SecurityState: "non_secret"},
	})
	if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); err != nil {
		t.Fatalf("build Search projection: %v", err)
	}
	service := newSearchServiceForHarness(t, harness, map[string]bool{pointID: true}, nil)
	response, err := service.Search(context.Background(), SearchActor{
		Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1},
	}, SearchRequest{
		SchemaVersion: QuerySchemaVersion,
		Root:          QueryNode{Op: QueryOpTerm, Field: SearchFieldPath, Text: "target"},
		Scope:         SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:          SearchSortRelevance,
		Limit:         10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 2 || response.Items[0].Ref.EntryID != nearEntryID ||
		response.Items[0].Score-response.Items[1].Score != 2 {
		t.Fatalf("path proximity ranking mismatch: %+v", response.Items)
	}
}

func TestSearchServiceTagRevisionChangeStalesCursor(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{
		{EntryID: strings.Repeat("b", 64), NormalizedPath: "first.txt", Name: "first.txt", EntryType: "file", SecurityState: "non_secret"},
		{EntryID: strings.Repeat("c", 64), NormalizedPath: "second.txt", Name: "second.txt", EntryType: "file", SecurityState: "non_secret"},
	})
	if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); err != nil {
		t.Fatalf("build Search projection: %v", err)
	}
	service := newSearchServiceForHarness(t, harness, map[string]bool{pointID: true}, nil)
	tags := &revisionTagResolverFake{
		revision: strings.Repeat("1", 64),
		candidates: []backupasset.AssetRef{
			{RecoveryPointID: pointID, EntryID: strings.Repeat("b", 64)},
			{RecoveryPointID: pointID, EntryID: strings.Repeat("c", 64)},
		},
	}
	service.tags = tags
	request := SearchRequest{
		SchemaVersion: QuerySchemaVersion,
		Root:          QueryNode{Op: QueryOpTerm, Field: SearchFieldTag, Text: "finance"},
		Scope:         SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:          SearchSortRelevance,
		Limit:         1,
	}
	first, err := service.Search(context.Background(), SearchActor{
		Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1},
	}, request)
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first tag page result=%+v err=%v", first, err)
	}
	tags.revision = strings.Repeat("2", 64)
	request.Cursor = first.NextCursor
	if _, err := service.Search(context.Background(), SearchActor{
		Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1},
	}, request); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("tag revision drift got %v, want ErrStaleCursor", err)
	}
}

func TestSearchServiceCurrentNewestUnindexedDoesNotFallback(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	oldID, _ := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: strings.Repeat("3", 64), NormalizedPath: "old.txt", Name: "old.txt", EntryType: "file", SecurityState: "non_secret",
	}})
	if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: oldID}); err != nil {
		t.Fatalf("build old Search: %v", err)
	}
	newID, _ := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: strings.Repeat("4", 64), NormalizedPath: "new.txt", Name: "new.txt", EntryType: "file", SecurityState: "non_secret",
	}})
	newTime := harness.now.Add(time.Minute)
	if err := harness.db.Model(&model.RecoveryPoint{}).Where("id = ?", newID).
		Updates(map[string]any{"created_at": newTime, "committed_at": newTime}).Error; err != nil {
		t.Fatal(err)
	}
	authorizer := map[string]bool{oldID: true, newID: true}
	service := newSearchServiceForHarness(t, harness, authorizer, nil)
	response, err := service.Search(context.Background(), SearchActor{Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1}}, SearchRequest{
		SchemaVersion: QuerySchemaVersion, Root: QueryNode{Op: QueryOpTerm, Field: SearchFieldName, Text: "old"},
		Scope: SearchScope{Mode: SearchScopeCurrent}, Sort: SearchSortRelevance, Limit: 25,
	})
	if err != nil {
		t.Fatalf("Search current: %v", err)
	}
	if len(response.Items) != 0 || response.Total != nil || response.AuthoritativeEmpty || len(response.Indexes) != 1 ||
		response.Indexes[0].RecoveryPointID != newID || response.Indexes[0].Coverage == CoverageComplete {
		t.Fatalf("current search fell back or overstated coverage: %+v", response)
	}
}

func TestSearchServiceKleeneHiddenContentExposesNoFactsWithoutExactProof(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	entryID := strings.Repeat("5", 64)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: entryID, NormalizedPath: "visible.txt", Name: "visible.txt", EntryType: "file", SecurityState: "secret",
	}})
	generation, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := harness.ring.Active(context.Background(), backupasset.KeyDomainSearchToken)
	token, _ := TokenHMAC(key.Key, key.Version, NormalizerVersion, SearchFieldContent, TokenKindExact, "needle")
	if err := harness.db.Create(&model.BackupAssetSearchPosting{
		SearchGenerationID: generation.ID, DocumentID: entryID, Field: string(SearchFieldContent),
		TokenKind: string(TokenKindExact), KeyVersion: key.Version, TokenHMAC: token, TermFrequency: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetSearchDocumentField{}).
		Where("search_generation_id = ? AND document_id = ? AND field = ?", generation.ID, entryID, SearchFieldContent).
		Updates(map[string]any{"state": FieldCoverageComplete, "excerpt_ref": strings.Repeat("a", 32)}).Error; err != nil {
		t.Fatal(err)
	}
	resolver := &excerptResolverFake{match: true, snippet: "verified excerpt"}
	service := newSearchServiceForHarness(t, harness, map[string]bool{pointID: true}, resolver)
	actor := SearchActor{Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1}}
	contentLeaf := QueryNode{Op: QueryOpTerm, Field: SearchFieldContent, Text: "needle"}
	request := SearchRequest{
		SchemaVersion: QuerySchemaVersion,
		Root: QueryNode{Op: QueryOpOr, Children: []QueryNode{
			{Op: QueryOpTerm, Field: SearchFieldName, Text: "visible"}, contentLeaf,
		}},
		Scope: SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:  SearchSortRelevance, Limit: 25,
	}
	withoutProof, err := service.Search(context.Background(), actor, request)
	if err != nil || len(withoutProof.Items) != 1 {
		t.Fatalf("metadata OR hidden content result=%+v err=%v", withoutProof, err)
	}
	if !reflect.DeepEqual(withoutProof.Items[0].HitFields, []SearchField{SearchFieldName}) || withoutProof.Items[0].Snippet != nil || resolver.calls != 0 {
		t.Fatalf("hidden content leaked facts: hit=%+v resolver_calls=%d", withoutProof.Items[0], resolver.calls)
	}
	if !reflect.DeepEqual(withoutProof.Suggestions, []SearchSuggestion{{Field: SearchFieldName, Value: "visible.txt"}}) {
		t.Fatalf("metadata-only suggestions mismatch: %+v", withoutProof.Suggestions)
	}

	request.Root = QueryNode{Op: QueryOpNot, Children: []QueryNode{contentLeaf}}
	notHidden, err := service.Search(context.Background(), actor, request)
	if err != nil || len(notHidden.Items) != 0 {
		t.Fatalf("NOT unknown became true: result=%+v err=%v", notHidden, err)
	}

	actor.SecretProof = &SecretRevealProof{ID: "proof-1", ExpiresAt: harness.now.Add(5 * time.Minute)}
	request.Root = contentLeaf
	withProof, err := service.Search(context.Background(), actor, request)
	if err != nil || len(withProof.Items) != 1 || !reflect.DeepEqual(withProof.Items[0].HitFields, []SearchField{SearchFieldContent}) ||
		withProof.Items[0].Snippet == nil || resolver.calls != 1 {
		t.Fatalf("valid proof content result=%+v calls=%d err=%v", withProof, resolver.calls, err)
	}
}

func TestSearchServiceExcerptResolverFailureCannotClaimAuthoritativeEmpty(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	entryID := strings.Repeat("6", 64)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: entryID, NormalizedPath: "content.txt", Name: "content.txt", EntryType: "file", SecurityState: "non_secret",
	}})
	generation, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
	if err != nil {
		t.Fatal(err)
	}
	key, err := harness.ring.Active(context.Background(), backupasset.KeyDomainSearchToken)
	if err != nil {
		t.Fatal(err)
	}
	token, err := TokenHMAC(key.Key, key.Version, NormalizerVersion, SearchFieldContent, TokenKindExact, "needle")
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Create(&model.BackupAssetSearchPosting{
		SearchGenerationID: generation.ID, DocumentID: entryID, Field: string(SearchFieldContent),
		TokenKind: string(TokenKindExact), KeyVersion: key.Version, TokenHMAC: token, TermFrequency: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetSearchDocumentField{}).
		Where("search_generation_id = ? AND document_id = ? AND field = ?", generation.ID, entryID, SearchFieldContent).
		Updates(map[string]any{"state": FieldCoverageComplete, "excerpt_ref": strings.Repeat("b", 32)}).Error; err != nil {
		t.Fatal(err)
	}
	service := newSearchServiceForHarness(t, harness, map[string]bool{pointID: true}, &excerptResolverFake{
		err: errors.New("excerpt resolver unavailable"),
	})
	response, err := service.Search(context.Background(), SearchActor{
		Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1},
	}, SearchRequest{
		SchemaVersion: QuerySchemaVersion, Root: QueryNode{Op: QueryOpTerm, Field: SearchFieldContent, Text: "needle"},
		Scope: SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:  SearchSortRelevance, Limit: 25,
	})
	if err != nil {
		t.Fatalf("Search with unavailable excerpt resolver: %v", err)
	}
	if len(response.Items) != 0 || response.Total != nil || response.AuthoritativeEmpty ||
		response.Coverage.Status == CoverageComplete || response.Capabilities.Content {
		t.Fatalf("resolver failure overstated content coverage: %+v", response)
	}
}

func TestSearchServiceMalwareReleaseGateHidesUnsafeOrStaleDerivedEvidence(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		field SearchField
		err   error
	}{
		{name: "content unsafe", field: SearchFieldContent},
		{name: "content stale", field: SearchFieldContent, err: errors.New("malware evidence stale")},
		{name: "ocr unsafe", field: SearchFieldOCR},
		{name: "ocr stale", field: SearchFieldOCR, err: errors.New("malware evidence stale")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			indexer, harness := newIndexerTestHarness(t)
			entryID := strings.Repeat("8", 64)
			pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{{
				EntryID: entryID, NormalizedPath: "unsafe-content.txt", Name: "unsafe-content.txt", EntryType: "file",
				Size: 4096, MimeType: "text/plain", Fingerprint: strings.Repeat("9", 64),
				FingerprintStrength: string(catalog.FingerprintStrong), SecurityState: "non_secret",
			}})
			generation, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
			if err != nil {
				t.Fatal(err)
			}
			key, err := harness.ring.Active(context.Background(), backupasset.KeyDomainSearchToken)
			if err != nil {
				t.Fatal(err)
			}
			token, err := TokenHMAC(key.Key, key.Version, NormalizerVersion, testCase.field, TokenKindExact, "needle")
			if err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Create(&model.BackupAssetSearchPosting{
				SearchGenerationID: generation.ID, DocumentID: entryID, Field: string(testCase.field),
				TokenKind: string(TokenKindExact), KeyVersion: key.Version, TokenHMAC: token, TermFrequency: 1,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := harness.db.Model(&model.BackupAssetSearchDocumentField{}).
				Where("search_generation_id = ? AND document_id = ? AND field = ?", generation.ID, entryID, testCase.field).
				Updates(map[string]any{"state": FieldCoverageComplete, "excerpt_ref": strings.Repeat("d", 32)}).Error; err != nil {
				t.Fatal(err)
			}
			resolver := &excerptResolverFake{match: true, snippet: "must not be released"}
			service := newSearchServiceForHarness(t, harness, map[string]bool{pointID: true}, resolver)
			var requests []MalwareSafetyRequest
			service.malwareSafety = func(_ context.Context, request MalwareSafetyRequest) (bool, error) {
				requests = append(requests, request)
				return false, testCase.err
			}

			response, err := service.Search(context.Background(), SearchActor{
				Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1},
			}, SearchRequest{
				SchemaVersion: QuerySchemaVersion, Root: QueryNode{Op: QueryOpTerm, Field: testCase.field, Text: "needle"},
				Scope: SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
				Sort:  SearchSortRelevance, Limit: 25,
			})
			if err != nil {
				t.Fatalf("Search with blocked malware evidence: %v", err)
			}
			if len(response.Items) != 0 || response.Total != nil || response.TotalRelation != TotalRelationUnavailable ||
				response.AuthoritativeEmpty || len(response.Suggestions) != 0 ||
				response.Coverage.Status == CoverageComplete || response.Capabilities.Content || resolver.calls != 0 {
				t.Fatalf("blocked malware evidence leaked Search output: response=%+v resolver_calls=%d", response, resolver.calls)
			}
			if len(requests) != 1 {
				t.Fatalf("malware release checks=%d, want 1", len(requests))
			}
			var point model.RecoveryPoint
			if err := harness.db.First(&point, "id = ?", pointID).Error; err != nil {
				t.Fatal(err)
			}
			request := requests[0]
			if request.Ref != (backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}) ||
				request.CatalogGenerationID != generation.CatalogGenerationID || request.SourceFingerprint != point.SourceFingerprint ||
				request.EntryFingerprint != strings.Repeat("9", 64) || request.FingerprintStrength != string(catalog.FingerprintStrong) ||
				request.ProviderCapabilityRevision != int64(point.CapabilityRevision) || request.Size != 4096 || request.MediaType != "text/plain" {
				t.Fatalf("malware release request=%+v", request)
			}

			requests = nil
			resolver.calls = 0
			response, err = service.Search(context.Background(), SearchActor{
				Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1},
			}, SearchRequest{
				SchemaVersion: QuerySchemaVersion,
				Root: QueryNode{Op: QueryOpNot, Children: []QueryNode{{
					Op: QueryOpTerm, Field: testCase.field, Text: "different-term",
				}}},
				Scope: SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
				Sort:  SearchSortRelevance, Limit: 25,
			})
			if err != nil {
				t.Fatalf("Search NOT %s with blocked malware evidence: %v", testCase.field, err)
			}
			if len(requests) != 1 {
				t.Fatalf("NOT %s malware release checks=%d, want 1", testCase.field, len(requests))
			}
			if len(response.Items) != 0 || response.Total != nil || response.TotalRelation != TotalRelationUnavailable ||
				response.AuthoritativeEmpty || len(response.Suggestions) != 0 ||
				response.Coverage.Status == CoverageComplete || response.Capabilities.Content || resolver.calls != 0 {
				t.Fatalf("blocked malware evidence leaked through NOT %s: response=%+v resolver_calls=%d", testCase.field, response, resolver.calls)
			}
		})
	}
}

func TestSearchServicePipelineRevisionMismatchHidesOldContentProjection(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	entryID := strings.Repeat("7", 64)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: entryID, NormalizedPath: "stale-content.txt", Name: "stale-content.txt", EntryType: "file", SecurityState: "non_secret",
	}})
	generation, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID})
	if err != nil {
		t.Fatal(err)
	}
	key, err := harness.ring.Active(context.Background(), backupasset.KeyDomainSearchToken)
	if err != nil {
		t.Fatal(err)
	}
	token, err := TokenHMAC(key.Key, key.Version, NormalizerVersion, SearchFieldContent, TokenKindExact, "old-needle")
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Create(&model.BackupAssetSearchPosting{
		SearchGenerationID: generation.ID, DocumentID: entryID, Field: string(SearchFieldContent),
		TokenKind: string(TokenKindExact), KeyVersion: key.Version, TokenHMAC: token, TermFrequency: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetSearchDocumentField{}).
		Where("search_generation_id = ? AND document_id = ? AND field = ?", generation.ID, entryID, SearchFieldContent).
		Updates(map[string]any{
			"state": FieldCoverageComplete, "pipeline_revision": 2, "excerpt_ref": strings.Repeat("c", 32),
		}).Error; err != nil {
		t.Fatal(err)
	}
	resolver := &excerptResolverFake{match: true, snippet: "must stay hidden"}
	service := newSearchServiceForHarness(t, harness, map[string]bool{pointID: true}, resolver)
	service.pipelineRevisions = func(context.Context) (ContentPipelineRevisions, error) {
		return ContentPipelineRevisions{Content: 3, OCR: 1}, nil
	}
	response, err := service.Search(context.Background(), SearchActor{
		Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1},
	}, SearchRequest{
		SchemaVersion: QuerySchemaVersion, Root: QueryNode{Op: QueryOpTerm, Field: SearchFieldContent, Text: "old-needle"},
		Scope: SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:  SearchSortRelevance, Limit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 0 || response.Total != nil || response.AuthoritativeEmpty ||
		response.Coverage.Status == CoverageComplete || resolver.calls != 0 {
		t.Fatalf("stale content projection remained visible: response=%+v resolver_calls=%d", response, resolver.calls)
	}
}

func TestSearchServiceMissingTagResolverCannotClaimAuthoritativeEmpty(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	pointID, _ := harness.seedCatalog(t, []model.CatalogEntry{{
		EntryID: strings.Repeat("7", 64), NormalizedPath: "tagged.txt", Name: "tagged.txt", EntryType: "file", SecurityState: "non_secret",
	}})
	if _, err := indexer.Build(context.Background(), BuildRequest{RecoveryPointID: pointID}); err != nil {
		t.Fatal(err)
	}
	service := newSearchServiceForHarness(t, harness, map[string]bool{pointID: true}, nil)
	response, err := service.Search(context.Background(), SearchActor{
		Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1},
	}, SearchRequest{
		SchemaVersion: QuerySchemaVersion, Root: QueryNode{Op: QueryOpTerm, Field: SearchFieldTag, Text: "finance"},
		Scope: SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{pointID}},
		Sort:  SearchSortRelevance, Limit: 25,
	})
	if err != nil {
		t.Fatalf("Search without tag resolver: %v", err)
	}
	if len(response.Items) != 0 || response.Total != nil || response.AuthoritativeEmpty || response.Coverage.Status == CoverageComplete {
		t.Fatalf("missing tag resolver overstated tag coverage: %+v", response)
	}
}

func TestSearchServiceRejectsViewerBeforeScopeOrCandidateAccess(t *testing.T) {
	_, harness := newIndexerTestHarness(t)
	authorizer := &scopeTestAuthorizer{allowed: map[string]bool{}}
	scope, _ := NewScopeResolver(harness.db, authorizer, ScopeResolverLimits{MaxCandidates: 100})
	service, err := NewService(ServiceDependencies{
		DB: harness.db, Scope: scope, Keys: harness.ring,
		Cursor: NewCursorCodec(harness.ring, func() time.Time { return harness.now }, 15*time.Minute),
		Now:    func() time.Time { return harness.now }, Limits: DefaultServiceLimits(),
		FeatureEnabled: func() (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Search(context.Background(), SearchActor{Authorization: catalog.AuthorizationScope{Role: "viewer", UserID: 9}}, SearchRequest{
		SchemaVersion: QuerySchemaVersion, Root: QueryNode{Op: QueryOpTerm, Field: SearchFieldName, Text: "secret"},
		Scope: SearchScope{Mode: SearchScopeCurrent}, Sort: SearchSortRelevance, Limit: 25,
	})
	if !errorsIs(err, backupasset.ErrForbidden) || len(authorizer.batchSizes) != 0 {
		t.Fatalf("viewer reached scope/candidate access: err=%v batches=%v", err, authorizer.batchSizes)
	}
}

func TestSearchServiceFeatureDisabledBeforeScopeKeyOrDatabaseAccess(t *testing.T) {
	_, harness := newIndexerTestHarness(t)
	authorizer := &scopeTestAuthorizer{allowed: map[string]bool{}}
	scope, err := NewScopeResolver(harness.db, authorizer, ScopeResolverLimits{MaxCandidates: 100})
	if err != nil {
		t.Fatal(err)
	}
	keys := &countingSearchKeySource{}
	service, err := NewService(ServiceDependencies{
		DB: harness.db, Scope: scope, Keys: keys,
		Cursor: NewCursorCodec(keys, func() time.Time { return harness.now }, 15*time.Minute),
		Now:    func() time.Time { return harness.now }, Limits: DefaultServiceLimits(),
		FeatureEnabled: func() (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Search(context.Background(), SearchActor{Authorization: catalog.AuthorizationScope{Role: "admin", UserID: 1}}, SearchRequest{
		SchemaVersion: QuerySchemaVersion, Root: QueryNode{Op: QueryOpTerm, Field: SearchFieldName, Text: "hidden"},
		Scope: SearchScope{Mode: SearchScopeCurrent}, Sort: SearchSortRelevance, Limit: 25,
	})
	if !errorsIs(err, catalog.ErrFeatureDisabled) || keys.calls != 0 || len(authorizer.batchSizes) != 0 {
		t.Fatalf("disabled Search crossed preflight: err=%v key_calls=%d scope_batches=%v", err, keys.calls, authorizer.batchSizes)
	}
}

func TestSearchServiceRequiresMalwareSafetyForDerivedPipelines(t *testing.T) {
	_, harness := newIndexerTestHarness(t)
	if _, err := harness.ring.Ensure(context.Background(), backupasset.KeyDomainCursorSigning); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScopeResolver(harness.db, &scopeTestAuthorizer{allowed: map[string]bool{}}, ScopeResolverLimits{MaxCandidates: 100})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewService(ServiceDependencies{
		DB: harness.db, Scope: scope, Keys: harness.ring,
		Cursor: NewCursorCodec(harness.ring, func() time.Time { return harness.now }, 15*time.Minute),
		Now:    func() time.Time { return harness.now }, Limits: DefaultServiceLimits(),
		FeatureEnabled: func() (bool, error) { return true, nil },
		PipelineRevisions: func(context.Context) (ContentPipelineRevisions, error) {
			return ContentPipelineRevisions{Content: 1, OCR: 1}, nil
		},
	})
	if err == nil {
		t.Fatal("Search service accepted Derived pipeline revisions without malware release safety")
	}
}

func newSearchServiceForHarness(t *testing.T, harness *indexerTestHarness, allowed map[string]bool, excerpts ExcerptResolver) *Service {
	t.Helper()
	if _, err := harness.ring.Ensure(context.Background(), backupasset.KeyDomainCursorSigning); err != nil {
		t.Fatalf("ensure Search cursor key: %v", err)
	}
	scope, err := NewScopeResolver(harness.db, &scopeTestAuthorizer{allowed: allowed}, ScopeResolverLimits{MaxCandidates: 1000})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceDependencies{
		DB: harness.db, Scope: scope, Keys: harness.ring, Excerpts: excerpts,
		Cursor: NewCursorCodec(harness.ring, func() time.Time { return harness.now }, 15*time.Minute),
		Now:    func() time.Time { return harness.now }, Limits: DefaultServiceLimits(),
		FeatureEnabled: func() (bool, error) { return true, nil },
		PipelineRevisions: func(context.Context) (ContentPipelineRevisions, error) {
			return ContentPipelineRevisions{Content: 1, OCR: 1}, nil
		},
		MalwareSafety: func(context.Context, MalwareSafetyRequest) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type countingSearchKeySource struct{ calls int }

func (source *countingSearchKeySource) Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error) {
	source.calls++
	return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
}

func (source *countingSearchKeySource) ByVersion(context.Context, backupasset.KeyDomain, int) (backupasset.DomainKeyMaterial, error) {
	source.calls++
	return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
}

type excerptResolverFake struct {
	match   bool
	snippet string
	calls   int
	err     error
}

type revisionTagResolverFake struct {
	revision   string
	candidates []backupasset.AssetRef
}

func (*revisionTagResolverFake) Matches(context.Context, uint, backupasset.AssetRef, string) (bool, error) {
	return true, nil
}

func (resolver *revisionTagResolverFake) Revision(context.Context, uint) (string, error) {
	return resolver.revision, nil
}

func (resolver *revisionTagResolverFake) CandidateRefs(context.Context, uint, string, []string, int) ([]backupasset.AssetRef, error) {
	return append([]backupasset.AssetRef(nil), resolver.candidates...), nil
}

type candidateTagResolverFake struct {
	revision string
	matches  map[backupasset.AssetRef]bool
}

func (resolver *candidateTagResolverFake) Matches(_ context.Context, _ uint, ref backupasset.AssetRef, _ string) (bool, error) {
	return resolver.matches[ref], nil
}

func (resolver *candidateTagResolverFake) Revision(context.Context, uint) (string, error) {
	return resolver.revision, nil
}

func (resolver *candidateTagResolverFake) CandidateRefs(_ context.Context, _ uint, _ string, _ []string, limit int) ([]backupasset.AssetRef, error) {
	result := make([]backupasset.AssetRef, 0, len(resolver.matches))
	for ref, matched := range resolver.matches {
		if matched {
			result = append(result, ref)
		}
	}
	if len(result) > limit {
		return nil, ErrResourceLimit
	}
	return result, nil
}

func (resolver *excerptResolverFake) Verify(_ context.Context, request ExcerptVerifyRequest) (VerifiedSnippet, bool, error) {
	resolver.calls++
	return VerifiedSnippet{Field: request.Field, Text: resolver.snippet}, resolver.match, resolver.err
}

func errorsIs(err, target error) bool {
	return err != nil && (err == target || strings.Contains(err.Error(), target.Error()))
}
