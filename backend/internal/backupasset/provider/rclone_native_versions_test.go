package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

func rcloneNativeVersionForTest(key, versionID string, kind RcloneNativeVersionKind, latest bool, size uint64, observed time.Time) RcloneNativeVersionRecord {
	return RcloneNativeVersionRecord{
		PhysicalKey: key, VersionID: versionID, Kind: kind, IsLatest: latest,
		Size: size, LastModified: observed,
	}
}

func TestRcloneNativeStableGraphRequiresTwoIdenticalCompleteObservations(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	records := []RcloneNativeVersionRecord{
		rcloneNativeVersionForTest("managed/v1/data/a.txt", "opaque-v1", RcloneNativeObjectVersion, true, 0, now),
		rcloneNativeVersionForTest("managed/v1/data/deleted.txt", "opaque-d1", RcloneNativeDeleteMarker, true, 0, now),
	}
	first := RcloneNativeFullObservation{Records: records, PageCount: 2, TerminalKeyMarker: "", TerminalVersionIDMarker: ""}
	second := RcloneNativeFullObservation{Records: append([]RcloneNativeVersionRecord(nil), records...), PageCount: 2}
	graph, err := NewRcloneNativeStableGraph(first, second)
	if err != nil || graph.Digest == "" || graph.RecordCount != 2 || graph.ObjectCount != 1 || graph.DeleteMarkerCount != 1 {
		t.Fatalf("graph=%+v err=%v", graph, err)
	}
	if graph.Records[0].Kind == graph.Records[1].Kind {
		t.Fatalf("zero-byte object collapsed with delete marker: %+v", graph.Records)
	}

	drift := second
	drift.Records = append(drift.Records, rcloneNativeVersionForTest("managed/v1/data/new.txt", "opaque-v2", RcloneNativeObjectVersion, true, 1, now))
	if _, err := NewRcloneNativeStableGraph(first, drift); rcloneNativeReason(err) != backupasset.RcloneReasonExternalWriterDetected {
		t.Fatalf("drift error=%v reason=%q", err, rcloneNativeReason(err))
	}
	duplicate := first
	duplicate.Records = append(duplicate.Records, duplicate.Records[0])
	if _, err := NewRcloneNativeStableGraph(duplicate, duplicate); rcloneNativeReason(err) != backupasset.RcloneReasonUnexpectedVersion {
		t.Fatalf("duplicate error=%v reason=%q", err, rcloneNativeReason(err))
	}
}

func TestRcloneNativeStableGraphRejectsIncompletePaginationAndLatestAmbiguity(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	first := rcloneNativeVersionForTest("managed/v1/data/a", "v1", RcloneNativeObjectVersion, true, 1, now)
	second := rcloneNativeVersionForTest("managed/v1/data/a", "v2", RcloneNativeObjectVersion, true, 2, now.Add(time.Second))
	for name, observation := range map[string]RcloneNativeFullObservation{
		"zero pages":         {Records: []RcloneNativeVersionRecord{first}},
		"key marker":         {Records: []RcloneNativeVersionRecord{first}, PageCount: 1, TerminalKeyMarker: "not-terminal"},
		"version marker":     {Records: []RcloneNativeVersionRecord{first}, PageCount: 1, TerminalVersionIDMarker: "not-terminal"},
		"multiple latest":    {Records: []RcloneNativeVersionRecord{first, second}, PageCount: 1},
		"no latest":          {Records: []RcloneNativeVersionRecord{{PhysicalKey: first.PhysicalKey, VersionID: first.VersionID, Kind: first.Kind, Size: first.Size, LastModified: first.LastModified}}, PageCount: 1},
		"duplicate identity": {Records: []RcloneNativeVersionRecord{first, {PhysicalKey: first.PhysicalKey, VersionID: first.VersionID, Kind: RcloneNativeDeleteMarker, IsLatest: false, LastModified: now}}, PageCount: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRcloneNativeStableGraph(observation, observation); rcloneNativeReason(err) != backupasset.RcloneReasonUnexpectedVersion {
				t.Fatalf("observation=%+v err=%v reason=%q", observation, err, rcloneNativeReason(err))
			}
		})
	}

	stable := RcloneNativeFullObservation{Records: []RcloneNativeVersionRecord{first}, PageCount: 1}
	differentPages := stable
	differentPages.PageCount = 2
	if _, err := NewRcloneNativeStableGraph(stable, differentPages); rcloneNativeReason(err) != backupasset.RcloneReasonExternalWriterDetected {
		t.Fatalf("page drift error=%v reason=%q", err, rcloneNativeReason(err))
	}
}

type rcloneNativeVersionEnumeratorFake struct {
	pages    []RcloneNativeVersionPage
	requests []RcloneNativeVersionPageRequest
}

type rcloneNativeBaselineS3Fake struct {
	*rcloneNativeVersionEnumeratorFake
	heads    map[string]RcloneNativeBaselineObjectHead
	payloads map[string][]byte
	headKeys []string
	openKeys []string
}

func (fake *rcloneNativeBaselineS3Fake) HeadBaselineVersion(_ context.Context, request RcloneNativeExactReadRequest) (RcloneNativeBaselineObjectHead, error) {
	key := request.PhysicalKey + "\x00" + request.VersionID
	fake.headKeys = append(fake.headKeys, key)
	head, ok := fake.heads[key]
	if !ok {
		return RcloneNativeBaselineObjectHead{}, errors.New("FAKE_BASELINE_HEAD_NOT_FOUND_FOR_TEST_ONLY")
	}
	return head, nil
}

func (fake *rcloneNativeBaselineS3Fake) OpenBaselineVersion(_ context.Context, request RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	key := request.PhysicalKey + "\x00" + request.VersionID
	fake.openKeys = append(fake.openKeys, key)
	payload, ok := fake.payloads[key]
	if !ok {
		return nil, errors.New("FAKE_BASELINE_BODY_NOT_FOUND_FOR_TEST_ONLY")
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func TestInspectRcloneNativeBaselineSourceRequiresStableExactEncryptionInventory(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	sourcePrefix := "legacy/current/"
	kmsKeyARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-SOURCE-KMS-FOR-TEST-ONLY"
	firstPayload := []byte("alpha")
	secondPayload := []byte("beta-config")
	records := []RcloneNativeVersionRecord{
		rcloneNativeVersionForTest(sourcePrefix+"a.txt", "opaque-a-v1", RcloneNativeObjectVersion, true, uint64(len(firstPayload)), now),
		rcloneNativeVersionForTest(sourcePrefix+"config.json", "opaque-c-v3", RcloneNativeObjectVersion, true, uint64(len(secondPayload)), now),
		rcloneNativeVersionForTest(sourcePrefix+"deleted.txt", "opaque-d-v2", RcloneNativeDeleteMarker, true, 0, now),
	}
	page := RcloneNativeVersionPage{Records: records}
	identity := func(record RcloneNativeVersionRecord) string { return record.PhysicalKey + "\x00" + record.VersionID }
	fake := &rcloneNativeBaselineS3Fake{
		rcloneNativeVersionEnumeratorFake: &rcloneNativeVersionEnumeratorFake{pages: []RcloneNativeVersionPage{page, page}},
		heads: map[string]RcloneNativeBaselineObjectHead{
			identity(records[0]): {
				PhysicalKey: records[0].PhysicalKey, VersionID: records[0].VersionID, Size: records[0].Size,
				EncryptionProfile: RcloneNativeSSES3V1,
			},
			identity(records[1]): {
				PhysicalKey: records[1].PhysicalKey, VersionID: records[1].VersionID, Size: records[1].Size,
				EncryptionProfile: RcloneNativeSSEKMSV1, KMSKeyARN: kmsKeyARN,
			},
		},
		payloads: map[string][]byte{identity(records[0]): firstPayload, identity(records[1]): secondPayload},
	}
	inventory, err := InspectRcloneNativeBaselineSource(context.Background(), fake, RcloneNativeBaselineInventoryRequest{
		SourcePrefix: sourcePrefix, ObservationLimits: RcloneNativeObservationLimits{PageSize: 100, MaxPages: 2, MaxRecords: 100},
		MaxReadBytes: 1024,
	})
	if err != nil || inventory.Digest == "" || inventory.ObjectCount != 2 ||
		inventory.LogicalBytes != uint64(len(firstPayload)+len(secondPayload)) ||
		!reflect.DeepEqual(inventory.SourceKMSKeyARNs, []string{kmsKeyARN}) || len(fake.headKeys) != 2 || len(fake.openKeys) != 2 {
		t.Fatalf("baseline inventory=%+v heads=%v opens=%v err=%v", inventory, fake.headKeys, fake.openKeys, err)
	}
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{sourcePrefix, kmsKeyARN, records[0].VersionID, records[1].VersionID} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("baseline inventory leaked %q: %s", private, encoded)
		}
	}
	budgetFake := *fake
	budgetFake.rcloneNativeVersionEnumeratorFake = &rcloneNativeVersionEnumeratorFake{pages: []RcloneNativeVersionPage{page, page}}
	budgetFake.headKeys = nil
	budgetFake.openKeys = nil
	if _, err := InspectRcloneNativeBaselineSource(context.Background(), &budgetFake, RcloneNativeBaselineInventoryRequest{
		SourcePrefix: sourcePrefix, ObservationLimits: RcloneNativeObservationLimits{PageSize: 100, MaxPages: 2, MaxRecords: 100},
		MaxReadBytes: uint64(len(firstPayload) + len(secondPayload) - 1),
	}); rcloneNativeReason(err) != backupasset.RcloneReasonVerificationCostLimit {
		t.Fatalf("baseline inventory budget error=%v reason=%q", err, rcloneNativeReason(err))
	}
}

func TestDiscoverRcloneNativeBaselineSourceFindsKMSKeysBeforeDecryptAdmission(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	sourcePrefix := "legacy/current/"
	kmsKeyARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-DISCOVERED-SOURCE-KMS-FOR-TEST-ONLY"
	record := rcloneNativeVersionForTest(sourcePrefix+"database.dump", "opaque-source-v1", RcloneNativeObjectVersion, true, 512, now)
	page := RcloneNativeVersionPage{Records: []RcloneNativeVersionRecord{record}}
	identity := record.PhysicalKey + "\x00" + record.VersionID
	fake := &rcloneNativeBaselineS3Fake{
		rcloneNativeVersionEnumeratorFake: &rcloneNativeVersionEnumeratorFake{pages: []RcloneNativeVersionPage{page, page}},
		heads: map[string]RcloneNativeBaselineObjectHead{identity: {
			PhysicalKey: record.PhysicalKey, VersionID: record.VersionID, Size: record.Size,
			EncryptionProfile: RcloneNativeSSEKMSV1, KMSKeyARN: kmsKeyARN,
		}},
		payloads: map[string][]byte{},
	}
	discovery, err := DiscoverRcloneNativeBaselineSource(context.Background(), fake, RcloneNativeBaselineInventoryRequest{
		SourcePrefix: sourcePrefix, ObservationLimits: RcloneNativeObservationLimits{PageSize: 100, MaxPages: 2, MaxRecords: 100},
		MaxReadBytes: 1024,
	})
	if err != nil || discovery.Digest == "" || discovery.ObjectCount != 1 || discovery.LogicalBytes != record.Size ||
		!reflect.DeepEqual(discovery.SourceKMSKeyARNs, []string{kmsKeyARN}) || len(fake.headKeys) != 1 || len(fake.openKeys) != 0 {
		t.Fatalf("baseline discovery=%+v heads=%v opens=%v err=%v", discovery, fake.headKeys, fake.openKeys, err)
	}
	payload, err := json.Marshal(discovery)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{sourcePrefix, kmsKeyARN, record.VersionID} {
		if strings.Contains(string(payload), private) {
			t.Fatalf("baseline discovery leaked %q: %s", private, payload)
		}
	}
}

func TestResolveRcloneNativeBaselineSourceRequiresExactBoundBucketPrefix(t *testing.T) {
	profile := validRcloneNativeProfileForTest()
	legacyLocator := "node_legacy:xirang-native-test/legacy/current/"
	source, err := ResolveRcloneNativeBaselineSource(legacyLocator, profile)
	if err != nil || source.SourcePrefix != "legacy/current/" ||
		source.PublicationSource.value != "xirang_native:xirang-native-test/legacy/current/" {
		t.Fatalf("native baseline source=%+v err=%v", source, err)
	}
	payload, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{legacyLocator, source.SourcePrefix, source.PublicationSource.value} {
		if strings.Contains(string(payload), private) {
			t.Fatalf("native baseline source leaked %q: %s", private, payload)
		}
	}

	for _, invalid := range []string{
		"/srv/local",
		"node_legacy:xirang-native-test",
		"node_legacy:xirang-native-test/",
		"node_legacy:another-bucket/legacy/current/",
		"node_legacy:xirang-native-test/legacy/current",
		"node_legacy:xirang-native-test//legacy/current/",
		"node_legacy:xirang-native-test/legacy/../current/",
		"bad remote:xirang-native-test/legacy/current/",
		"node_legacy:xirang-native-test/managed/",
		"node_legacy:xirang-native-test/managed/v1/",
		"node_legacy:xirang-native-test/managed/v1/nested/",
	} {
		if _, err := ResolveRcloneNativeBaselineSource(invalid, profile); rcloneNativeReason(err) != backupasset.RcloneReasonIdentityMismatch {
			t.Fatalf("unsafe native baseline locator %q error=%v reason=%q", invalid, err, rcloneNativeReason(err))
		}
	}
}

func (fake *rcloneNativeVersionEnumeratorFake) ListVersionPage(_ context.Context, request RcloneNativeVersionPageRequest) (RcloneNativeVersionPage, error) {
	fake.requests = append(fake.requests, request)
	if len(fake.pages) == 0 {
		return RcloneNativeVersionPage{}, errors.New("FAKE_UNEXPECTED_PAGE_REQUEST_FOR_TEST_ONLY")
	}
	page := fake.pages[0]
	fake.pages = fake.pages[1:]
	return page, nil
}

func TestRcloneNativeVersionPaginatorRequiresBothMarkersAndTwoStableFullPasses(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	latest := rcloneNativeVersionForTest("managed/v1/data/a", "v2", RcloneNativeObjectVersion, true, 2, now)
	old := rcloneNativeVersionForTest("managed/v1/data/a", "v1", RcloneNativeObjectVersion, false, 1, now.Add(-time.Second))
	deleted := rcloneNativeVersionForTest("managed/v1/data/b", "d1", RcloneNativeDeleteMarker, true, 0, now)
	firstPage := RcloneNativeVersionPage{
		Records: []RcloneNativeVersionRecord{latest, old}, Truncated: true,
		NextKeyMarker: "managed/v1/data/a", NextVersionIDMarker: "v1",
	}
	finalPage := RcloneNativeVersionPage{Records: []RcloneNativeVersionRecord{deleted}}
	fake := &rcloneNativeVersionEnumeratorFake{pages: []RcloneNativeVersionPage{firstPage, finalPage, firstPage, finalPage}}
	graph, err := CaptureRcloneNativeStableGraph(context.Background(), fake, "managed/v1/", RcloneNativeObservationLimits{PageSize: 2, MaxPages: 4, MaxRecords: 8})
	if err != nil || graph.RecordCount != 3 || graph.PageCount != 2 || len(fake.requests) != 4 {
		t.Fatalf("graph=%+v requests=%+v err=%v", graph, fake.requests, err)
	}
	for _, index := range []int{1, 3} {
		request := fake.requests[index]
		if request.KeyMarker != firstPage.NextKeyMarker || request.VersionIDMarker != firstPage.NextVersionIDMarker {
			t.Fatalf("second page request[%d]=%+v", index, request)
		}
	}
	if fake.requests[0].KeyMarker != "" || fake.requests[0].VersionIDMarker != "" || fake.requests[2].KeyMarker != "" || fake.requests[2].VersionIDMarker != "" {
		t.Fatalf("full traversal did not restart from empty markers: %+v", fake.requests)
	}

	for name, first := range map[string]RcloneNativeVersionPage{
		"missing key marker":     {Records: []RcloneNativeVersionRecord{latest}, Truncated: true, NextVersionIDMarker: "v2"},
		"missing version marker": {Records: []RcloneNativeVersionRecord{latest}, Truncated: true, NextKeyMarker: latest.PhysicalKey},
		"empty truncated page":   {Truncated: true, NextKeyMarker: latest.PhysicalKey, NextVersionIDMarker: latest.VersionID},
	} {
		t.Run(name, func(t *testing.T) {
			reader := &rcloneNativeVersionEnumeratorFake{pages: []RcloneNativeVersionPage{first}}
			if _, err := ObserveRcloneNativeFullVersions(context.Background(), reader, "managed/v1/", RcloneNativeObservationLimits{PageSize: 2, MaxPages: 2, MaxRecords: 4}); rcloneNativeReason(err) != backupasset.RcloneReasonUnexpectedVersion {
				t.Fatalf("page=%+v err=%v reason=%q", first, err, rcloneNativeReason(err))
			}
		})
	}

	overflow := &rcloneNativeVersionEnumeratorFake{pages: []RcloneNativeVersionPage{firstPage}}
	if _, err := ObserveRcloneNativeFullVersions(context.Background(), overflow, "managed/v1/", RcloneNativeObservationLimits{PageSize: 2, MaxPages: 1, MaxRecords: 8}); rcloneNativeReason(err) != backupasset.RcloneReasonProviderResourceLimit {
		t.Fatalf("page overflow error=%v reason=%q", err, rcloneNativeReason(err))
	}
}

func TestRcloneNativePointGraphBuildsExactViewAndCompleteMutationLedger(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	b0Records := []RcloneNativeVersionRecord{
		rcloneNativeVersionForTest("managed/v1/data/a.txt", "opaque-v1", RcloneNativeObjectVersion, true, 1, now.Add(-time.Minute)),
		rcloneNativeVersionForTest("managed/v1/data/remove.txt", "opaque-r1", RcloneNativeObjectVersion, true, 2, now.Add(-time.Minute)),
	}
	b1Records := []RcloneNativeVersionRecord{
		rcloneNativeVersionForTest("managed/v1/data/a.txt", "opaque-v3", RcloneNativeObjectVersion, true, 3, now.Add(time.Second)),
		rcloneNativeVersionForTest("managed/v1/data/a.txt", "opaque-v2", RcloneNativeObjectVersion, false, 2, now),
		rcloneNativeVersionForTest("managed/v1/data/a.txt", "opaque-v1", RcloneNativeObjectVersion, false, 1, now.Add(-time.Minute)),
		rcloneNativeVersionForTest("managed/v1/data/remove.txt", "opaque-d2", RcloneNativeDeleteMarker, true, 0, now.Add(time.Second)),
		rcloneNativeVersionForTest("managed/v1/data/remove.txt", "opaque-r1", RcloneNativeObjectVersion, false, 2, now.Add(-time.Minute)),
	}
	b0, err := NewRcloneNativeStableGraph(RcloneNativeFullObservation{Records: b0Records, PageCount: 1}, RcloneNativeFullObservation{Records: b0Records, PageCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	b1, err := NewRcloneNativeStableGraph(RcloneNativeFullObservation{Records: b1Records, PageCount: 2}, RcloneNativeFullObservation{Records: b1Records, PageCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	owned := []RcloneNativeOwnedMutation{
		{PhysicalKey: "managed/v1/data/a.txt", VersionID: "opaque-v2", Kind: RcloneNativeObjectVersion},
		{PhysicalKey: "managed/v1/data/a.txt", VersionID: "opaque-v3", Kind: RcloneNativeObjectVersion},
		{PhysicalKey: "managed/v1/data/remove.txt", VersionID: "opaque-d2", Kind: RcloneNativeDeleteMarker},
	}
	point, err := BuildRcloneNativePointGraph(b0, b1, "managed/v1/data/", owned)
	if err != nil || len(point.View) != 2 || len(point.Ledger) != 3 || point.ViewDigest == "" || point.LedgerDigest == "" {
		t.Fatalf("point=%+v err=%v", point, err)
	}
	if point.View[0].LogicalPath != "a.txt" || point.View[0].VersionID != "opaque-v3" || point.View[0].Kind != RcloneNativeObjectVersion {
		t.Fatalf("object view=%+v", point.View[0])
	}
	if point.View[1].LogicalPath != "remove.txt" || point.View[1].Kind != RcloneNativeDeleteMarker || point.View[1].VersionID != "opaque-d2" {
		t.Fatalf("delete view=%+v", point.View[1])
	}
	dispositions := map[string]RcloneNativeMutationDisposition{}
	for _, entry := range point.Ledger {
		dispositions[entry.VersionID] = entry.Disposition
	}
	if dispositions["opaque-v2"] != RcloneNativeMutationSuperseded || dispositions["opaque-v3"] != RcloneNativeMutationReferenced || dispositions["opaque-d2"] != RcloneNativeMutationReferenced {
		t.Fatalf("ledger dispositions=%+v", dispositions)
	}
}

func TestRcloneNativePointGraphRejectsPermanentRemovalAndUnownedVersion(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	baseRecord := rcloneNativeVersionForTest("managed/v1/data/a", "v1", RcloneNativeObjectVersion, true, 1, now)
	b0, err := NewRcloneNativeStableGraph(RcloneNativeFullObservation{Records: []RcloneNativeVersionRecord{baseRecord}, PageCount: 1}, RcloneNativeFullObservation{Records: []RcloneNativeVersionRecord{baseRecord}, PageCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := NewRcloneNativeStableGraph(RcloneNativeFullObservation{PageCount: 1}, RcloneNativeFullObservation{PageCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRcloneNativePointGraph(b0, empty, "managed/v1/data/", nil); rcloneNativeReason(err) != backupasset.RcloneReasonUnexpectedVersion {
		t.Fatalf("permanent removal error=%v reason=%q", err, rcloneNativeReason(err))
	}

	newRecord := rcloneNativeVersionForTest("managed/v1/data/b", "v2", RcloneNativeObjectVersion, true, 1, now.Add(time.Second))
	b1Records := []RcloneNativeVersionRecord{baseRecord, newRecord}
	b1, err := NewRcloneNativeStableGraph(RcloneNativeFullObservation{Records: b1Records, PageCount: 1}, RcloneNativeFullObservation{Records: b1Records, PageCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRcloneNativePointGraph(b0, b1, "managed/v1/data/", nil); rcloneNativeReason(err) != backupasset.RcloneReasonExternalWriterDetected {
		t.Fatalf("unowned version error=%v reason=%q", err, rcloneNativeReason(err))
	}
}

type rcloneNativeExactReaderFake struct {
	head     RcloneNativeExactObjectHead
	payload  []byte
	requests []RcloneNativeExactReadRequest
}

func (fake *rcloneNativeExactReaderFake) HeadVersion(_ context.Context, request RcloneNativeExactReadRequest) (RcloneNativeExactObjectHead, error) {
	fake.requests = append(fake.requests, request)
	return fake.head, nil
}

func (fake *rcloneNativeExactReaderFake) OpenVersion(_ context.Context, request RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	fake.requests = append(fake.requests, request)
	return io.NopCloser(bytes.NewReader(fake.payload)), nil
}

func TestRcloneNativeExactProofRequiresVersionIDBytesAndEncryptionIdentity(t *testing.T) {
	payload := []byte("exact native bytes")
	digest := sha256Hex(payload)
	entry := RcloneNativePointViewEntry{
		LogicalPath: "a.txt", PhysicalKey: "managed/v1/data/a.txt", VersionID: "opaque-v1",
		Kind: RcloneNativeObjectVersion, Size: uint64(len(payload)), ContentDigest: digest,
		EncryptionProfile: RcloneNativeSSEKMSV1, KMSKeyDigest: strings.Repeat("a", 64), BucketKeyEnabled: true,
	}
	reader := &rcloneNativeExactReaderFake{
		head: RcloneNativeExactObjectHead{
			PhysicalKey: entry.PhysicalKey, VersionID: entry.VersionID, Size: entry.Size,
			EncryptionProfile: entry.EncryptionProfile, KMSKeyDigest: entry.KMSKeyDigest, BucketKeyEnabled: true,
		},
		payload: payload,
	}
	proof, err := VerifyRcloneNativeExactObject(context.Background(), reader, entry, uint64(len(payload)))
	if err != nil || proof.Digest == "" || proof.VerifiedBytes != uint64(len(payload)) || len(reader.requests) != 2 {
		t.Fatalf("proof=%+v requests=%+v err=%v", proof, reader.requests, err)
	}
	for _, request := range reader.requests {
		if request.VersionID != entry.VersionID || request.PhysicalKey != entry.PhysicalKey {
			t.Fatalf("non-exact request=%+v", request)
		}
	}

	for _, mutate := range []func(*RcloneNativePointViewEntry, *rcloneNativeExactReaderFake){
		func(value *RcloneNativePointViewEntry, _ *rcloneNativeExactReaderFake) { value.VersionID = "" },
		func(_ *RcloneNativePointViewEntry, value *rcloneNativeExactReaderFake) {
			value.head.VersionID = "current"
		},
		func(_ *RcloneNativePointViewEntry, value *rcloneNativeExactReaderFake) {
			value.head.KMSKeyDigest = strings.Repeat("b", 64)
		},
		func(_ *RcloneNativePointViewEntry, value *rcloneNativeExactReaderFake) {
			value.payload = value.payload[:len(value.payload)-1]
		},
	} {
		candidate := entry
		candidateReader := *reader
		candidateReader.requests = nil
		mutate(&candidate, &candidateReader)
		if _, err := VerifyRcloneNativeExactObject(context.Background(), &candidateReader, candidate, uint64(len(payload))); err == nil {
			t.Fatal("invalid exact proof unexpectedly succeeded")
		}
	}
}

type rcloneNativeExactRangeReaderFake struct {
	head     RcloneNativeExactObjectHead
	payload  []byte
	requests []RcloneNativeExactRangeRequest
}

func (fake *rcloneNativeExactRangeReaderFake) HeadVersion(_ context.Context, request RcloneNativeExactReadRequest) (RcloneNativeExactObjectHead, error) {
	fake.requests = append(fake.requests, RcloneNativeExactRangeRequest{PhysicalKey: request.PhysicalKey, VersionID: request.VersionID})
	return fake.head, nil
}

func (fake *rcloneNativeExactRangeReaderFake) OpenVersionRange(_ context.Context, request RcloneNativeExactRangeRequest) (io.ReadCloser, error) {
	fake.requests = append(fake.requests, request)
	return io.NopCloser(bytes.NewReader(fake.payload)), nil
}

func TestRcloneNativeExactRangeRequiresVersionIDBoundsAndExpectedBytes(t *testing.T) {
	object := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	entry := RcloneNativePointViewEntry{
		LogicalPath: "a.txt", PhysicalKey: "managed/v1/data/a.txt", VersionID: "opaque-v1",
		Kind: RcloneNativeObjectVersion, Size: uint64(len(object)), ContentDigest: sha256Hex(object),
		EncryptionProfile: RcloneNativeSSES3V1,
	}
	offset, length := uint64(10), uint64(8)
	rangeBytes := object[offset : offset+length]
	reader := &rcloneNativeExactRangeReaderFake{
		head: RcloneNativeExactObjectHead{
			PhysicalKey: entry.PhysicalKey, VersionID: entry.VersionID, Size: entry.Size,
			EncryptionProfile: entry.EncryptionProfile,
		},
		payload: rangeBytes,
	}
	proof, err := VerifyRcloneNativeExactRange(context.Background(), reader, entry, offset, length, sha256Hex(rangeBytes), length)
	if err != nil || proof.Digest == "" || proof.VerifiedBytes != length || proof.Offset != offset || len(reader.requests) != 2 {
		t.Fatalf("proof=%+v requests=%+v err=%v", proof, reader.requests, err)
	}
	request := reader.requests[1]
	if request.PhysicalKey != entry.PhysicalKey || request.VersionID != entry.VersionID || request.Offset != offset || request.Length != length {
		t.Fatalf("non-exact range request=%+v", request)
	}

	for name, mutate := range map[string]func(*rcloneNativeExactRangeReaderFake) (uint64, uint64, string, uint64){
		"short read": func(value *rcloneNativeExactRangeReaderFake) (uint64, uint64, string, uint64) {
			value.payload = value.payload[:len(value.payload)-1]
			return offset, length, sha256Hex(rangeBytes), length
		},
		"extra byte": func(value *rcloneNativeExactRangeReaderFake) (uint64, uint64, string, uint64) {
			value.payload = append(append([]byte(nil), value.payload...), 'x')
			return offset, length, sha256Hex(rangeBytes), length
		},
		"wrong digest": func(_ *rcloneNativeExactRangeReaderFake) (uint64, uint64, string, uint64) {
			return offset, length, strings.Repeat("f", 64), length
		},
		"out of bounds": func(_ *rcloneNativeExactRangeReaderFake) (uint64, uint64, string, uint64) {
			return entry.Size - 1, 2, sha256Hex(rangeBytes), length
		},
		"budget": func(_ *rcloneNativeExactRangeReaderFake) (uint64, uint64, string, uint64) {
			return offset, length, sha256Hex(rangeBytes), length - 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *reader
			candidate.payload = append([]byte(nil), reader.payload...)
			candidate.requests = nil
			candidateOffset, candidateLength, digest, budget := mutate(&candidate)
			if _, err := VerifyRcloneNativeExactRange(context.Background(), &candidate, entry, candidateOffset, candidateLength, digest, budget); err == nil {
				t.Fatal("invalid exact range unexpectedly succeeded")
			}
		})
	}
}

type rcloneNativeControlStoreFake struct {
	writes   []RcloneNativeControlWriteRequest
	versions map[string]RcloneNativeControlObjectVersion
	payloads map[string][]byte
}

type rcloneNativePublisherS3Fake struct {
	profile RcloneNativeProfile
	now     time.Time
	records []RcloneNativeVersionRecord
	bodies  map[string][]byte
	writes  []string
	events  *[]string
}

func newRcloneNativePublisherS3Fake(profile RcloneNativeProfile, now time.Time, events *[]string) *rcloneNativePublisherS3Fake {
	return &rcloneNativePublisherS3Fake{
		profile: profile,
		now:     now,
		bodies:  make(map[string][]byte),
		events:  events,
	}
}

func (fake *rcloneNativePublisherS3Fake) BucketIdentity(context.Context, RcloneNativeProfile) (RcloneNativeBucketIdentity, error) {
	return RcloneNativeBucketIdentity{AccountID: "123456789012", Region: fake.profile.Region, Kind: RcloneNativeBucketGeneralPurpose}, nil
}

func (*rcloneNativePublisherS3Fake) GetVersioning(context.Context, RcloneNativeProfile) (RcloneNativeVersioningObservation, error) {
	return RcloneNativeVersioningObservation{Status: "Enabled", MFADelete: "Disabled"}, nil
}

func (*rcloneNativePublisherS3Fake) GetLifecycle(context.Context, RcloneNativeProfile) (RcloneNativeLifecycleObservation, error) {
	return RcloneNativeLifecycleObservation{}, nil
}

func (*rcloneNativePublisherS3Fake) GetEncryption(context.Context, RcloneNativeProfile) (RcloneNativeBucketEncryption, error) {
	return RcloneNativeBucketEncryption{Algorithm: "AES256", BlockedEncryptionTypesKnown: true}, nil
}

func (fake *rcloneNativePublisherS3Fake) ListVersionPage(_ context.Context, request RcloneNativeVersionPageRequest) (RcloneNativeVersionPage, error) {
	*fake.events = append(*fake.events, "list")
	if request.KeyMarker != "" || request.VersionIDMarker != "" {
		return RcloneNativeVersionPage{}, errors.New("FAKE_UNEXPECTED_NATIVE_MARKER_FOR_TEST_ONLY")
	}
	records := make([]RcloneNativeVersionRecord, 0, len(fake.records))
	for _, record := range fake.records {
		if strings.HasPrefix(record.PhysicalKey, request.Prefix) {
			records = append(records, record)
		}
	}
	return RcloneNativeVersionPage{Records: records}, nil
}

func (fake *rcloneNativePublisherS3Fake) PutControlVersion(_ context.Context, request RcloneNativeControlWriteRequest) (RcloneNativeControlWriteResult, error) {
	versionID := fmt.Sprintf("opaque-control-v%d", len(fake.writes)+1)
	fake.writes = append(fake.writes, request.PhysicalKey)
	*fake.events = append(*fake.events, "put:"+path.Base(request.PhysicalKey))
	fake.addObject(request.PhysicalKey, versionID, request.Payload, request.EncryptionProfile, request.KMSKeyDigest, request.BucketKeyEnabled)
	return RcloneNativeControlWriteResult{VersionID: versionID}, nil
}

func (fake *rcloneNativePublisherS3Fake) HeadVersion(_ context.Context, request RcloneNativeExactReadRequest) (RcloneNativeExactObjectHead, error) {
	*fake.events = append(*fake.events, "head")
	for _, record := range fake.records {
		if record.PhysicalKey == request.PhysicalKey && record.VersionID == request.VersionID && record.Kind == RcloneNativeObjectVersion {
			return RcloneNativeExactObjectHead{
				PhysicalKey: record.PhysicalKey, VersionID: record.VersionID, Size: record.Size,
				EncryptionProfile: record.EncryptionProfile, KMSKeyDigest: record.KMSKeyDigest,
				BucketKeyEnabled: record.BucketKeyEnabled,
			}, nil
		}
	}
	return RcloneNativeExactObjectHead{}, errors.New("FAKE_NATIVE_HEAD_NOT_FOUND_FOR_TEST_ONLY")
}

func (fake *rcloneNativePublisherS3Fake) OpenVersion(_ context.Context, request RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	*fake.events = append(*fake.events, "get")
	payload, exists := fake.bodies[rcloneNativeVersionIdentity(request.PhysicalKey, request.VersionID)]
	if !exists {
		return nil, errors.New("FAKE_NATIVE_BODY_NOT_FOUND_FOR_TEST_ONLY")
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func (fake *rcloneNativePublisherS3Fake) OpenVersionRange(_ context.Context, request RcloneNativeExactRangeRequest) (io.ReadCloser, error) {
	payload, exists := fake.bodies[rcloneNativeVersionIdentity(request.PhysicalKey, request.VersionID)]
	if !exists || request.Offset > uint64(len(payload)) || request.Length > uint64(len(payload))-request.Offset {
		return nil, errors.New("FAKE_NATIVE_RANGE_NOT_FOUND_FOR_TEST_ONLY")
	}
	return io.NopCloser(bytes.NewReader(payload[request.Offset : request.Offset+request.Length])), nil
}

func (fake *rcloneNativePublisherS3Fake) addObject(key, versionID string, payload []byte, encryption RcloneNativeEncryptionProfileCode, keyDigest string, bucketKey bool) {
	for index := range fake.records {
		if fake.records[index].PhysicalKey == key && fake.records[index].IsLatest {
			fake.records[index].IsLatest = false
		}
	}
	fake.now = fake.now.Add(time.Second)
	record := RcloneNativeVersionRecord{
		PhysicalKey: key, VersionID: versionID, Kind: RcloneNativeObjectVersion, IsLatest: true,
		Size: uint64(len(payload)), LastModified: fake.now, ContentDigest: sha256Hex(payload),
		EncryptionProfile: encryption, KMSKeyDigest: keyDigest, BucketKeyEnabled: bucketKey,
	}
	fake.records = append(fake.records, record)
	fake.bodies[rcloneNativeVersionIdentity(key, versionID)] = append([]byte(nil), payload...)
}

type rcloneNativeDataPlaneFake struct {
	observations []RcloneManifestBundle
	s3           *rcloneNativePublisherS3Fake
	payload      []byte
	events       *[]string
	verified     uint64
}

type rcloneNativeClientFactoryFake struct {
	s3 S3Native
}

func (fake rcloneNativeClientFactoryFake) S3(RcloneNativeSession, RcloneNativeProfile, []RcloneNativeKMSKeyDigestBinding) (S3Native, error) {
	return fake.s3, nil
}

func (rcloneNativeClientFactoryFake) KMS(RcloneNativeSession, string) (KMSKeyInspector, error) {
	return nil, errors.New("FAKE_UNEXPECTED_KMS_FACTORY_CALL_FOR_TEST_ONLY")
}

func TestRcloneNativePublicationRequestRejectsKMSBindingDriftFromAttempt(t *testing.T) {
	now := time.Date(2026, 7, 16, 1, 30, 0, 0, time.UTC)
	profile := validRcloneNativeProfileForTest()
	activeARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-ACTIVE-NATIVE-PUBLISH-KEY-FOR-TEST-ONLY"
	retainedARN := "arn:aws:kms:us-east-1:123456789012:key/FAKE-RETAINED-NATIVE-PUBLISH-KEY-FOR-TEST-ONLY"
	activeDigest := strings.Repeat("a", 64)
	retainedDigest := strings.Repeat("b", 64)
	readSetDigest, err := canonicalRcloneNativeDigest("kms-read-key-set-v1", []string{retainedDigest})
	if err != nil {
		t.Fatal(err)
	}
	selection := RcloneNativeEncryptionSelection{
		Profile: RcloneNativeSSEKMSV1, ActiveKeyARN: activeARN, RetainedReadKeyARNs: []string{retainedARN},
	}
	evidence := RcloneNativeEncryptionEvidence{
		Profile: RcloneNativeSSEKMSV1, ActiveKeyDigest: activeDigest,
		ReadKeySetDigest: readSetDigest, RetainedReadKeyCount: 1,
	}
	attempt := validRcloneAttemptForTest(backupasset.PublicationNativeObjectVersions)
	attempt.Native.EncryptionProfile = RcloneNativeSSEKMSV1
	attempt.Native.ActiveKeyDigest = activeDigest
	attempt.Native.RetainedReadKeySetDigest = readSetDigest
	attempt.Native.KMSCapabilityRevision = 1
	session := newRcloneNativeSession(
		"FAKE_KMS_ACCESS_KEY_ID_FOR_TEST_ONLY", "FAKE_KMS_SECRET_ACCESS_KEY_FOR_TEST_ONLY", "FAKE_KMS_SESSION_TOKEN_FOR_TEST_ONLY",
		"123456789012", attempt.Native.RoleSessionIdentityDigest, attempt.Native.SessionExpiresAt,
	)
	config, err := BuildRcloneNativeRcloneConfig(profile, selection, session)
	if err != nil {
		t.Fatal(err)
	}
	attempt.ConfigDigest = sha256Hex(config)
	request := RcloneNativePublicationRequest{
		Attempt: attempt, Profile: profile, Session: session, ClientFactory: rcloneNativeClientFactoryFake{},
		Source: mustRclonePrivateLocatorForTest(t, "/srv/source"), RcloneConfig: config,
		Runtime: RemoteCommandAccess{Node: model.Node{ID: 9}},
		Manifest: RcloneManifestBundle{
			Version: 1, IndexDigest: strings.Repeat("c", 64), ObservationDigest: strings.Repeat("d", 64),
		},
		ObservationLimits: RcloneNativeObservationLimits{PageSize: 1000, MaxPages: 4, MaxRecords: 100},
		Encryption:        selection, EncryptionEvidence: evidence,
		KMSKeyBindings: []RcloneNativeKMSKeyDigestBinding{
			{KeyARN: activeARN, Digest: activeDigest}, {KeyARN: retainedARN, Digest: retainedDigest},
		},
		MarkerKey:                []byte("FAKE_NATIVE_MARKER_AUTH_KEY_32_BYTES_FOR_TEST_ONLY"),
		CapabilityEvidenceDigest: strings.Repeat("e", 64), CostEvidenceDigest: strings.Repeat("f", 64),
		MaxVerifyBytes: 1 << 20, ControlPayloadMaxBytes: 1 << 20, LowLevelRetries: 3,
	}
	if err := request.validate(now); err != nil {
		t.Fatalf("valid KMS publication request rejected: %v", err)
	}

	for name, mutate := range map[string]func(*RcloneNativePublicationRequest){
		"active digest": func(value *RcloneNativePublicationRequest) {
			value.Attempt.Native.ActiveKeyDigest = strings.Repeat("1", 64)
		},
		"retained set digest": func(value *RcloneNativePublicationRequest) {
			value.Attempt.Native.RetainedReadKeySetDigest = strings.Repeat("2", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			drifted := request
			attemptCopy := *request.Attempt.Native
			drifted.Attempt.Native = &attemptCopy
			mutate(&drifted)
			if err := drifted.validate(now); rcloneNativeReason(err) != backupasset.RcloneReasonEncryptionUnsupported {
				t.Fatalf("KMS attempt drift accepted: err=%v reason=%q", err, rcloneNativeReason(err))
			}
		})
	}
}

func (fake *rcloneNativeDataPlaneFake) ObserveSource(context.Context, RcloneNativePublicationRequest) (RcloneManifestBundle, error) {
	*fake.events = append(*fake.events, "source")
	if len(fake.observations) == 0 {
		return RcloneManifestBundle{}, errors.New("FAKE_NATIVE_SOURCE_OBSERVATION_MISSING_FOR_TEST_ONLY")
	}
	observation := fake.observations[0]
	fake.observations = fake.observations[1:]
	return observation, nil
}

func (fake *rcloneNativeDataPlaneFake) Sync(_ context.Context, request RcloneNativePublicationRequest) error {
	*fake.events = append(*fake.events, "sync")
	physical, err := EncodeRcloneV1744S3Path("a.txt")
	if err != nil {
		return err
	}
	fake.s3.addObject(request.Profile.ManagedPrefix+"data/"+physical, "opaque-data-v1", fake.payload, request.Encryption.Profile, "", false)
	return nil
}

func (fake *rcloneNativeDataPlaneFake) VerifyFullBytes(_ context.Context, _ RcloneNativePublicationRequest, expected uint64) (RcloneFullByteProof, error) {
	*fake.events = append(*fake.events, "check")
	fake.verified = expected
	return RcloneFullByteProof{VerifiedBytes: expected, Complete: true}, nil
}

func TestRcloneNativePublisherCapturesExactGraphAndCommitsLast(t *testing.T) {
	now := time.Date(2026, 7, 16, 1, 30, 0, 0, time.UTC)
	profile := validRcloneNativeProfileForTest()
	payload := []byte("hello")
	manifestJSON := fmt.Sprintf(`[{"Path":"a.txt","Name":"a.txt","Size":%d,"ModTime":"2026-07-16T01:00:01Z","IsDir":false,"Hashes":{"sha256":"%s"},"Metadata":{"mode":"100640","uid":"1000","gid":"1000","mtime":"2026-07-16T01:00:01Z"}}]`, len(payload), sha256Hex(payload))
	manifest, err := BuildRcloneManifestV1(context.Background(), strings.NewReader(manifestJSON), rcloneManifestOptionsForTest())
	if err != nil {
		t.Fatal(err)
	}
	b0, err := NewRcloneNativeStableGraph(
		RcloneNativeFullObservation{PageCount: 1},
		RcloneNativeFullObservation{PageCount: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	attempt := validRcloneAttemptForTest(backupasset.PublicationNativeObjectVersions)
	attempt.Native.B0VersionGraphDigest = b0.Digest
	session := newRcloneNativeSession(
		"FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY", "FAKE_AWS_SECRET_ACCESS_KEY_FOR_TEST_ONLY", "FAKE_AWS_SESSION_TOKEN_FOR_TEST_ONLY",
		"123456789012", attempt.Native.RoleSessionIdentityDigest, attempt.Native.SessionExpiresAt,
	)
	config, err := BuildRcloneNativeRcloneConfig(profile, RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1}, session)
	if err != nil {
		t.Fatal(err)
	}
	attempt.ConfigDigest = sha256Hex(config)
	events := make([]string, 0)
	s3 := newRcloneNativePublisherS3Fake(profile, now, &events)
	dataPlane := &rcloneNativeDataPlaneFake{
		observations: []RcloneManifestBundle{manifest, manifest},
		s3:           s3, payload: payload, events: &events,
	}
	publisher := NewRcloneNativePublisher(dataPlane, func() time.Time { return now })
	request := RcloneNativePublicationRequest{
		Attempt: attempt, Profile: profile, Session: session, ClientFactory: rcloneNativeClientFactoryFake{s3: s3},
		Source:       mustRclonePrivateLocatorForTest(t, "/srv/source"),
		RcloneConfig: config, Runtime: RemoteCommandAccess{Node: model.Node{ID: 9}},
		Manifest: manifest, ManifestOptions: rcloneManifestOptionsForTest(),
		ObservationLimits:        RcloneNativeObservationLimits{PageSize: 1000, MaxPages: 4, MaxRecords: 100},
		Encryption:               RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1},
		EncryptionEvidence:       RcloneNativeEncryptionEvidence{Profile: RcloneNativeSSES3V1},
		MarkerKey:                []byte("FAKE_NATIVE_MARKER_AUTH_KEY_32_BYTES_FOR_TEST_ONLY"),
		CapabilityEvidenceDigest: strings.Repeat("a", 64), CostEvidenceDigest: strings.Repeat("b", 64),
		MaxVerifyBytes: 1 << 20, ControlPayloadMaxBytes: 1 << 20, LowLevelRetries: 3,
	}
	commit, err := publisher.Publish(context.Background(), request)
	if err != nil {
		t.Fatalf("publish native point: %v", err)
	}
	if err := commit.Validate(); err != nil || commit.Native == nil || commit.Native.CommitVersionID == "" ||
		commit.Native.B0VersionGraphDigest != b0.Digest || commit.Native.B1VersionGraphDigest == "" ||
		commit.Native.PointViewDigest == "" || commit.Native.MutationLedgerDigest == "" || commit.Native.ExactReadProofDigest == "" {
		t.Fatalf("native commit=%+v err=%v", commit, err)
	}
	if got := path.Base(s3.writes[len(s3.writes)-1]); got != "commit.json" {
		t.Fatalf("last mutation=%q writes=%v", got, s3.writes)
	}
	var nativeManifest bytes.Buffer
	for _, key := range s3.writes {
		name := path.Base(key)
		if !strings.HasPrefix(name, "manifest-") || name == "manifest-index.json" {
			continue
		}
		for _, record := range s3.records {
			if record.PhysicalKey == key && record.IsLatest {
				nativeManifest.Write(s3.bodies[rcloneNativeVersionIdentity(key, record.VersionID)])
			}
		}
	}
	manifestText := nativeManifest.String()
	for _, required := range []string{
		`"record_kind":"entry"`, `"version_id":"opaque-data-v1"`,
		`"record_kind":"mutation"`, `"disposition":"referenced"`,
	} {
		if !strings.Contains(manifestText, required) {
			t.Fatalf("native manifest omitted %s: %s", required, manifestText)
		}
	}
	versionForKey := func(want string) string {
		for _, record := range s3.records {
			if path.Base(record.PhysicalKey) == want && record.IsLatest {
				return record.VersionID
			}
		}
		return ""
	}
	commitKey := s3.writes[len(s3.writes)-1]
	commitVersion := versionForKey("commit.json")
	commitPayload := string(s3.bodies[rcloneNativeVersionIdentity(commitKey, commitVersion)])
	for _, requiredVersion := range []string{versionForKey("manifest-000000.jsonl"), versionForKey("manifest-index.json")} {
		if requiredVersion == "" || !strings.Contains(commitPayload, requiredVersion) {
			t.Fatalf("commit marker omitted exact control version %q: %s", requiredVersion, commitPayload)
		}
	}
	if dataPlane.verified != 0 {
		t.Fatalf("strong-hash source unexpectedly used full download: %d", dataPlane.verified)
	}
	wantOrder := []string{"source", "list", "list", "put:start.json", "head", "get", "sync", "source", "put:end.json", "head", "get", "list", "list"}
	if len(events) < len(wantOrder) || !reflect.DeepEqual(events[:len(wantOrder)], wantOrder) {
		t.Fatalf("event prefix=%v want=%v", events, wantOrder)
	}
	reconciled, err := publisher.Reconcile(context.Background(), request)
	if err != nil || !reflect.DeepEqual(reconciled, commit) {
		t.Fatalf("reconciled=%+v err=%v want=%+v", reconciled, err, commit)
	}
	lateNow := attempt.Native.SessionExpiresAt.Add(time.Minute)
	readSession := newRcloneNativeSession(
		"FAKE_FRESH_ACCESS_KEY_ID_FOR_TEST_ONLY", "FAKE_FRESH_SECRET_ACCESS_KEY_FOR_TEST_ONLY", "FAKE_FRESH_SESSION_TOKEN_FOR_TEST_ONLY",
		"123456789012", strings.Repeat("e", 64), lateNow.Add(time.Hour),
	)
	reconcileRequest := request
	reconcileRequest.Session = readSession
	reconcileRequest.Source = RclonePrivateLocator{}
	reconcileRequest.RcloneConfig = nil
	reconcileRequest.Runtime = RemoteCommandAccess{}
	reconcileRequest.Manifest = RcloneManifestBundle{}
	reconcileRequest.MaxVerifyBytes = 0
	reconcileRequest.LowLevelRetries = 0
	latePublisher := NewRcloneNativePublisher(dataPlane, func() time.Time { return lateNow })
	lateReconciled, err := latePublisher.Reconcile(context.Background(), reconcileRequest)
	if err != nil || !reflect.DeepEqual(lateReconciled, commit) {
		t.Fatalf("late reconcile=%+v err=%v want=%+v", lateReconciled, err, commit)
	}
	healthReferences, err := LoadRcloneNativeHealthReferences(context.Background(), s3, reconcileRequest, commit, 8)
	if err != nil {
		t.Fatalf("load exact native health references: %v", err)
	}
	encodedDataPath, err := EncodeRcloneV1744S3Path("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	dataKey := profile.ManagedPrefix + "data/" + encodedDataPath
	seenData, seenCommit := false, false
	for _, reference := range healthReferences {
		seenData = seenData || (reference.Entry.PhysicalKey == dataKey && reference.Entry.VersionID == "opaque-data-v1")
		seenCommit = seenCommit || (reference.Entry.PhysicalKey == commit.Native.CommitKey && reference.Entry.VersionID == commit.Native.CommitVersionID)
	}
	if !seenData || !seenCommit {
		t.Fatalf("exact native health references=%+v", healthReferences)
	}
	s3.addObject(commitKey, "opaque-duplicate-commit", []byte(commitPayload), RcloneNativeSSES3V1, "", false)
	exactReconcileRequest := reconcileRequest
	exactReconcileRequest.ExactCommitKey = commit.Native.CommitKey
	exactReconcileRequest.ExactCommitVersionID = commit.Native.CommitVersionID
	listEventsBeforeExact := countRcloneNativeTestEvents(events, "list")
	exactReconciled, err := latePublisher.Reconcile(context.Background(), exactReconcileRequest)
	if err != nil || !reflect.DeepEqual(exactReconciled, commit) {
		t.Fatalf("exact late reconcile=%+v err=%v want=%+v", exactReconciled, err, commit)
	}
	if got := countRcloneNativeTestEvents(events, "list"); got != listEventsBeforeExact {
		t.Fatalf("exact reconcile listed current control prefix: before=%d after=%d", listEventsBeforeExact, got)
	}
	if _, err := publisher.Reconcile(context.Background(), request); rcloneNativeReason(err) != backupasset.RcloneReasonMarkerMismatch {
		t.Fatalf("multiple commit versions error=%v reason=%q", err, rcloneNativeReason(err))
	}
}

func TestRcloneNativeCatalogReopensExactControlAndObjectVersionsWithoutCurrentFallback(t *testing.T) {
	now := time.Date(2026, 7, 16, 1, 30, 0, 0, time.UTC)
	profile := validRcloneNativeProfileForTest()
	payload := []byte("hello")
	manifestJSON := fmt.Sprintf(`[{"Path":"a.txt","Name":"a.txt","Size":%d,"ModTime":"2026-07-16T01:00:01Z","IsDir":false,"Hashes":{"sha256":"%s"},"Metadata":{"mode":"100640","uid":"1000","gid":"1000","mtime":"2026-07-16T01:00:01Z"}}]`, len(payload), sha256Hex(payload))
	manifest, err := BuildRcloneManifestV1(context.Background(), strings.NewReader(manifestJSON), rcloneManifestOptionsForTest())
	if err != nil {
		t.Fatal(err)
	}
	b0, err := NewRcloneNativeStableGraph(RcloneNativeFullObservation{PageCount: 1}, RcloneNativeFullObservation{PageCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	attempt := validRcloneAttemptForTest(backupasset.PublicationNativeObjectVersions)
	attempt.Native.B0VersionGraphDigest = b0.Digest
	session := newRcloneNativeSession(
		"FAKE_AWS_ACCESS_KEY_ID_FOR_TEST_ONLY", "FAKE_AWS_SECRET_ACCESS_KEY_FOR_TEST_ONLY", "FAKE_AWS_SESSION_TOKEN_FOR_TEST_ONLY",
		"123456789012", attempt.Native.RoleSessionIdentityDigest, attempt.Native.SessionExpiresAt,
	)
	config, err := BuildRcloneNativeRcloneConfig(profile, RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1}, session)
	if err != nil {
		t.Fatal(err)
	}
	attempt.ConfigDigest = sha256Hex(config)
	events := make([]string, 0)
	s3 := newRcloneNativePublisherS3Fake(profile, now, &events)
	dataPlane := &rcloneNativeDataPlaneFake{observations: []RcloneManifestBundle{manifest, manifest}, s3: s3, payload: payload, events: &events}
	publisher := NewRcloneNativePublisher(dataPlane, func() time.Time { return now })
	request := RcloneNativePublicationRequest{
		Attempt: attempt, Profile: profile, Session: session, ClientFactory: rcloneNativeClientFactoryFake{s3: s3},
		Source: mustRclonePrivateLocatorForTest(t, "/srv/source"), RcloneConfig: config, Runtime: RemoteCommandAccess{Node: model.Node{ID: 9}},
		Manifest: manifest, ManifestOptions: rcloneManifestOptionsForTest(),
		ObservationLimits: RcloneNativeObservationLimits{PageSize: 1000, MaxPages: 4, MaxRecords: 100},
		Encryption:        RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1}, EncryptionEvidence: RcloneNativeEncryptionEvidence{Profile: RcloneNativeSSES3V1},
		MarkerKey: []byte("FAKE_NATIVE_MARKER_AUTH_KEY_32_BYTES_FOR_TEST_ONLY"), CapabilityEvidenceDigest: strings.Repeat("a", 64),
		CostEvidenceDigest: strings.Repeat("b", 64), MaxVerifyBytes: 1 << 20, ControlPayloadMaxBytes: 1 << 20, LowLevelRetries: 3,
	}
	commit, err := publisher.Publish(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	writesBefore := len(s3.writes)
	listsBefore := countString(events, "list")
	physical, err := EncodeRcloneV1744S3Path("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	s3.addObject(profile.ManagedPrefix+"data/"+physical, "opaque-current-root-replacement", []byte("replacement"), RcloneNativeSSES3V1, "", false)

	reconcile := request
	reconcile.Source = RclonePrivateLocator{}
	reconcile.RcloneConfig = nil
	reconcile.Runtime = RemoteCommandAccess{}
	reconcile.Manifest = RcloneManifestBundle{}
	reconcile.MaxVerifyBytes = 0
	reconcile.LowLevelRetries = 0
	reconcile.ExactCommitKey = commit.Native.CommitKey
	reconcile.ExactCommitVersionID = commit.Native.CommitVersionID
	strategy, err := NewRclonePublicationStrategy(
		NewRclonePortablePublisher(&fakeRclonePortableRemote{}, func(time.Duration) {}, func() time.Time { return now }), publisher,
	)
	if err != nil {
		t.Fatal(err)
	}
	readRequest := CatalogReadRequest{
		Provider: backupasset.ProviderRclone, RecoveryPointID: attempt.RecoveryPointID,
		Snapshot: ReadSnapshot{RepositoryID: attempt.RepositoryID, CapabilityRevision: int(attempt.CapabilityRevision), SourceRevision: strings.Repeat("f", 64),
			Access: AccessBinding{Provider: backupasset.ProviderRclone, RepositoryID: attempt.RepositoryID}},
		Point: PointLocator{Native: "FAKE_EXACT_NATIVE_POINT_FOR_TEST_ONLY"}, Mode: CatalogProofPublicationManifest,
		Manifest: CatalogManifestProof{ManifestID: strings.Repeat("9", 32), Revision: 1, DigestAlgorithm: "sha256", Digest: commit.ManifestIndexDigest,
			EntryCount: int64(commit.ManifestEntryCount), Completeness: backupasset.ManifestComplete, SourceRevision: strings.Repeat("f", 64)},
		RcloneProof: &RcloneCatalogProofInput{Reconcile: RcloneReconcileInput{ManifestLimits: request.ManifestOptions.Limits, NativeRequest: &reconcile}, Commit: commit},
		MaxItems:    int(commit.ManifestEntryCount) + 1,
	}
	catalogSession, err := strategy.OpenCatalogRead(context.Background(), readRequest)
	if err != nil {
		t.Fatalf("open native Catalog: %v", err)
	}
	page, err := catalogSession.ListCanonical(context.Background(), PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].NormalizedPath != "a.txt" ||
		!strings.Contains(page.Items[0].ProviderLocator.Native, "opaque-data-v1") ||
		strings.Contains(page.Items[0].ProviderLocator.Native, "opaque-current-root-replacement") {
		t.Fatalf("native Catalog page=%+v err=%v", page, err)
	}
	proof, err := catalogSession.Finalize(context.Background())
	if err != nil || proof.Manifest != readRequest.Manifest || !proof.Catalog.Complete {
		t.Fatalf("native Catalog proof=%+v err=%v", proof, err)
	}
	if len(s3.writes) != writesBefore || countString(events, "list") != listsBefore {
		t.Fatalf("native Catalog used mutation/current listing writes=%d/%d lists=%d/%d events=%v", len(s3.writes), writesBefore, countString(events, "list"), listsBefore, events)
	}
}

func countString(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func countRcloneNativeTestEvents(events []string, want string) int {
	count := 0
	for _, event := range events {
		if event == want {
			count++
		}
	}
	return count
}

func TestCommandRcloneNativeDataPlaneUsesFrozenConfigAndExactManagedDestination(t *testing.T) {
	transport := &fakeCommandTransport{}
	limits := func() (OperationLimits, error) { return NewMetadataOperationLimits(time.Minute, 1<<20) }
	dataPlane, err := NewCommandRcloneNativeDataPlane(transport, limits)
	if err != nil {
		t.Fatal(err)
	}
	attempt := validRcloneAttemptForTest(backupasset.PublicationNativeObjectVersions)
	profile := validRcloneNativeProfileForTest()
	config := []byte("FAKE_NATIVE_RCLONE_CONFIG_FOR_TEST_ONLY")
	request := RcloneNativePublicationRequest{
		Attempt: attempt, Profile: profile, Source: mustRclonePrivateLocatorForTest(t, "/srv/source"),
		RcloneConfig: config, Runtime: RemoteCommandAccess{Node: model.Node{ID: 9}}, LowLevelRetries: 3,
	}
	if err := dataPlane.Sync(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("command count=%d", len(transport.requests))
	}
	invocation := transport.requests[0]
	if invocation.Operation != OperationRcloneManagedNativeSync || invocation.Purpose != CommandPurposePublish ||
		invocation.RcloneSource == nil || invocation.RcloneSource.value != "/srv/source" ||
		invocation.RcloneDestination == nil || invocation.RcloneDestination.value != "xirang_native:xirang-native-test/managed/v1/data" ||
		!bytes.Equal(invocation.SecretStdin, config) || invocation.RcloneLowLevelRetries != 3 ||
		!invocation.AbsoluteDeadline.Equal(attempt.PointDeadlineAt) {
		t.Fatalf("native sync invocation=%+v", invocation)
	}
	if err := invocation.Validate(); err != nil {
		t.Fatalf("native sync invocation rejected: %v", err)
	}
}

func newRcloneNativeControlStoreFake() *rcloneNativeControlStoreFake {
	return &rcloneNativeControlStoreFake{
		versions: make(map[string]RcloneNativeControlObjectVersion),
		payloads: make(map[string][]byte),
	}
}

func (fake *rcloneNativeControlStoreFake) PutControlVersion(_ context.Context, request RcloneNativeControlWriteRequest) (RcloneNativeControlWriteResult, error) {
	fake.writes = append(fake.writes, request)
	versionID := fmt.Sprintf("opaque-control-v%d", len(fake.writes))
	fake.versions[request.PhysicalKey] = RcloneNativeControlObjectVersion{
		PhysicalKey: request.PhysicalKey, VersionID: versionID, Size: uint64(len(request.Payload)),
		ContentDigest: sha256Hex(request.Payload), EncryptionProfile: request.EncryptionProfile,
		KMSKeyDigest: request.KMSKeyDigest, BucketKeyEnabled: request.BucketKeyEnabled,
	}
	fake.payloads[request.PhysicalKey] = append([]byte(nil), request.Payload...)
	return RcloneNativeControlWriteResult{VersionID: versionID}, nil
}

func (fake *rcloneNativeControlStoreFake) HeadVersion(_ context.Context, request RcloneNativeExactReadRequest) (RcloneNativeExactObjectHead, error) {
	version, exists := fake.versions[request.PhysicalKey]
	if !exists {
		return RcloneNativeExactObjectHead{}, errors.New("FAKE_CONTROL_HEAD_NOT_FOUND_FOR_TEST_ONLY")
	}
	return RcloneNativeExactObjectHead{
		PhysicalKey: version.PhysicalKey, VersionID: version.VersionID, Size: version.Size,
		EncryptionProfile: version.EncryptionProfile, KMSKeyDigest: version.KMSKeyDigest, BucketKeyEnabled: version.BucketKeyEnabled,
	}, nil
}

func (fake *rcloneNativeControlStoreFake) OpenVersion(_ context.Context, request RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	payload, exists := fake.payloads[request.PhysicalKey]
	if !exists {
		return nil, errors.New("FAKE_CONTROL_BODY_NOT_FOUND_FOR_TEST_ONLY")
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func TestRcloneNativeControlCommitWritesCommitLastAndExactVerifiesEveryVersion(t *testing.T) {
	store := newRcloneNativeControlStoreFake()
	request := RcloneNativeControlCommitRequest{
		ManifestChunks: []RcloneNativeControlPayload{
			{PhysicalKey: "managed/v1/control/manifest-000000.jsonl", Payload: []byte("manifest-0\n")},
			{PhysicalKey: "managed/v1/control/manifest-000001.jsonl", Payload: []byte("manifest-1\n")},
		},
		ManifestIndex:     RcloneNativeControlPayload{PhysicalKey: "managed/v1/control/manifest-index.json", Payload: []byte("index")},
		Commit:            RcloneNativeControlPayload{PhysicalKey: "managed/v1/control/commit.json", Payload: []byte("commit")},
		EncryptionProfile: RcloneNativeSSES3V1,
		MaxObjectBytes:    1024,
	}
	graph, err := PublishRcloneNativeControlCommit(context.Background(), store, request)
	if err != nil || len(graph.ManifestVersions) != 2 || graph.IndexVersion.VersionID == "" || graph.CommitVersion.VersionID == "" || graph.Digest == "" {
		t.Fatalf("graph=%+v writes=%+v err=%v", graph, store.writes, err)
	}
	wantOrder := []string{
		request.ManifestChunks[0].PhysicalKey,
		request.ManifestChunks[1].PhysicalKey,
		request.ManifestIndex.PhysicalKey,
		request.Commit.PhysicalKey,
	}
	if len(store.writes) != len(wantOrder) {
		t.Fatalf("writes=%d want=%d", len(store.writes), len(wantOrder))
	}
	for index, want := range wantOrder {
		if store.writes[index].PhysicalKey != want {
			t.Fatalf("write[%d]=%q want=%q", index, store.writes[index].PhysicalKey, want)
		}
	}
	if graph.CommitVersion.PhysicalKey != request.Commit.PhysicalKey || graph.CommitVersion.ContentDigest != sha256Hex(request.Commit.Payload) {
		t.Fatalf("commit version=%+v", graph.CommitVersion)
	}

	bad := request
	bad.Commit.PhysicalKey = bad.ManifestIndex.PhysicalKey
	if _, err := PublishRcloneNativeControlCommit(context.Background(), newRcloneNativeControlStoreFake(), bad); err == nil {
		t.Fatal("duplicate control key unexpectedly succeeded")
	}
	bad = request
	bad.MaxObjectBytes = uint64(len(request.Commit.Payload) - 1)
	if _, err := PublishRcloneNativeControlCommit(context.Background(), newRcloneNativeControlStoreFake(), bad); rcloneNativeReason(err) != backupasset.RcloneReasonProviderResourceLimit {
		t.Fatalf("control budget error=%v reason=%q", err, rcloneNativeReason(err))
	}
}

var _ RcloneNativeExactReader = (*rcloneNativeExactReaderFake)(nil)
var _ RcloneNativeExactRangeReader = (*rcloneNativeExactRangeReaderFake)(nil)
var _ RcloneNativeVersionEnumerator = (*rcloneNativeVersionEnumeratorFake)(nil)
var _ RcloneNativeControlStore = (*rcloneNativeControlStoreFake)(nil)
var _ = errors.Is
