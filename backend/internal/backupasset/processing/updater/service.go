package updater

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type OnlineConfig struct {
	Enabled          bool
	AllowedOrigins   []string
	ProxyURL         string
	CredentialFile   string
	RequestTimeout   time.Duration
	MetadataMaxBytes int64
	BundleMaxBytes   int64
}

type OnlineFetcher struct {
	enabled          bool
	allowedOrigins   map[string]bool
	credential       []byte
	httpClient       *http.Client
	metadataMaxBytes int64
	bundleMaxBytes   int64
}

func NewOnlineFetcher(config OnlineConfig) (*OnlineFetcher, error) {
	if !config.Enabled {
		return &OnlineFetcher{}, nil
	}
	credential, err := readOnlineCredential(config.CredentialFile)
	if err != nil {
		return nil, err
	}
	proxy, err := validateOnlineConfig(config)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxy), DisableCompression: true, ForceAttemptHTTP2: false,
		ResponseHeaderTimeout: config.RequestTimeout, IdleConnTimeout: config.RequestTimeout,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13},
	}
	return newOnlineFetcher(config, credential, &http.Client{Transport: transport, Timeout: config.RequestTimeout})
}

func newOnlineFetcher(config OnlineConfig, credential []byte, client *http.Client) (*OnlineFetcher, error) {
	if !config.Enabled || client == nil || len(credential) > 4096 || bytes.ContainsAny(credential, "\x00\r\n") {
		return nil, ErrPolicyRejected
	}
	if _, err := validateOnlineConfig(config); err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(config.AllowedOrigins))
	for _, origin := range config.AllowedOrigins {
		allowed[origin] = true
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return ErrPolicyRejected }
	return &OnlineFetcher{
		enabled: true, allowedOrigins: allowed, credential: append([]byte(nil), credential...), httpClient: &copyClient,
		metadataMaxBytes: config.MetadataMaxBytes, bundleMaxBytes: config.BundleMaxBytes,
	}, nil
}

func validateOnlineConfig(config OnlineConfig) (*url.URL, error) {
	if !config.Enabled || len(config.AllowedOrigins) == 0 || len(config.AllowedOrigins) > 32 ||
		config.RequestTimeout <= 0 || config.RequestTimeout > 15*time.Minute ||
		config.MetadataMaxBytes <= 0 || config.MetadataMaxBytes > maximumManifestBytes ||
		config.BundleMaxBytes <= 0 || config.BundleMaxBytes > maximumBundleBytes {
		return nil, ErrPolicyRejected
	}
	lastOrigin := ""
	for _, origin := range config.AllowedOrigins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" || parsed.Port() == "" ||
			parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
			parsed.String() != origin || strings.ToLower(parsed.Host) != parsed.Host || origin <= lastOrigin {
			return nil, ErrPolicyRejected
		}
		if _, err := strconv.ParseUint(parsed.Port(), 10, 16); err != nil || !validOnlineHostname(parsed.Hostname()) {
			return nil, ErrPolicyRejected
		}
		lastOrigin = origin
	}
	proxy, err := url.Parse(config.ProxyURL)
	if err != nil || proxy.Scheme != "http" && proxy.Scheme != "https" || proxy.User != nil || proxy.Host == "" || proxy.Port() == "" ||
		proxy.Path != "" || proxy.RawPath != "" || proxy.RawQuery != "" || proxy.Fragment != "" || proxy.String() != config.ProxyURL {
		return nil, ErrPolicyRejected
	}
	proxyIP := net.ParseIP(proxy.Hostname())
	if proxyIP == nil || !proxyIP.IsPrivate() && !proxyIP.IsLoopback() || proxyIP.IsUnspecified() || proxyIP.IsMulticast() {
		return nil, ErrPolicyRejected
	}
	if _, err := strconv.ParseUint(proxy.Port(), 10, 16); err != nil {
		return nil, ErrPolicyRejected
	}
	return proxy, nil
}

func validOnlineHostname(value string) bool {
	if value == "" || len(value) > 253 || strings.ToLower(value) != value || strings.ContainsAny(value, "*/:@/?#\\\x00") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func readOnlineCredential(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return nil, ErrPolicyRejected
	}
	pre, err := os.Lstat(path)
	if err != nil || !pre.Mode().IsRegular() || pre.Mode().Perm()&0o077 != 0 || pre.Size() <= 0 || pre.Size() > 4096 {
		return nil, ErrPolicyRejected
	}
	handle, err := os.Open(path)
	if err != nil {
		return nil, ErrPolicyRejected
	}
	payload, readErr := io.ReadAll(io.LimitReader(handle, 4097))
	opened, statErr := handle.Stat()
	closeErr := handle.Close()
	post, postErr := os.Lstat(path)
	if readErr != nil || statErr != nil || closeErr != nil || postErr != nil || len(payload) == 0 || len(payload) > 4096 ||
		bytes.ContainsAny(payload, "\x00\r\n") || !os.SameFile(pre, opened) || !os.SameFile(opened, post) {
		return nil, ErrPolicyRejected
	}
	return payload, nil
}

func (fetcher *OnlineFetcher) FetchMetadata(ctx context.Context, rawURL string) ([]byte, error) {
	return fetcher.fetch(ctx, rawURL, fetcher.metadataMaxBytes, "application/json")
}

func (fetcher *OnlineFetcher) FetchBundle(ctx context.Context, rawURL string) ([]byte, error) {
	return fetcher.fetch(ctx, rawURL, fetcher.bundleMaxBytes, "application/octet-stream")
}

func (fetcher *OnlineFetcher) fetch(ctx context.Context, rawURL string, maximum int64, accept string) ([]byte, error) {
	if fetcher == nil || !fetcher.enabled || fetcher.httpClient == nil || ctx == nil || maximum <= 0 {
		return nil, ErrPolicyRejected
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Path == "" || filepath.ToSlash(filepath.Clean(parsed.Path)) != parsed.Path || parsed.String() != rawURL ||
		!fetcher.allowedOrigins[parsed.Scheme+"://"+parsed.Host] {
		return nil, ErrPolicyRejected
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, ErrPolicyRejected
	}
	request.Header.Set("Accept", accept)
	if len(fetcher.credential) > 0 {
		request.Header.Set("Authorization", "Bearer "+string(fetcher.credential))
	}
	response, err := fetcher.httpClient.Do(request)
	if err != nil {
		return nil, ErrTemporarilyUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, ErrPolicyRejected
	}
	if response.StatusCode != http.StatusOK {
		return nil, ErrTemporarilyUnavailable
	}
	if response.ContentLength > maximum {
		return nil, ErrPolicyRejected
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil || int64(len(payload)) > maximum {
		return nil, ErrPolicyRejected
	}
	return payload, nil
}

type ProtocolClient interface {
	RegisterCandidate(context.Context, RegisterCandidateRequest) (RegisterCandidateResult, error)
	PullActivation(context.Context, PullActivationRequest) (PullActivationResult, error)
	ReportActivation(context.Context, ActivationReportRequest) (ActivationReportResult, error)
}

type Service struct {
	inbox       *Inbox
	store       *Store
	activator   *Activator
	client      ProtocolClient
	journalPath string
	replayPath  string
	mu          sync.Mutex
}

type RegisteredCandidateReceipt struct {
	CandidateID string `json:"candidate_id"`
	InboxReceipt
}

type candidateJournal struct {
	SchemaVersion int                     `json:"schema_version"`
	Candidates    []candidateJournalEntry `json:"candidates"`
}

type candidateJournalEntry struct {
	CandidateID       string `json:"candidate_id"`
	BundleFingerprint string `json:"bundle_fingerprint"`
}

type candidateReplayState struct {
	SchemaVersion int                    `json:"schema_version"`
	Receipts      []candidateReplayEntry `json:"receipts"`
}

type candidateReplayEntry struct {
	CandidateID string       `json:"candidate_id"`
	Receipt     InboxReceipt `json:"receipt"`
}

const maximumCandidateReplayBytes = 64 << 20

func NewService(inbox *Inbox, store *Store, activator *Activator, client ProtocolClient, journalPath string) (*Service, error) {
	if store == nil || activator == nil || client == nil || !filepath.IsAbs(journalPath) || filepath.Clean(journalPath) != journalPath ||
		filepath.Base(journalPath) != "candidate-journal.json" || strings.ContainsAny(journalPath, "\x00\r\n") {
		return nil, ErrActivationFailed
	}
	parentInfo, err := os.Lstat(filepath.Dir(journalPath))
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm() != sharedStoreRootMode {
		return nil, ErrActivationFailed
	}
	service := &Service{
		inbox: inbox, store: store, activator: activator, client: client, journalPath: journalPath,
		replayPath: filepath.Join(filepath.Dir(journalPath), "candidate-receipts.json"),
	}
	if _, err := os.Lstat(journalPath); errors.Is(err, os.ErrNotExist) {
		if err := service.writeCandidateJournal(candidateJournal{SchemaVersion: 1, Candidates: []candidateJournalEntry{}}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, ErrActivationFailed
	} else if _, err := service.readCandidateJournal(); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(service.replayPath); errors.Is(err, os.ErrNotExist) {
		if err := service.writeCandidateReplayState(candidateReplayState{SchemaVersion: 1, Receipts: []candidateReplayEntry{}}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, ErrActivationFailed
	} else if _, err := service.readCandidateReplayState(); err != nil {
		return nil, err
	}
	return service, nil
}

func (service *Service) ScanAndRegister(ctx context.Context, trust TrustStore, now time.Time) ([]RegisteredCandidateReceipt, error) {
	if service == nil || service.inbox == nil || ctx == nil || now.Location() != time.UTC {
		return nil, ErrPolicyRejected
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.scanAndRegisterLocked(ctx, trust, now)
}

func (service *Service) scanAndRegisterLocked(
	ctx context.Context,
	trust TrustStore,
	now time.Time,
) ([]RegisteredCandidateReceipt, error) {
	candidates, err := service.inbox.Scan(ctx, trust, now)
	if err != nil {
		return nil, err
	}
	journal, err := service.readCandidateJournal()
	if err != nil {
		return nil, err
	}
	replay, err := service.readCandidateReplayState()
	if err != nil {
		return nil, err
	}
	registered := make([]RegisteredCandidateReceipt, 0, len(candidates))
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := service.store.StoreBundle(ctx, candidate.Bundle); err != nil {
			return nil, err
		}
		result, err := service.client.RegisterCandidate(ctx, RegisterCandidateRequest{SchemaVersion: 1, Receipt: candidate.Receipt})
		if err != nil || ValidateRegisterCandidateResult(result) != nil {
			if err != nil {
				return nil, err
			}
			return nil, ErrProtocolInvalid
		}
		if err := addCandidateJournalEntry(&journal, candidateJournalEntry{
			CandidateID: result.CandidateID, BundleFingerprint: candidate.Receipt.BundleFingerprint,
		}); err != nil {
			return nil, err
		}
		if err := addCandidateReplayEntry(&replay, candidateReplayEntry{CandidateID: result.CandidateID, Receipt: candidate.Receipt}); err != nil {
			return nil, err
		}
		registered = append(registered, RegisteredCandidateReceipt{CandidateID: result.CandidateID, InboxReceipt: candidate.Receipt})
	}
	if err := service.writeCandidateReplayState(replay); err != nil {
		return nil, err
	}
	if err := service.writeCandidateJournal(journal); err != nil {
		return nil, err
	}
	return registered, nil
}

func (service *Service) PollAndActivate(ctx context.Context) (int, error) {
	if service == nil || ctx == nil {
		return 0, ErrActivationFailed
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.pollAndActivateLocked(ctx, TrustStore{}, time.Time{}, false)
}

func (service *Service) PollScanAndActivate(
	ctx context.Context,
	trust TrustStore,
	now time.Time,
) (int, error) {
	if service == nil || service.inbox == nil || ctx == nil || now.Location() != time.UTC {
		return 0, ErrPolicyRejected
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.pollAndActivateLocked(ctx, trust, now, true)
}

func (service *Service) pollAndActivateLocked(
	ctx context.Context,
	trust TrustStore,
	now time.Time,
	allowScan bool,
) (int, error) {
	if err := service.replayCandidateReceiptsLocked(ctx); err != nil {
		return 0, err
	}
	if err := service.recoverPendingActivationLocked(ctx); err != nil {
		return 0, err
	}
	active, err := service.activator.ActiveFingerprint()
	if err != nil {
		return 0, err
	}
	pulled, err := service.client.PullActivation(ctx, PullActivationRequest{SchemaVersion: 1, ActiveFingerprint: active})
	if err != nil || ValidatePullActivationResult(pulled) != nil {
		if err != nil {
			return 0, err
		}
		return 0, ErrProtocolInvalid
	}
	if pulled.ScanRequested {
		if !allowScan {
			return 0, ErrPolicyRejected
		}
		if _, err := service.scanAndRegisterLocked(ctx, trust, now); err != nil {
			return 0, err
		}
	}
	if pulled.Directive == nil {
		return pulled.RetryAfterSeconds, nil
	}
	directive := *pulled.Directive
	if directive.ExpectedOldFingerprint != active {
		return 0, ErrActivationFailed
	}
	journal, err := service.readCandidateJournal()
	if err != nil {
		return 0, err
	}
	if !journalContainsCandidate(journal, directive.CandidateID, directive.NewFingerprint) {
		return 0, ErrActivationFailed
	}
	receipt, err := service.activator.Activate(ctx, ActivationRequest{
		CandidateID: directive.CandidateID, ExpectedOldFingerprint: directive.ExpectedOldFingerprint,
		NewFingerprint: directive.NewFingerprint,
	})
	if err != nil {
		return 0, err
	}
	if err := service.reportAndFinalizeActivationLocked(ctx, receipt); err != nil {
		return 0, err
	}
	return pulled.RetryAfterSeconds, nil
}

func (service *Service) replayCandidateReceiptsLocked(ctx context.Context) error {
	replay, err := service.readCandidateReplayState()
	if err != nil {
		return err
	}
	for _, entry := range replay.Receipts {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := service.client.RegisterCandidate(ctx, RegisterCandidateRequest{SchemaVersion: 1, Receipt: entry.Receipt})
		if err != nil {
			return err
		}
		if ValidateRegisterCandidateResult(result) != nil || result.CandidateID != entry.CandidateID {
			return ErrProtocolInvalid
		}
	}
	return nil
}

func (service *Service) recoverPendingActivationLocked(ctx context.Context) error {
	journal, err := service.activator.readJournal()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	active, err := service.activator.ActiveFingerprint()
	if err != nil {
		return err
	}
	if active == journal.OldFingerprint {
		return service.activator.Recover(ctx, journal.OldFingerprint)
	}
	if active != journal.NewFingerprint {
		return ErrActivationFailed
	}
	return service.reportAndFinalizeActivationLocked(ctx, ActivationReceipt{
		SchemaVersion: 1, CandidateID: journal.CandidateID,
		OldFingerprint: journal.OldFingerprint, NewFingerprint: journal.NewFingerprint, State: "swapped",
	})
}

func (service *Service) reportAndFinalizeActivationLocked(ctx context.Context, receipt ActivationReceipt) error {
	reported, err := service.client.ReportActivation(ctx, ActivationReportRequest{SchemaVersion: 1, Receipt: receipt})
	if err != nil || ValidateActivationReportResult(reported) != nil {
		if err != nil {
			return err
		}
		return ErrProtocolInvalid
	}
	target := receipt.OldFingerprint
	if reported.Decision == ActivationDecisionCommit {
		target = receipt.NewFingerprint
	}
	if reported.ActiveFingerprint != target {
		return ErrProtocolInvalid
	}
	if err := service.activator.Recover(ctx, target); err != nil {
		return err
	}
	replay, err := service.readCandidateReplayState()
	if err != nil {
		return err
	}
	if reported.Decision == ActivationDecisionCommit {
		removeCandidateReplayFingerprint(&replay, receipt.OldFingerprint)
	} else {
		removeCandidateReplayEntry(&replay, receipt.CandidateID)
	}
	if err := service.writeCandidateReplayState(replay); err != nil {
		return err
	}
	journal, err := service.readCandidateJournal()
	if err != nil {
		return err
	}
	removeCandidateJournalEntry(&journal, receipt.CandidateID)
	if err := service.writeCandidateJournal(journal); err != nil {
		return err
	}
	return nil
}

func addCandidateJournalEntry(journal *candidateJournal, entry candidateJournalEntry) error {
	if journal == nil || !lowerHex(entry.CandidateID, 32) || !lowerHex(entry.BundleFingerprint, 64) {
		return ErrActivationFailed
	}
	for _, existing := range journal.Candidates {
		if existing.CandidateID == entry.CandidateID || existing.BundleFingerprint == entry.BundleFingerprint {
			if existing == entry {
				return nil
			}
			return ErrActivationFailed
		}
	}
	journal.Candidates = append(journal.Candidates, entry)
	sort.Slice(journal.Candidates, func(left, right int) bool {
		return journal.Candidates[left].CandidateID < journal.Candidates[right].CandidateID
	})
	return nil
}

func journalContainsCandidate(journal candidateJournal, candidateID, fingerprint string) bool {
	for _, entry := range journal.Candidates {
		if entry.CandidateID == candidateID && entry.BundleFingerprint == fingerprint {
			return true
		}
	}
	return false
}

func removeCandidateJournalEntry(journal *candidateJournal, candidateID string) {
	for index, entry := range journal.Candidates {
		if entry.CandidateID == candidateID {
			journal.Candidates = append(journal.Candidates[:index], journal.Candidates[index+1:]...)
			return
		}
	}
}

func addCandidateReplayEntry(state *candidateReplayState, entry candidateReplayEntry) error {
	if state == nil || !lowerHex(entry.CandidateID, 32) || validateInboxReceipt(entry.Receipt) != nil {
		return ErrActivationFailed
	}
	for _, existing := range state.Receipts {
		if existing.CandidateID == entry.CandidateID || existing.Receipt.BundleFingerprint == entry.Receipt.BundleFingerprint {
			existingPayload, _ := json.Marshal(existing)
			entryPayload, _ := json.Marshal(entry)
			if bytes.Equal(existingPayload, entryPayload) {
				return nil
			}
			return ErrActivationFailed
		}
	}
	state.Receipts = append(state.Receipts, entry)
	sort.Slice(state.Receipts, func(left, right int) bool {
		return state.Receipts[left].CandidateID < state.Receipts[right].CandidateID
	})
	return nil
}

func removeCandidateReplayEntry(state *candidateReplayState, candidateID string) {
	for index, entry := range state.Receipts {
		if entry.CandidateID == candidateID {
			state.Receipts = append(state.Receipts[:index], state.Receipts[index+1:]...)
			return
		}
	}
}

func removeCandidateReplayFingerprint(state *candidateReplayState, fingerprint string) {
	if fingerprint == "" {
		return
	}
	for index, entry := range state.Receipts {
		if entry.Receipt.BundleFingerprint == fingerprint {
			state.Receipts = append(state.Receipts[:index], state.Receipts[index+1:]...)
			return
		}
	}
}

func (service *Service) readCandidateJournal() (candidateJournal, error) {
	info, err := os.Lstat(service.journalPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > maximumProtocolBytes {
		return candidateJournal{}, ErrActivationFailed
	}
	payload, err := os.ReadFile(service.journalPath)
	if err != nil || rejectDuplicateJSONMembers(payload) != nil {
		return candidateJournal{}, ErrActivationFailed
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var journal candidateJournal
	if decoder.Decode(&journal) != nil || ensureJSONEOF(decoder) != nil || validateCandidateJournal(journal) != nil {
		return candidateJournal{}, ErrActivationFailed
	}
	canonical, err := json.Marshal(journal)
	if err != nil || !bytes.Equal(canonical, payload) {
		return candidateJournal{}, ErrActivationFailed
	}
	return journal, nil
}

func validateCandidateJournal(journal candidateJournal) error {
	if journal.SchemaVersion != 1 || journal.Candidates == nil || len(journal.Candidates) > 100_000 {
		return ErrActivationFailed
	}
	lastID := ""
	fingerprints := make(map[string]bool, len(journal.Candidates))
	for _, entry := range journal.Candidates {
		if !lowerHex(entry.CandidateID, 32) || entry.CandidateID <= lastID || !lowerHex(entry.BundleFingerprint, 64) ||
			fingerprints[entry.BundleFingerprint] {
			return ErrActivationFailed
		}
		lastID = entry.CandidateID
		fingerprints[entry.BundleFingerprint] = true
	}
	return nil
}

func (service *Service) writeCandidateJournal(journal candidateJournal) error {
	if validateCandidateJournal(journal) != nil {
		return ErrActivationFailed
	}
	payload, err := json.Marshal(journal)
	if err != nil || len(payload) > maximumProtocolBytes {
		return ErrActivationFailed
	}
	temporary, err := privateTemporaryPath(filepath.Dir(service.journalPath), ".candidate-journal-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporary)
		}
	}()
	handle, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrActivationFailed
	}
	written, writeErr := handle.Write(payload)
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || written != len(payload) {
		return ErrActivationFailed
	}
	if err := os.Rename(temporary, service.journalPath); err != nil {
		return ErrActivationFailed
	}
	cleanup = false
	if err := syncDirectory(filepath.Dir(service.journalPath)); err != nil {
		return ErrActivationFailed
	}
	return nil
}

func (service *Service) readCandidateReplayState() (candidateReplayState, error) {
	info, err := os.Lstat(service.replayPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		info.Size() <= 0 || info.Size() > maximumCandidateReplayBytes {
		return candidateReplayState{}, ErrActivationFailed
	}
	payload, err := os.ReadFile(service.replayPath)
	if err != nil || rejectDuplicateJSONMembers(payload) != nil {
		return candidateReplayState{}, ErrActivationFailed
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var state candidateReplayState
	if decoder.Decode(&state) != nil || ensureJSONEOF(decoder) != nil || validateCandidateReplayState(state) != nil {
		return candidateReplayState{}, ErrActivationFailed
	}
	canonical, err := json.Marshal(state)
	if err != nil || !bytes.Equal(canonical, payload) {
		return candidateReplayState{}, ErrActivationFailed
	}
	return state, nil
}

func validateCandidateReplayState(state candidateReplayState) error {
	if state.SchemaVersion != 1 || state.Receipts == nil || len(state.Receipts) > 1024 {
		return ErrActivationFailed
	}
	lastID := ""
	fingerprints := make(map[string]bool, len(state.Receipts))
	for _, entry := range state.Receipts {
		if !lowerHex(entry.CandidateID, 32) || entry.CandidateID <= lastID || validateInboxReceipt(entry.Receipt) != nil ||
			fingerprints[entry.Receipt.BundleFingerprint] {
			return ErrActivationFailed
		}
		lastID = entry.CandidateID
		fingerprints[entry.Receipt.BundleFingerprint] = true
	}
	return nil
}

func (service *Service) writeCandidateReplayState(state candidateReplayState) error {
	if validateCandidateReplayState(state) != nil {
		return ErrActivationFailed
	}
	payload, err := json.Marshal(state)
	if err != nil || len(payload) > maximumCandidateReplayBytes {
		return ErrActivationFailed
	}
	temporary, err := privateTemporaryPath(filepath.Dir(service.replayPath), ".candidate-receipts-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporary)
		}
	}()
	handle, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrActivationFailed
	}
	written, writeErr := handle.Write(payload)
	syncErr := handle.Sync()
	closeErr := handle.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || written != len(payload) {
		return ErrActivationFailed
	}
	if err := os.Rename(temporary, service.replayPath); err != nil {
		return ErrActivationFailed
	}
	cleanup = false
	if err := syncDirectory(filepath.Dir(service.replayPath)); err != nil {
		return ErrActivationFailed
	}
	return nil
}
