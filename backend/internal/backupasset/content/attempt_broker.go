package content

import (
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
)

var (
	ErrInvalidAttemptBinding = errors.New("invalid attempt source binding")
	ErrAttemptSessionDenied  = errors.New("attempt input session denied")
	ErrAttemptBudgetExceeded = errors.New("attempt input budget exceeded")
	ErrAttemptSourceChanged  = errors.New("attempt input source changed")
	ErrAttemptSourceFailed   = errors.New("attempt input source unavailable")
)

type AttemptReadLimits struct {
	MaxBytesPerRequest int64 `json:"-"`
	MaxCumulativeBytes int64 `json:"-"`
	MaxRequests        int64 `json:"-"`
	MaxInFlight        int64 `json:"-"`
}

type AttemptSourceBinding struct {
	SessionID           string               `json:"-"`
	Ref                 backupasset.AssetRef `json:"-"`
	CatalogGenerationID string               `json:"-"`
	SourceFingerprint   string               `json:"-"`
	EntryFingerprint    string               `json:"-"`
	AllowedModes        []SourceMode         `json:"-"`
	Limits              AttemptReadLimits    `json:"-"`
	AbsoluteExpiresAt   time.Time            `json:"-"`
}

type AttemptSourceInfo struct {
	Size              int64  `json:"-"`
	MediaType         string `json:"-"`
	FingerprintStrong bool   `json:"-"`
	Sequential        bool   `json:"-"`
	Range             bool   `json:"-"`
}

type AttemptReadIntent struct {
	SessionID string     `json:"-"`
	Mode      SourceMode `json:"-"`
	Offset    *int64     `json:"-"`
	Length    *int64     `json:"-"`
	Bytes     int64      `json:"-"`
}

type AttemptReadReservation struct {
	ID            string `json:"-"`
	ReservedBytes int64  `json:"-"`
}

type AttemptReadFinalization struct {
	ReservationID string `json:"-"`
	ReservedBytes int64  `json:"-"`
	ProviderBytes int64  `json:"-"`
	EvidenceKnown bool   `json:"-"`
	Succeeded     bool   `json:"-"`
}

type AttemptBudget interface {
	ReserveAttemptRead(context.Context, AttemptReadIntent) (AttemptReadReservation, error)
	FinalizeAttemptRead(context.Context, AttemptReadFinalization) error
}

type AttemptReadHandle interface {
	io.ReadCloser
}

type AttemptInputSession interface {
	Info() AttemptSourceInfo
	OpenSequential(context.Context, int64) (AttemptReadHandle, error)
	OpenRange(context.Context, int64, int64) (AttemptReadHandle, error)
	Revalidate(context.Context) error
	Close() error
}

type AttemptBroker struct {
	source SourceResolver
	budget AttemptBudget
	now    func() time.Time
}

func NewAttemptBroker(source SourceResolver, budget AttemptBudget, now func() time.Time) (*AttemptBroker, error) {
	if source == nil || budget == nil {
		return nil, ErrInvalidAttemptBinding
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &AttemptBroker{source: source, budget: budget, now: now}, nil
}

func (broker *AttemptBroker) OpenSession(ctx context.Context, binding AttemptSourceBinding) (AttemptInputSession, AttemptSourceInfo, error) {
	if broker == nil || !validAttemptSourceBinding(binding) || !broker.now().UTC().Before(binding.AbsoluteExpiresAt.UTC()) {
		return nil, AttemptSourceInfo{}, ErrAttemptSessionDenied
	}
	session := &attemptInputSession{
		broker: broker, binding: cloneAttemptBinding(binding), allowed: attemptModeSet(binding.AllowedModes),
		active: make(map[*attemptReadHandle]struct{}),
	}
	info, err := session.stat(ctx)
	if err != nil {
		return nil, AttemptSourceInfo{}, err
	}
	session.info = info
	return session, info, nil
}

type attemptInputSession struct {
	broker  *AttemptBroker
	binding AttemptSourceBinding
	allowed map[SourceMode]bool
	info    AttemptSourceInfo

	mu     sync.Mutex
	closed bool
	active map[*attemptReadHandle]struct{}
}

func (session *attemptInputSession) Info() AttemptSourceInfo {
	if session == nil {
		return AttemptSourceInfo{}
	}
	return session.info
}

func (session *attemptInputSession) OpenSequential(ctx context.Context, maxBytes int64) (AttemptReadHandle, error) {
	return session.openRead(ctx, SourceModeSequential, 0, maxBytes)
}

func (session *attemptInputSession) OpenRange(ctx context.Context, offset, length int64) (AttemptReadHandle, error) {
	return session.openRead(ctx, SourceModeRange, offset, length)
}

func (session *attemptInputSession) Revalidate(ctx context.Context) error {
	_, err := session.stat(ctx)
	return err
}

func (session *attemptInputSession) Close() error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil
	}
	session.closed = true
	active := make([]*attemptReadHandle, 0, len(session.active))
	for read := range session.active {
		active = append(active, read)
	}
	session.mu.Unlock()
	var result error
	for _, read := range active {
		result = errors.Join(result, read.Close())
	}
	return result
}

func (session *attemptInputSession) stat(ctx context.Context) (AttemptSourceInfo, error) {
	if err := session.admit(SourceModeStat, 0, 0); err != nil {
		return AttemptSourceInfo{}, err
	}
	reservation, err := session.broker.budget.ReserveAttemptRead(ctx, AttemptReadIntent{SessionID: session.binding.SessionID, Mode: SourceModeStat})
	if err != nil {
		return AttemptSourceInfo{}, classifyAttemptBudgetError(err)
	}
	finalization := AttemptReadFinalization{ReservationID: reservation.ID, ReservedBytes: reservation.ReservedBytes}
	source, err := session.broker.source.OpenContentSource(ctx, session.sourceRequest(SourceModeStat, 0, 0))
	if err != nil {
		_ = session.broker.budget.FinalizeAttemptRead(withoutCancel(ctx), finalization)
		return AttemptSourceInfo{}, ErrAttemptSourceFailed
	}
	stat := source.Stat()
	capabilities := source.Capabilities()
	valid := session.sourceMatches(stat, capabilities, SourceModeStat) && source.Revalidate(ctx) == nil
	closeErr := source.Close()
	finalization.EvidenceKnown = valid && closeErr == nil
	finalization.Succeeded = finalization.EvidenceKnown
	finalization.ProviderBytes = 0
	finalizeErr := session.broker.budget.FinalizeAttemptRead(withoutCancel(ctx), finalization)
	if !valid {
		return AttemptSourceInfo{}, errors.Join(ErrAttemptSourceChanged, finalizeErr)
	}
	if closeErr != nil || finalizeErr != nil {
		return AttemptSourceInfo{}, errors.Join(ErrAttemptSourceFailed, closeErr, finalizeErr)
	}
	return AttemptSourceInfo{
		Size: stat.Size, MediaType: stat.MediaType, FingerprintStrong: stat.FingerprintStrong,
		Sequential: capabilities.Sequential && session.allowed[SourceModeSequential],
		Range:      capabilities.Range && session.allowed[SourceModeRange],
	}, nil
}

func (session *attemptInputSession) openRead(ctx context.Context, mode SourceMode, offset, length int64) (AttemptReadHandle, error) {
	if err := session.admit(mode, offset, length); err != nil {
		return nil, err
	}
	intent := AttemptReadIntent{SessionID: session.binding.SessionID, Mode: mode, Bytes: length}
	if mode == SourceModeRange {
		intent.Offset, intent.Length = int64Pointer(offset), int64Pointer(length)
	}
	reservation, err := session.broker.budget.ReserveAttemptRead(ctx, intent)
	if err != nil {
		return nil, classifyAttemptBudgetError(err)
	}
	finalization := AttemptReadFinalization{ReservationID: reservation.ID, ReservedBytes: reservation.ReservedBytes}
	source, err := session.broker.source.OpenContentSource(ctx, session.sourceRequest(mode, offset, length))
	if err != nil {
		_ = session.broker.budget.FinalizeAttemptRead(withoutCancel(ctx), finalization)
		return nil, ErrAttemptSourceFailed
	}
	stat := source.Stat()
	capabilities := source.Capabilities()
	if !session.sourceMatches(stat, capabilities, mode) || source.Revalidate(ctx) != nil {
		_ = source.Close()
		_ = session.broker.budget.FinalizeAttemptRead(withoutCancel(ctx), finalization)
		return nil, ErrAttemptSourceChanged
	}
	reader := source.Reader()
	if reader == nil {
		_ = source.Close()
		_ = session.broker.budget.FinalizeAttemptRead(withoutCancel(ctx), finalization)
		return nil, ErrAttemptSourceFailed
	}
	read := &attemptReadHandle{
		ctx: ctx, session: session, source: source, reader: reader, budget: session.broker.budget,
		reservation: reservation, remaining: length, done: make(chan struct{}),
	}
	session.mu.Lock()
	if session.closed || !session.broker.now().UTC().Before(session.binding.AbsoluteExpiresAt.UTC()) {
		session.mu.Unlock()
		_ = read.closeInternal(false)
		return nil, ErrAttemptSessionDenied
	}
	session.active[read] = struct{}{}
	session.mu.Unlock()
	go read.closeOnCancellation()
	return read, nil
}

func (session *attemptInputSession) sourceRequest(mode SourceMode, offset, length int64) SourceRequest {
	request := SourceRequest{
		Ref: session.binding.Ref, CatalogGenerationID: session.binding.CatalogGenerationID,
		ExpectedSource: session.binding.SourceFingerprint, ExpectedEntry: session.binding.EntryFingerprint, Mode: mode,
	}
	if mode == SourceModeSequential {
		request.MaxBytes = length
	}
	if mode == SourceModeRange {
		request.MaxBytes = length
		request.Range = &ResolvedRange{Offset: offset, Length: length}
	}
	return request
}

func (session *attemptInputSession) sourceMatches(stat SourceStat, capabilities SourceCapabilities, mode SourceMode) bool {
	if stat.Size < 0 || strings.TrimSpace(stat.MediaType) == "" || stat.SourceFingerprint != session.binding.SourceFingerprint ||
		stat.EntryFingerprint != session.binding.EntryFingerprint {
		return false
	}
	switch mode {
	case SourceModeStat:
		return true
	case SourceModeSequential:
		return capabilities.Sequential
	case SourceModeRange:
		return capabilities.Range
	default:
		return false
	}
}

func (session *attemptInputSession) admit(mode SourceMode, offset, length int64) error {
	if session == nil || session.broker == nil {
		return ErrAttemptSessionDenied
	}
	session.mu.Lock()
	closed := session.closed
	session.mu.Unlock()
	if closed || !session.broker.now().UTC().Before(session.binding.AbsoluteExpiresAt.UTC()) || !session.allowed[mode] {
		return ErrAttemptSessionDenied
	}
	if mode == SourceModeStat {
		if offset != 0 || length != 0 {
			return ErrAttemptSessionDenied
		}
		return nil
	}
	if length <= 0 || length > session.binding.Limits.MaxBytesPerRequest || offset < 0 || offset > math.MaxInt64-length {
		return ErrAttemptBudgetExceeded
	}
	if mode == SourceModeSequential && offset != 0 {
		return ErrAttemptSessionDenied
	}
	return nil
}

type attemptReadHandle struct {
	ctx         context.Context
	session     *attemptInputSession
	source      SourceSession
	reader      SourceReader
	budget      AttemptBudget
	reservation AttemptReadReservation

	mu        sync.Mutex
	remaining int64
	readErr   error
	closeErr  error
	closed    bool
	done      chan struct{}
	once      sync.Once
}

func (read *attemptReadHandle) Read(payload []byte) (int, error) {
	read.mu.Lock()
	if read.closed {
		read.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if read.remaining == 0 {
		read.mu.Unlock()
		return 0, io.EOF
	}
	if int64(len(payload)) > read.remaining {
		payload = payload[:read.remaining]
	}
	reader := read.reader
	read.mu.Unlock()

	count, err := reader.Read(payload)
	read.mu.Lock()
	read.remaining -= int64(count)
	if err != nil && !errors.Is(err, io.EOF) {
		read.readErr = err
	}
	read.mu.Unlock()
	return count, err
}

func (read *attemptReadHandle) Close() error {
	return read.closeInternal(true)
}

func (read *attemptReadHandle) closeInternal(callerInitiated bool) error {
	read.once.Do(func() {
		read.mu.Lock()
		read.closed = true
		readErr := read.readErr
		fullyRead := read.remaining == 0
		read.mu.Unlock()
		readerErr := read.reader.Close()
		revalidateErr := read.source.Revalidate(withoutCancel(read.ctx))
		sourceErr := read.source.Close()
		providerBytes := read.reader.ProviderBytes()
		evidenceKnown := readErr == nil && readerErr == nil && revalidateErr == nil && sourceErr == nil && providerBytes >= 0 &&
			providerBytes <= read.reservation.ReservedBytes
		finalization := AttemptReadFinalization{
			ReservationID: read.reservation.ID, ReservedBytes: read.reservation.ReservedBytes,
			ProviderBytes: providerBytes, EvidenceKnown: evidenceKnown, Succeeded: evidenceKnown && callerInitiated && fullyRead,
		}
		finalizeErr := read.budget.FinalizeAttemptRead(withoutCancel(read.ctx), finalization)
		switch {
		case revalidateErr != nil:
			read.closeErr = errors.Join(ErrAttemptSourceChanged, finalizeErr)
		case !evidenceKnown:
			read.closeErr = errors.Join(ErrAttemptSourceFailed, readErr, readerErr, sourceErr, finalizeErr)
		default:
			read.closeErr = finalizeErr
		}
		read.session.mu.Lock()
		delete(read.session.active, read)
		read.session.mu.Unlock()
		close(read.done)
	})
	<-read.done
	return read.closeErr
}

func (read *attemptReadHandle) closeOnCancellation() {
	select {
	case <-read.ctx.Done():
		_ = read.closeInternal(false)
	case <-read.done:
	}
}

func validAttemptSourceBinding(binding AttemptSourceBinding) bool {
	if backupasset.ValidateOpaqueID(binding.SessionID) != nil || backupasset.ValidateAssetRef(binding.Ref) != nil ||
		backupasset.ValidateOpaqueID(binding.CatalogGenerationID) != nil || strings.TrimSpace(binding.SourceFingerprint) == "" ||
		len(binding.SourceFingerprint) > 128 || len(binding.EntryFingerprint) > 128 || binding.AbsoluteExpiresAt.IsZero() ||
		binding.Limits.MaxBytesPerRequest <= 0 || binding.Limits.MaxCumulativeBytes < binding.Limits.MaxBytesPerRequest ||
		binding.Limits.MaxRequests <= 0 || binding.Limits.MaxInFlight <= 0 || len(binding.AllowedModes) == 0 || len(binding.AllowedModes) > 3 {
		return false
	}
	seen := make(map[SourceMode]bool, len(binding.AllowedModes))
	for _, mode := range binding.AllowedModes {
		if mode != SourceModeStat && mode != SourceModeSequential && mode != SourceModeRange || seen[mode] {
			return false
		}
		seen[mode] = true
	}
	return seen[SourceModeStat]
}

func cloneAttemptBinding(binding AttemptSourceBinding) AttemptSourceBinding {
	binding.AllowedModes = append([]SourceMode(nil), binding.AllowedModes...)
	binding.AbsoluteExpiresAt = binding.AbsoluteExpiresAt.UTC()
	return binding
}

func attemptModeSet(modes []SourceMode) map[SourceMode]bool {
	result := make(map[SourceMode]bool, len(modes))
	for _, mode := range modes {
		result[mode] = true
	}
	return result
}

func classifyAttemptBudgetError(err error) error {
	if errors.Is(err, ErrAttemptBudgetExceeded) {
		return err
	}
	return errors.Join(ErrAttemptBudgetExceeded, err)
}

func withoutCancel(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

func int64Pointer(value int64) *int64 { return &value }

var _ AttemptInputSession = (*attemptInputSession)(nil)
var _ AttemptReadHandle = (*attemptReadHandle)(nil)
