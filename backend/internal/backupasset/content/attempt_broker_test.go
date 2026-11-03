package content

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestAttemptBrokerProvidesBoundedSequentialAndMultipleRangeReads(t *testing.T) {
	payload := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	budget := &attemptBudgetFake{}
	resolver := &attemptResolverFake{payload: payload, budget: budget}
	clock := func() time.Time { return time.Date(2026, 7, 19, 6, 7, 8, 0, time.UTC) }
	broker, err := NewAttemptBroker(resolver, budget, clock)
	if err != nil {
		t.Fatal(err)
	}
	session, info, err := broker.OpenSession(context.Background(), validAttemptBinding(clock().Add(time.Minute)))
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if info.Size != int64(len(payload)) || info.MediaType != "application/octet-stream" || !info.FingerprintStrong || !info.Sequential || !info.Range {
		t.Fatalf("unsafe or incomplete activation info: %+v", info)
	}

	sequential, err := session.OpenSequential(context.Background(), 8)
	if err != nil {
		t.Fatalf("OpenSequential: %v", err)
	}
	if got, err := io.ReadAll(sequential); err != nil || string(got) != "01234567" {
		t.Fatalf("sequential read=%q err=%v", got, err)
	}
	if err := sequential.Close(); err != nil {
		t.Fatalf("close sequential: %v", err)
	}
	for _, test := range []struct {
		offset int64
		length int64
		want   string
	}{{4, 5, "45678"}, {12, 4, "cdef"}} {
		read, err := session.OpenRange(context.Background(), test.offset, test.length)
		if err != nil {
			t.Fatalf("OpenRange(%d,%d): %v", test.offset, test.length, err)
		}
		got, readErr := io.ReadAll(read)
		closeErr := read.Close()
		if readErr != nil || closeErr != nil || string(got) != test.want {
			t.Fatalf("range read=%q readErr=%v closeErr=%v", got, readErr, closeErr)
		}
	}
	if err := session.Revalidate(context.Background()); err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close session: %v", err)
	}
	if got := budget.snapshot(); got.reservations != 5 || got.finalizations != 5 || got.unknown != 0 {
		t.Fatalf("attempt accounting=%+v", got)
	}
	if resolver.openBeforeReserve {
		t.Fatal("source was opened before atomic budget reservation")
	}
}

func TestAttemptBrokerAcceptsEmptySourceMediaTypeAndRejectsFingerprintDrift(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 6, 7, 8, 0, time.UTC)
	payload := []byte("payload")
	stat := SourceStat{
		Size: 7, MediaType: "", SourceFingerprint: "source-v1",
		EntryFingerprint: "entry-v1", FingerprintStrong: true,
	}
	budget := &attemptBudgetFake{}
	resolver := &attemptResolverFake{payload: payload, budget: budget, stat: &stat}
	broker, err := NewAttemptBroker(resolver, budget, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	binding := validAttemptBinding(now.Add(time.Minute))
	session, info, err := broker.OpenSession(ctx, binding)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close input session: %v", err)
		}
	})
	if info.Size != 7 || info.MediaType != "" || !info.FingerprintStrong || !info.Sequential || !info.Range || session.Info() != info {
		t.Fatalf("source info=%+v session info=%+v", info, session.Info())
	}
	read, err := session.OpenSequential(ctx, 7)
	if err != nil {
		t.Fatalf("OpenSequential: %v", err)
	}
	got, readErr := io.ReadAll(read)
	closeErr := read.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("sequential read=%q readErr=%v closeErr=%v", got, readErr, closeErr)
	}
	if err := session.Revalidate(ctx); err != nil {
		t.Fatalf("Revalidate: %v", err)
	}
	stat.SourceFingerprint = "source-v2"
	if _, err := session.OpenSequential(ctx, 7); !errors.Is(err, ErrAttemptSourceChanged) {
		t.Fatalf("changed source fingerprint: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close session: %v", err)
	}
	if got := budget.snapshot(); got.reservations != 4 || got.finalizations != 4 || got.unknown != 1 || got.charged != 14 {
		t.Fatalf("attempt accounting=%+v", got)
	}
	wantIntents := []AttemptReadIntent{
		{SessionID: binding.SessionID, Mode: SourceModeStat},
		{SessionID: binding.SessionID, Mode: SourceModeSequential, Bytes: 7},
		{SessionID: binding.SessionID, Mode: SourceModeStat},
		{SessionID: binding.SessionID, Mode: SourceModeSequential, Bytes: 7},
	}
	wantFinalizations := []AttemptReadFinalization{
		{ReservationID: strings.Repeat("b", 32), EvidenceKnown: true, Succeeded: true},
		{ReservationID: strings.Repeat("c", 32), ReservedBytes: 7, ProviderBytes: 7, EvidenceKnown: true, Succeeded: true},
		{ReservationID: strings.Repeat("d", 32), EvidenceKnown: true, Succeeded: true},
		{ReservationID: strings.Repeat("e", 32), ReservedBytes: 7},
	}
	if len(budget.intents) != len(wantIntents) || len(budget.results) != len(wantFinalizations) {
		t.Fatalf("intents=%+v finalizations=%+v", budget.intents, budget.results)
	}
	for index := range wantIntents {
		if budget.intents[index] != wantIntents[index] || budget.results[index] != wantFinalizations[index] {
			t.Fatalf("accounting[%d]: intent=%+v finalization=%+v", index, budget.intents[index], budget.results[index])
		}
	}
}

func TestAttemptBrokerRejectsSourceDriftAndChargesUnknownConservatively(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 7, 19, 6, 7, 8, 0, time.UTC) }
	budget := &attemptBudgetFake{}
	resolver := &attemptResolverFake{payload: []byte("payload"), budget: budget, driftAfterOpen: true}
	broker, err := NewAttemptBroker(resolver, budget, clock)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = broker.OpenSession(context.Background(), validAttemptBinding(clock().Add(time.Minute)))
	if !errors.Is(err, ErrAttemptSourceChanged) {
		t.Fatalf("source drift got %v", err)
	}
	if got := budget.snapshot(); got.reservations != 1 || got.finalizations != 1 || got.unknown != 1 || got.charged != 0 {
		t.Fatalf("drift accounting=%+v", got)
	}
}

func TestAttemptBrokerRejectsExpiredDisallowedAndOverBudgetReads(t *testing.T) {
	now := time.Date(2026, 7, 19, 6, 7, 8, 0, time.UTC)
	budget := &attemptBudgetFake{}
	resolver := &attemptResolverFake{payload: []byte("0123456789"), budget: budget}
	broker, err := NewAttemptBroker(resolver, budget, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	expired := validAttemptBinding(now)
	if _, _, err := broker.OpenSession(context.Background(), expired); !errors.Is(err, ErrAttemptSessionDenied) {
		t.Fatalf("expired session got %v", err)
	}
	binding := validAttemptBinding(now.Add(time.Minute))
	binding.AllowedModes = []SourceMode{SourceModeStat, SourceModeSequential}
	session, _, err := broker.OpenSession(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.OpenRange(context.Background(), 0, 1); !errors.Is(err, ErrAttemptSessionDenied) {
		t.Fatalf("disallowed Range got %v", err)
	}
	if _, err := session.OpenSequential(context.Background(), binding.Limits.MaxBytesPerRequest+1); !errors.Is(err, ErrAttemptBudgetExceeded) {
		t.Fatalf("oversize sequential got %v", err)
	}
}

func TestAttemptContractsDoNotSerializeSourceOrBudgetInternals(t *testing.T) {
	binding := validAttemptBinding(time.Now().UTC().Add(time.Minute))
	info := AttemptSourceInfo{Size: 1, MediaType: "text/plain", FingerprintStrong: true, Sequential: true, Range: true}
	for name, value := range map[string]any{"binding": binding, "info": info} {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(payload) != "{}" {
			t.Fatalf("private %s escaped: %s", name, payload)
		}
	}
}

func validAttemptBinding(expiresAt time.Time) AttemptSourceBinding {
	return AttemptSourceBinding{
		SessionID:           strings.Repeat("1", 32),
		Ref:                 backupasset.AssetRef{RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("b", 64)},
		CatalogGenerationID: strings.Repeat("c", 32), SourceFingerprint: "source-v1", EntryFingerprint: "entry-v1",
		AllowedModes:      []SourceMode{SourceModeStat, SourceModeSequential, SourceModeRange},
		Limits:            AttemptReadLimits{MaxBytesPerRequest: 16, MaxCumulativeBytes: 64, MaxRequests: 8, MaxInFlight: 2},
		AbsoluteExpiresAt: expiresAt,
	}
}

type attemptBudgetSnapshot struct {
	reservations  int
	finalizations int
	unknown       int
	charged       int64
}

type attemptBudgetFake struct {
	mu            sync.Mutex
	reservations  int
	finalizations int
	unknown       int
	charged       int64
	intents       []AttemptReadIntent
	results       []AttemptReadFinalization
}

func (budget *attemptBudgetFake) ReserveAttemptRead(_ context.Context, intent AttemptReadIntent) (AttemptReadReservation, error) {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	budget.reservations++
	budget.intents = append(budget.intents, intent)
	return AttemptReadReservation{ID: strings.Repeat(string(rune('a'+budget.reservations)), 32), ReservedBytes: intent.Bytes}, nil
}

func (budget *attemptBudgetFake) FinalizeAttemptRead(_ context.Context, finalization AttemptReadFinalization) error {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	budget.finalizations++
	budget.results = append(budget.results, finalization)
	if !finalization.EvidenceKnown {
		budget.unknown++
		budget.charged += finalization.ReservedBytes
		return nil
	}
	budget.charged += finalization.ProviderBytes
	return nil
}

func (budget *attemptBudgetFake) snapshot() attemptBudgetSnapshot {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return attemptBudgetSnapshot{budget.reservations, budget.finalizations, budget.unknown, budget.charged}
}

type attemptResolverFake struct {
	payload           []byte
	budget            *attemptBudgetFake
	openBeforeReserve bool
	driftAfterOpen    bool
	stat              *SourceStat
}

func (resolver *attemptResolverFake) OpenContentSource(_ context.Context, request SourceRequest) (SourceSession, error) {
	if resolver.budget.snapshot().reservations == 0 {
		resolver.openBeforeReserve = true
	}
	start := int64(0)
	length := int64(len(resolver.payload))
	if request.Mode == SourceModeSequential {
		length = request.MaxBytes
	}
	if request.Mode == SourceModeRange {
		start = request.Range.Offset
		length = request.Range.Length
	}
	if start > int64(len(resolver.payload)) {
		start = int64(len(resolver.payload))
	}
	end := start + length
	if end > int64(len(resolver.payload)) {
		end = int64(len(resolver.payload))
	}
	reader := &attemptSourceReaderFake{Reader: bytes.NewReader(resolver.payload[start:end]), providerBytes: end - start}
	return &attemptSourceSessionFake{request: request, reader: reader, drift: resolver.driftAfterOpen, stat: resolver.stat}, nil
}

func (*attemptResolverFake) ValidateContentCacheRoot(context.Context, string) error { return nil }

type attemptSourceSessionFake struct {
	request SourceRequest
	reader  *attemptSourceReaderFake
	drift   bool
	closed  bool
	stat    *SourceStat
}

func (session *attemptSourceSessionFake) Stat() SourceStat {
	if session.stat != nil {
		return *session.stat
	}
	source := session.request.ExpectedSource
	if session.drift {
		source = "changed"
	}
	return SourceStat{
		Size: 36, MediaType: "application/octet-stream", SourceFingerprint: source,
		EntryFingerprint: session.request.ExpectedEntry, FingerprintStrong: true,
	}
}

func (*attemptSourceSessionFake) Capabilities() SourceCapabilities {
	return SourceCapabilities{Sequential: true, Range: true}
}

func (session *attemptSourceSessionFake) Reader() SourceReader { return session.reader }

func (session *attemptSourceSessionFake) Revalidate(context.Context) error {
	if session.drift {
		return errors.New("changed")
	}
	return nil
}

func (session *attemptSourceSessionFake) Close() error {
	session.closed = true
	return nil
}

type attemptSourceReaderFake struct {
	*bytes.Reader
	providerBytes int64
	closed        bool
}

func (reader *attemptSourceReaderFake) Close() error {
	reader.closed = true
	return nil
}

func (reader *attemptSourceReaderFake) ProviderBytes() int64 { return reader.providerBytes }

var _ SourceResolver = (*attemptResolverFake)(nil)
