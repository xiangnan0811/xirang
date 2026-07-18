package provider

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

type catalogTreeReaderFake struct {
	mu        sync.Mutex
	provider  backupasset.ProviderKind
	tree      map[string][]Entry
	readCalls int
	mutations int
	block     <-chan struct{}
	err       error
	proof     CatalogManifestProof
	proofErr  error
}

func (reader *catalogTreeReaderFake) ProveCatalogManifest(_ context.Context, request CatalogReadRequest) (CatalogManifestProof, error) {
	if reader.proofErr != nil {
		return CatalogManifestProof{}, reader.proofErr
	}
	if reader.proof != (CatalogManifestProof{}) {
		return reader.proof, nil
	}
	return request.Manifest, nil
}

type catalogUnprovenReaderFake struct{ reader *catalogTreeReaderFake }

type catalogReaderRouteFake struct {
	opened  int
	request CatalogReadRequest
}

func (reader *catalogReaderRouteFake) OpenCatalogRead(_ context.Context, request CatalogReadRequest) (CatalogReadSession, error) {
	reader.opened++
	reader.request = request
	return nil, nil
}

func (reader *catalogUnprovenReaderFake) ListEntries(ctx context.Context, snapshot ReadSnapshot, point PointLocator, parent EntryLocator, request PageRequest) (EntryPage, error) {
	return reader.reader.ListEntries(ctx, snapshot, point, parent, request)
}

func (reader *catalogTreeReaderFake) ListEntries(ctx context.Context, snapshot ReadSnapshot, _ PointLocator, parent EntryLocator, request PageRequest) (EntryPage, error) {
	reader.mu.Lock()
	reader.readCalls++
	reader.mu.Unlock()
	if reader.block != nil {
		select {
		case <-ctx.Done():
			return EntryPage{}, ctx.Err()
		case <-reader.block:
		}
	}
	if reader.err != nil {
		return EntryPage{}, reader.err
	}
	if snapshot.Access.Provider != reader.provider {
		return EntryPage{}, errors.New("wrong provider")
	}
	items := append([]Entry(nil), reader.tree[parent.Native]...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].OpaqueDigest < items[j].OpaqueDigest
		}
		return items[i].Name < items[j].Name
	})
	limit := request.Limit
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	start := 0
	if request.Cursor != "" {
		for index := range items {
			if items[index].OpaqueDigest == request.Cursor {
				start = index + 1
				break
			}
		}
	}
	end := min(start+limit, len(items))
	page := EntryPage{Items: append([]Entry(nil), items[start:end]...)}
	if end < len(items) && end > start {
		page.NextCursor = items[end-1].OpaqueDigest
	}
	return page, nil
}

func catalogContractRequest(kind backupasset.ProviderKind) CatalogReadRequest {
	return CatalogReadRequest{
		Provider:        kind,
		RecoveryPointID: "11111111111111111111111111111111",
		Snapshot: ReadSnapshot{
			RepositoryID: "22222222222222222222222222222222", CapabilityRevision: 3,
			SourceRevision: strings.Repeat("a", 64), Access: AccessBinding{Provider: kind, RepositoryID: "22222222222222222222222222222222"},
		},
		Point: PointLocator{Native: "FAKE_EXACT_POINT_FOR_TEST_ONLY"},
		Mode:  CatalogProofPublicationManifest,
		Manifest: CatalogManifestProof{
			ManifestID: "33333333333333333333333333333333", Revision: 2,
			DigestAlgorithm: "sha256", Digest: strings.Repeat("b", 64),
			EntryCount: 2, Completeness: backupasset.ManifestComplete,
			SourceRevision: strings.Repeat("a", 64),
		},
		MaxItems: 20,
	}
}

func catalogContractTree() map[string][]Entry {
	return map[string][]Entry{
		"": {
			{OpaqueDigest: strings.Repeat("1", 64), Name: "docs", Type: backupasset.CatalogEntryDirectory, ModTime: time.Unix(10, 0).UTC(), Locator: EntryLocator{Native: "docs"}},
		},
		"docs": {
			{OpaqueDigest: strings.Repeat("2", 64), Name: "report.txt", Type: backupasset.CatalogEntryFile, Size: 42, ModTime: time.Unix(20, 0).UTC(), Locator: EntryLocator{Native: "docs/report.txt"}},
		},
	}
}

func runCatalogProviderContract(t *testing.T, kind backupasset.ProviderKind) {
	t.Helper()
	reader := &catalogTreeReaderFake{provider: kind, tree: catalogContractTree()}
	request := catalogContractRequest(kind)
	session, err := NewCatalogReadSession(reader, request)
	if err != nil {
		t.Fatalf("open Catalog session: %v", err)
	}
	if session.SourceRevision() != request.Snapshot.SourceRevision {
		t.Fatalf("source revision=%q", session.SourceRevision())
	}
	if _, err := session.Finalize(context.Background()); !errors.Is(err, ErrCatalogSessionIncomplete) {
		t.Fatalf("finalize before EOF error=%v", err)
	}

	var records []CatalogRecord
	cursor := ""
	for {
		page, err := session.ListCanonical(context.Background(), PageRequest{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatalf("list Catalog page: %v", err)
		}
		records = append(records, page.Items...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(records) != 2 || records[0].NormalizedPath != "docs" || records[1].NormalizedPath != "docs/report.txt" {
		t.Fatalf("records=%#v", records)
	}
	proof, err := session.Finalize(context.Background())
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if proof.Provider != kind || proof.Mode != CatalogProofPublicationManifest || proof.Manifest != request.Manifest ||
		proof.Catalog.EntryCount != 2 || proof.Catalog.DigestAlgorithm != "sha256" || len(proof.Catalog.Digest) != 64 || !proof.Catalog.Complete {
		t.Fatalf("proof=%#v", proof)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
	if reader.mutations != 0 {
		t.Fatalf("Catalog session performed %d mutations", reader.mutations)
	}
}

func TestResticCatalogContract(t *testing.T) {
	runCatalogProviderContract(t, backupasset.ProviderRestic)
}
func TestRsyncCatalogContract(t *testing.T) { runCatalogProviderContract(t, backupasset.ProviderRsync) }
func TestRcloneCatalogContract(t *testing.T) {
	runCatalogProviderContract(t, backupasset.ProviderRclone)
}

func TestCatalogContractAllowsProvenEmptyPoint(t *testing.T) {
	t.Parallel()

	request := catalogContractRequest(backupasset.ProviderRestic)
	request.Manifest.EntryCount = 0
	reader := &catalogTreeReaderFake{provider: backupasset.ProviderRestic, tree: map[string][]Entry{}}
	session, err := NewCatalogReadSession(reader, request)
	if err != nil {
		t.Fatalf("open empty Catalog session: %v", err)
	}
	page, err := session.ListCanonical(context.Background(), PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("empty page=%#v err=%v", page, err)
	}
	proof, err := session.Finalize(context.Background())
	if err != nil || proof.Catalog.EntryCount != 0 || !proof.Catalog.Complete || proof.Catalog.Digest == "" {
		t.Fatalf("empty proof=%#v err=%v", proof, err)
	}
}

func TestCatalogContractRejectsDuplicateAndUnsafeRecords(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		tree map[string][]Entry
	}{
		{"duplicate", map[string][]Entry{"": {
			{OpaqueDigest: strings.Repeat("1", 64), Name: "same", Type: backupasset.CatalogEntryFile, Locator: EntryLocator{Native: "same"}},
			{OpaqueDigest: strings.Repeat("2", 64), Name: "same", Type: backupasset.CatalogEntryFile, Locator: EntryLocator{Native: "same"}},
		}}},
		{"unsafe", map[string][]Entry{"": {
			{OpaqueDigest: strings.Repeat("1", 64), Name: "../escape", Type: backupasset.CatalogEntryFile, Locator: EntryLocator{Native: "escape"}},
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := catalogContractRequest(backupasset.ProviderRsync)
			reader := &catalogTreeReaderFake{provider: backupasset.ProviderRsync, tree: test.tree}
			session, err := NewCatalogReadSession(reader, request)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if _, err := session.ListCanonical(context.Background(), PageRequest{Limit: 10}); !errors.Is(err, ErrCatalogProtocol) {
				t.Fatalf("record error=%v", err)
			}
		})
	}
}

func TestCatalogContractCancellationAndClose(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	reader := &catalogTreeReaderFake{provider: backupasset.ProviderRclone, tree: catalogContractTree(), block: block}
	session, err := NewCatalogReadSession(reader, catalogContractRequest(backupasset.ProviderRclone))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.ListCanonical(ctx, PageRequest{Limit: 10}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	close(block)
	if err := session.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := session.ListCanonical(context.Background(), PageRequest{Limit: 10}); !errors.Is(err, ErrCatalogSessionClosed) {
		t.Fatalf("list after close error=%v", err)
	}
}

func TestCatalogContractRejectsUnverifiedOrDriftingPublicationProof(t *testing.T) {
	t.Parallel()

	request := catalogContractRequest(backupasset.ProviderRestic)
	unproven := &catalogUnprovenReaderFake{reader: &catalogTreeReaderFake{provider: request.Provider, tree: catalogContractTree()}}
	if _, err := NewCatalogReadSession(unproven, request); !errors.Is(err, ErrCatalogProtocol) {
		t.Fatalf("unproven publication reader error=%v", err)
	}

	drifting := &catalogTreeReaderFake{provider: request.Provider, tree: catalogContractTree(), proof: request.Manifest}
	drifting.proof.Digest = strings.Repeat("c", 64)
	session, err := NewCatalogReadSession(drifting, request)
	if err != nil {
		t.Fatalf("open drifting session: %v", err)
	}
	if _, err := session.ListCanonical(context.Background(), PageRequest{Limit: 10}); err != nil {
		t.Fatalf("enumerate drifting session: %v", err)
	}
	if _, err := session.Finalize(context.Background()); !errors.Is(err, ErrCatalogProofMismatch) {
		t.Fatalf("drifting proof error=%v", err)
	}
}

func TestCatalogProviderAdaptersRejectUnprovenExactSources(t *testing.T) {
	t.Parallel()

	resticRequest := catalogContractRequest(backupasset.ProviderRestic)
	if _, err := newResticAdapterForTest(t, &fakeCommandTransport{}).OpenCatalogRead(context.Background(), resticRequest); !errors.Is(err, ErrCatalogProtocol) {
		t.Fatalf("Restic unproven exact source error=%v", err)
	}

	rsyncRequest := catalogContractRequest(backupasset.ProviderRsync)
	if _, err := newRsyncAdapterForTest(t).OpenCatalogRead(context.Background(), rsyncRequest); !errors.Is(err, ErrCatalogProtocol) {
		t.Fatalf("mutable Rsync accepted publication proof: %v", err)
	}

	rcloneRequest := catalogContractRequest(backupasset.ProviderRclone)
	if _, err := newRcloneAdapterForTest(t, &fakeCommandTransport{}).OpenCatalogRead(context.Background(), rcloneRequest); !errors.Is(err, ErrCatalogProtocol) {
		t.Fatalf("Rclone unproven exact source error=%v", err)
	}
}

func TestRcloneCatalogReaderRoutesOnlyClosedProofModes(t *testing.T) {
	mutable := &catalogReaderRouteFake{}
	immutable := &catalogReaderRouteFake{}
	reader, err := NewRcloneCatalogReader(mutable, immutable)
	if err != nil {
		t.Fatal(err)
	}
	request := catalogContractRequest(backupasset.ProviderRclone)
	request.Mode = CatalogProofMutableObservation
	request.Manifest = CatalogManifestProof{}
	if _, err := reader.OpenCatalogRead(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if mutable.opened != 1 || immutable.opened != 0 {
		t.Fatalf("mutable route counts=%d/%d", mutable.opened, immutable.opened)
	}
	request.Mode = CatalogProofPublicationManifest
	request.Manifest = catalogContractRequest(backupasset.ProviderRclone).Manifest
	if _, err := reader.OpenCatalogRead(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if mutable.opened != 1 || immutable.opened != 1 {
		t.Fatalf("immutable route counts=%d/%d", mutable.opened, immutable.opened)
	}
	request.Mode = CatalogProofMode("future")
	if _, err := reader.OpenCatalogRead(context.Background(), request); !errors.Is(err, ErrCatalogProtocol) {
		t.Fatalf("unknown proof route error=%v", err)
	}
}
