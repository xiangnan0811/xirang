package updater

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOnlineFetcherProductionTransportRequiresTLS13(t *testing.T) {
	config := OnlineConfig{
		Enabled: true, AllowedOrigins: []string{"https://updates.example.test:443"},
		ProxyURL: "http://127.0.0.1:8888", RequestTimeout: time.Minute,
		MetadataMaxBytes: 1024, BundleMaxBytes: 1 << 20,
	}
	fetcher, err := NewOnlineFetcher(config)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := fetcher.httpClient.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("production updater transport does not require TLS 1.3: %#v", fetcher.httpClient.Transport)
	}
}

func TestOnlineFetcherRequiresExternalProxyAndExactHTTPSOrigin(t *testing.T) {
	base := OnlineConfig{
		Enabled: true, AllowedOrigins: []string{"https://updates.example.test:443"},
		ProxyURL: "http://127.0.0.1:8888", RequestTimeout: time.Minute,
		MetadataMaxBytes: 1024, BundleMaxBytes: 1 << 20,
	}
	for _, mutate := range []func(*OnlineConfig){
		func(value *OnlineConfig) { value.ProxyURL = "" },
		func(value *OnlineConfig) { value.ProxyURL = "http://public-proxy.example.test" },
		func(value *OnlineConfig) { value.AllowedOrigins = []string{"http://updates.example.test"} },
		func(value *OnlineConfig) { value.AllowedOrigins = []string{"https://updates.example.test/path"} },
		func(value *OnlineConfig) { value.RequestTimeout = 0 },
	} {
		config := base
		mutate(&config)
		if _, err := NewOnlineFetcher(config); !errors.Is(err, ErrPolicyRejected) {
			t.Fatalf("unsafe online config=%+v error=%v", config, err)
		}
	}

	credential := []byte("updater-only-token")
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://updates.example.test:443/v1/manifest" ||
			request.Header.Get("Authorization") != "Bearer "+string(credential) {
			t.Fatalf("unexpected allowlisted request: %s %v", request.URL, request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader("manifest")), Request: request,
		}, nil
	})
	fetcher, err := newOnlineFetcher(base, credential, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := fetcher.FetchMetadata(context.Background(), "https://updates.example.test:443/v1/manifest")
	if err != nil || string(payload) != "manifest" {
		t.Fatalf("payload=%q error=%v", payload, err)
	}
	for _, rawURL := range []string{
		"https://updates.example.test/v1/manifest",
		"https://other.example.test:443/v1/manifest",
		"https://updates.example.test:443/v1/../manifest",
		"https://user@updates.example.test:443/v1/manifest",
	} {
		if _, err := fetcher.FetchMetadata(context.Background(), rawURL); !errors.Is(err, ErrPolicyRejected) {
			t.Fatalf("unsafe URL %q error=%v", rawURL, err)
		}
	}
}

func TestOnlineFetcherRejectsRedirectOversizeAndCredentialBearingErrors(t *testing.T) {
	config := OnlineConfig{
		Enabled: true, AllowedOrigins: []string{"https://updates.example.test:443"},
		ProxyURL: "http://127.0.0.1:8888", RequestTimeout: time.Minute,
		MetadataMaxBytes: 8, BundleMaxBytes: 16,
	}
	credential := []byte("never-return-this-token")
	responses := []roundTripFunc{
		func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://updates.example.test:443/other"}},
				Body: io.NopCloser(strings.NewReader("redirect")), Request: request,
			}, nil
		},
		func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Length": []string{"9"}},
				Body: io.NopCloser(strings.NewReader("123456789")), Request: request,
			}, nil
		},
		func(*http.Request) (*http.Response, error) { return nil, errors.New(string(credential)) },
	}
	for index, transport := range responses {
		fetcher, err := newOnlineFetcher(config, credential, &http.Client{Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		_, err = fetcher.FetchMetadata(context.Background(), "https://updates.example.test:443/manifest")
		if err == nil || strings.Contains(err.Error(), string(credential)) {
			t.Fatalf("unsafe result %d error=%v", index, err)
		}
	}
}

func TestServiceRegistersStoredCandidateAndCommitsActivation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inboxRoot := t.TempDir()
	publicKey, _, _ := writeInboxCandidate(t, inboxRoot, "operator-package", now)
	if err := os.Chmod(inboxRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(inboxRoot) })
	inbox, err := NewInbox(inboxRoot, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	client := &serviceProtocolClient{candidateID: strings.Repeat("1", 32)}
	service, err := NewService(inbox, store, activator, client, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := service.ScanAndRegister(context.Background(), TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now)
	if err != nil || len(receipts) != 1 || receipts[0].CandidateID != client.candidateID {
		t.Fatalf("receipts=%+v error=%v", receipts, err)
	}
	journalPayload, err := os.ReadFile(filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"operator-package", inboxRoot, "manifest.json", "bundle.tar", "signature", "content", "path"} {
		if strings.Contains(strings.ToLower(string(journalPayload)), strings.ToLower(forbidden)) {
			t.Fatalf("candidate journal leaked %q: %s", forbidden, journalPayload)
		}
	}
	client.directive = &ActivationDirective{
		SchemaVersion: 1, CandidateID: client.candidateID, NewFingerprint: receipts[0].BundleFingerprint,
	}
	client.decision = ActivationReportResult{
		SchemaVersion: 1, Decision: ActivationDecisionCommit, ActiveFingerprint: receipts[0].BundleFingerprint,
	}
	retry, err := service.PollAndActivate(context.Background())
	if err != nil || retry != 5 {
		t.Fatalf("poll retry=%d error=%v", retry, err)
	}
	if active, err := activator.ActiveFingerprint(); err != nil || active != receipts[0].BundleFingerprint {
		t.Fatalf("active=%q error=%v", active, err)
	}
	journalPayload, err = os.ReadFile(filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil || string(journalPayload) != `{"schema_version":1,"candidates":[]}` {
		t.Fatalf("terminal candidate journal=%s error=%v", journalPayload, err)
	}
}

func TestServiceScansInboxOnlyWhenCoreDeliversAuthorizedRequest(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inboxRoot := t.TempDir()
	publicKey, _, _ := writeInboxCandidate(t, inboxRoot, "operator-package", now)
	if err := os.Chmod(inboxRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(inboxRoot) })
	inbox, err := NewInbox(inboxRoot, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	client := &serviceProtocolClient{candidateID: strings.Repeat("1", 32), scanRequested: true}
	service, err := NewService(inbox, store, activator, client, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	trust := TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}
	if retry, err := service.PollScanAndActivate(context.Background(), trust, now); err != nil || retry != 5 || client.registerCalls != 1 {
		t.Fatalf("authorized scan retry=%d calls=%d err=%v", retry, client.registerCalls, err)
	}
	if retry, err := service.PollScanAndActivate(context.Background(), trust, now); err != nil || retry != 5 || client.registerCalls != 2 {
		t.Fatalf("non-requested scan retry=%d calls=%d err=%v", retry, client.registerCalls, err)
	}
}

func TestServiceRejectsUnknownCandidateAndRollsBackRejectedActivation(t *testing.T) {
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	oldFingerprint := strings.Repeat("a", 64)
	newFingerprint := strings.Repeat("b", 64)
	for _, fingerprint := range []string{oldFingerprint, newFingerprint} {
		if _, err := store.StoreBundle(context.Background(), VerifiedBundle{
			BundleFingerprint: fingerprint,
			Files:             []BundleFilePayload{{Path: "data.dat", Mode: 0o444, Content: []byte(fingerprint[:8])}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activator.Activate(context.Background(), ActivationRequest{
		CandidateID: strings.Repeat("9", 32), NewFingerprint: oldFingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	if err := activator.Recover(context.Background(), oldFingerprint); err != nil {
		t.Fatal(err)
	}
	journal := candidateJournal{SchemaVersion: 1, Candidates: []candidateJournalEntry{{
		CandidateID: strings.Repeat("1", 32), BundleFingerprint: newFingerprint,
	}}}
	payload, _ := json.Marshal(journal)
	journalPath := filepath.Join(storeRoot, "candidate-journal.json")
	if err := os.WriteFile(journalPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	client := &serviceProtocolClient{
		directive: &ActivationDirective{
			SchemaVersion: 1, CandidateID: strings.Repeat("2", 32), ExpectedOldFingerprint: oldFingerprint,
			NewFingerprint: newFingerprint,
		},
		decision: ActivationReportResult{SchemaVersion: 1, Decision: ActivationDecisionRollback, ActiveFingerprint: oldFingerprint},
	}
	service, err := NewService(nil, store, activator, client, journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PollAndActivate(context.Background()); !errors.Is(err, ErrActivationFailed) {
		t.Fatalf("unknown candidate error=%v", err)
	}
	client.directive.CandidateID = strings.Repeat("1", 32)
	if _, err := service.PollAndActivate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if active, err := activator.ActiveFingerprint(); err != nil || active != oldFingerprint {
		t.Fatalf("rolled back active=%q error=%v", active, err)
	}
}

func TestServiceReplaysPrivateCandidateReceiptAfterCoreRestart(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inboxRoot := t.TempDir()
	publicKey, _, _ := writeInboxCandidate(t, inboxRoot, "operator-package", now)
	if err := os.Chmod(inboxRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(inboxRoot) })
	inbox, err := NewInbox(inboxRoot, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	candidateID := strings.Repeat("1", 32)
	firstClient := &serviceProtocolClient{candidateID: candidateID}
	service, err := NewService(inbox, store, activator, firstClient, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ScanAndRegister(context.Background(), TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now); err != nil {
		t.Fatal(err)
	}

	restartedClient := &serviceProtocolClient{candidateID: candidateID}
	restarted, err := NewService(nil, store, activator, restartedClient, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := restarted.PollAndActivate(context.Background()); err != nil || retry != 5 {
		t.Fatalf("restart replay retry=%d err=%v", retry, err)
	}
	if restartedClient.registerCalls != 1 || len(restartedClient.registrations) != 1 ||
		len(restartedClient.registrations[0].Receipt.Capabilities) == 0 {
		t.Fatalf("restart registrations=%+v calls=%d", restartedClient.registrations, restartedClient.registerCalls)
	}
	journalPayload, err := os.ReadFile(filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(journalPayload), "capabilities") || strings.Contains(string(journalPayload), "profiles") {
		t.Fatalf("handle journal contains replay metadata: %s", journalPayload)
	}
	replayInfo, err := os.Lstat(filepath.Join(storeRoot, "candidate-receipts.json"))
	if err != nil || !replayInfo.Mode().IsRegular() || replayInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private replay metadata info=%v err=%v", replayInfo, err)
	}
}

func TestServiceReportsCrashAfterPointerSwapBeforePullingAgain(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	inboxRoot := t.TempDir()
	publicKey, _, _ := writeInboxCandidate(t, inboxRoot, "operator-package", now)
	if err := os.Chmod(inboxRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(inboxRoot) })
	inbox, err := NewInbox(inboxRoot, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := newStoreTestRoot(t)
	store, err := NewStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	activator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	candidateID := strings.Repeat("1", 32)
	client := &serviceProtocolClient{candidateID: candidateID}
	service, err := NewService(inbox, store, activator, client, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := service.ScanAndRegister(context.Background(), TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("scan receipts=%+v err=%v", receipts, err)
	}
	client.directive = &ActivationDirective{
		SchemaVersion: 1, CandidateID: candidateID, NewFingerprint: receipts[0].BundleFingerprint,
	}
	client.decision = ActivationReportResult{
		SchemaVersion: 1, Decision: ActivationDecisionCommit, ActiveFingerprint: receipts[0].BundleFingerprint,
	}
	activator.fault = func(stage activationStage) error {
		if stage == activationAfterSwap {
			return errors.New("simulated updater crash after pointer swap")
		}
		return nil
	}
	if _, err := service.PollAndActivate(context.Background()); !errors.Is(err, ErrActivationFailed) || client.reportCalls != 0 {
		t.Fatalf("post-swap crash err=%v reports=%d", err, client.reportCalls)
	}

	restartedActivator, err := NewActivator(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	restartedClient := &serviceProtocolClient{
		candidateID: candidateID, directive: client.directive, decision: client.decision, clearDirectiveAfterReport: true,
	}
	restarted, err := NewService(nil, store, restartedActivator, restartedClient, filepath.Join(storeRoot, "candidate-journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := restarted.PollAndActivate(context.Background()); err != nil || retry != 5 || restartedClient.reportCalls != 1 {
		t.Fatalf("recovered poll retry=%d reports=%d err=%v", retry, restartedClient.reportCalls, err)
	}
	if active, err := restartedActivator.ActiveFingerprint(); err != nil || active != receipts[0].BundleFingerprint {
		t.Fatalf("recovered active=%q err=%v", active, err)
	}
}

type serviceProtocolClient struct {
	candidateID               string
	directive                 *ActivationDirective
	decision                  ActivationReportResult
	scanRequested             bool
	clearDirectiveAfterReport bool
	registerCalls             int
	reportCalls               int
	registrations             []RegisterCandidateRequest
}

func (client *serviceProtocolClient) RegisterCandidate(_ context.Context, request RegisterCandidateRequest) (RegisterCandidateResult, error) {
	client.registerCalls++
	client.registrations = append(client.registrations, request)
	return RegisterCandidateResult{SchemaVersion: 1, CandidateID: client.candidateID}, nil
}

func (client *serviceProtocolClient) PullActivation(context.Context, PullActivationRequest) (PullActivationResult, error) {
	scanRequested := client.scanRequested
	client.scanRequested = false
	return PullActivationResult{
		SchemaVersion: 1, RetryAfterSeconds: 5, ScanRequested: scanRequested, Directive: client.directive,
	}, nil
}

func (client *serviceProtocolClient) ReportActivation(context.Context, ActivationReportRequest) (ActivationReportResult, error) {
	client.reportCalls++
	if client.clearDirectiveAfterReport {
		client.directive = nil
	}
	return client.decision, nil
}
