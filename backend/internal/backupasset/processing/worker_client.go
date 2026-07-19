package processing

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrWorkerTemporarilyUnavailable = errors.New("worker protocol service temporarily unavailable")

type WorkerClientConfig struct {
	LocalSocketPath      string
	RemoteEndpoint       string
	RemoteClientCertFile string
	RemoteClientKeyFile  string
	RemoteServerCAFile   string
	JSONMaxBytes         int64
	InputMaxBytes        int64
	ArtifactMaxBytes     int64
	RequestTimeout       time.Duration
}

type WorkerClient struct {
	httpClient       *http.Client
	baseURL          string
	jsonMaxBytes     int64
	inputMaxBytes    int64
	artifactMaxBytes int64
}

func NewWorkerClient(config WorkerClientConfig) (*WorkerClient, error) {
	local := strings.TrimSpace(config.LocalSocketPath)
	remote := strings.TrimSpace(config.RemoteEndpoint)
	if (local == "") == (remote == "") {
		return nil, ErrWorkerTransportUnsafe
	}
	if config.JSONMaxBytes == 0 {
		config.JSONMaxBytes = 64 << 10
	}
	if config.InputMaxBytes == 0 {
		config.InputMaxBytes = 16 << 20
	}
	if config.ArtifactMaxBytes == 0 {
		config.ArtifactMaxBytes = 64 << 20
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 30 * time.Second
	}
	if config.JSONMaxBytes <= 0 || config.JSONMaxBytes > 64<<10 || config.InputMaxBytes <= 0 || config.ArtifactMaxBytes <= 0 ||
		config.RequestTimeout <= 0 || config.RequestTimeout > 2*time.Minute {
		return nil, ErrWorkerTransportUnsafe
	}

	transport := &http.Transport{
		Proxy:                 nil,
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: config.RequestTimeout,
		IdleConnTimeout:       config.RequestTimeout,
	}
	baseURL := "http://asset-worker.local"
	if local != "" {
		if !filepath.IsAbs(local) || filepath.Clean(local) != local {
			return nil, ErrWorkerTransportUnsafe
		}
		dialer := &net.Dialer{Timeout: config.RequestTimeout}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", local)
		}
	} else {
		endpoint, tlsConfig, err := workerRemoteClientTLS(config)
		if err != nil {
			return nil, err
		}
		baseURL = endpoint
		transport.TLSClientConfig = tlsConfig
	}
	client, err := newWorkerClient(&http.Client{Transport: transport, Timeout: config.RequestTimeout}, baseURL, config.JSONMaxBytes, config.InputMaxBytes)
	if err != nil {
		return nil, err
	}
	client.artifactMaxBytes = config.ArtifactMaxBytes
	return client, nil
}

func workerRemoteClientTLS(config WorkerClientConfig) (string, *tls.Config, error) {
	endpoint, err := url.Parse(strings.TrimSpace(config.RemoteEndpoint))
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		(endpoint.Path != "" && endpoint.Path != "/") || endpoint.Host == "" {
		return "", nil, ErrWorkerTransportUnsafe
	}
	ip := net.ParseIP(endpoint.Hostname())
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return "", nil, ErrWorkerTransportUnsafe
	}
	if strings.TrimSpace(config.RemoteClientCertFile) == "" || strings.TrimSpace(config.RemoteClientKeyFile) == "" ||
		strings.TrimSpace(config.RemoteServerCAFile) == "" {
		return "", nil, ErrWorkerTransportUnsafe
	}
	certificate, err := loadPrivateX509KeyPair(config.RemoteClientCertFile, config.RemoteClientKeyFile)
	if err != nil {
		return "", nil, err
	}
	caPEM, err := os.ReadFile(config.RemoteServerCAFile)
	if err != nil {
		return "", nil, ErrWorkerTransportUnsafe
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return "", nil, ErrWorkerTransportUnsafe
	}
	return strings.TrimSuffix(endpoint.String(), "/"), &tls.Config{
		MinVersion:   tls.VersionTLS13,
		MaxVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		Certificates: []tls.Certificate{certificate},
		ServerName:   endpoint.Hostname(),
	}, nil
}

func newWorkerClient(httpClient *http.Client, rawBaseURL string, jsonMaxBytes, inputMaxBytes int64) (*WorkerClient, error) {
	if httpClient == nil || jsonMaxBytes <= 0 || jsonMaxBytes > 64<<10 || inputMaxBytes <= 0 {
		return nil, ErrWorkerTransportUnsafe
	}
	base, err := url.Parse(rawBaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil ||
		base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, ErrWorkerTransportUnsafe
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &WorkerClient{
		httpClient: &clientCopy, baseURL: strings.TrimSuffix(base.String(), "/"),
		jsonMaxBytes: jsonMaxBytes, inputMaxBytes: inputMaxBytes, artifactMaxBytes: inputMaxBytes,
	}, nil
}

func (client *WorkerClient) Handshake(ctx context.Context, request HandshakeRequest) (HandshakeResult, error) {
	var result HandshakeResult
	if err := client.postJSON(ctx, "/internal/v1/asset-worker/handshake", request, &result, false); err != nil {
		return HandshakeResult{}, err
	}
	if !lowerHex(result.WorkerID, 32) || result.TrustState == "" || result.HealthState == "" || result.CapabilityCount < 0 {
		return HandshakeResult{}, ErrProtocolInvalid
	}
	return result, nil
}

func (client *WorkerClient) Pull(ctx context.Context, request WorkerPullRequest) (WorkerJobEnvelope, error) {
	var result WorkerJobEnvelope
	if err := client.postJSON(ctx, "/internal/v1/asset-worker/leases/pull", request, &result, true); err != nil {
		return WorkerJobEnvelope{}, err
	}
	if result.SchemaVersion != 1 || result.ProtocolVersion != WorkerProtocolVersion || !lowerHex(result.JobID, 32) || !lowerHex(result.AttemptID, 32) {
		return WorkerJobEnvelope{}, ErrProtocolInvalid
	}
	return result, nil
}

func (client *WorkerClient) Heartbeat(ctx context.Context, jobID string, request WorkerHeartbeatRequest) (WorkerHeartbeatResult, error) {
	if !lowerHex(jobID, 32) {
		return WorkerHeartbeatResult{}, ErrProtocolInvalid
	}
	var result WorkerHeartbeatResult
	if err := client.postJSON(ctx, "/internal/v1/asset-worker/jobs/"+jobID+"/heartbeat", request, &result, false); err != nil {
		return WorkerHeartbeatResult{}, err
	}
	if result.SchemaVersion != 1 || result.TransitionRevision <= 0 {
		return WorkerHeartbeatResult{}, ErrProtocolInvalid
	}
	return result, nil
}

func (client *WorkerClient) Transition(ctx context.Context, request WorkerTransitionRequest) (WorkerTransitionResult, error) {
	if !lowerHex(request.JobID, 32) {
		return WorkerTransitionResult{}, ErrProtocolInvalid
	}
	var result WorkerTransitionResult
	if err := client.postJSON(ctx, "/internal/v1/asset-worker/jobs/"+request.JobID+"/transitions", request, &result, false); err != nil {
		return WorkerTransitionResult{}, err
	}
	if result.SchemaVersion != 1 || !result.State.Valid() || result.Revision <= 0 {
		return WorkerTransitionResult{}, ErrProtocolInvalid
	}
	return result, nil
}

func (client *WorkerClient) ActivateInput(ctx context.Context, request WorkerInputActivateRequest) (WorkerInputActivation, error) {
	if !lowerHex(request.JobID, 32) {
		return WorkerInputActivation{}, ErrProtocolInvalid
	}
	var result WorkerInputActivation
	if err := client.postJSON(ctx, "/internal/v1/asset-worker/jobs/"+request.JobID+"/input/activate", request, &result, false); err != nil {
		return WorkerInputActivation{}, err
	}
	if result.SchemaVersion != 1 || !lowerHex(result.SessionID, 32) || result.TransitionRevision <= 0 || result.ExpiresAt.IsZero() {
		return WorkerInputActivation{}, ErrProtocolInvalid
	}
	return result, nil
}

func (client *WorkerClient) ReadInput(ctx context.Context, requestBody WorkerInputReadRequest) ([]byte, error) {
	if client == nil || client.httpClient == nil || ctx == nil || !lowerHex(requestBody.SessionID, 32) || requestBody.Length <= 0 ||
		requestBody.Length > client.inputMaxBytes || (requestBody.Mode != "sequential" && requestBody.Mode != "range") {
		return nil, ErrProtocolInvalid
	}
	payload, err := json.Marshal(requestBody)
	if err != nil || int64(len(payload)) > client.jsonMaxBytes {
		return nil, ErrProtocolInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		client.baseURL+"/internal/v1/asset-worker/input-sessions/"+requestBody.SessionID+"/ranges", bytes.NewReader(payload))
	if err != nil {
		return nil, ErrProtocolInvalid
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/octet-stream")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, ErrWorkerTemporarilyUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, client.decodeErrorResponse(response)
	}
	if response.Header.Get("Content-Type") != "application/octet-stream" || response.Header.Get("Cache-Control") != "no-store" {
		return nil, ErrProtocolInvalid
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, requestBody.Length+1))
	if err != nil || int64(len(content)) > requestBody.Length {
		return nil, ErrProtocolInvalid
	}
	return content, nil
}

func (client *WorkerClient) ActivateSink(ctx context.Context, request WorkerSinkActivateRequest) (WorkerSinkActivation, error) {
	if !lowerHex(request.JobID, 32) {
		return WorkerSinkActivation{}, ErrProtocolInvalid
	}
	var result WorkerSinkActivation
	if err := client.postJSON(ctx, "/internal/v1/asset-worker/jobs/"+request.JobID+"/sink/activate", request, &result, false); err != nil {
		return WorkerSinkActivation{}, err
	}
	if result.SchemaVersion != 1 || !lowerHex(result.SessionID, 32) || result.ExpiresAt.IsZero() || result.MaxArtifacts <= 0 ||
		result.MaxArtifactBytes <= 0 || result.MaxTotalBytes <= 0 || result.MaxInFlight <= 0 {
		return WorkerSinkActivation{}, ErrProtocolInvalid
	}
	return result, nil
}

func (client *WorkerClient) UploadArtifact(ctx context.Context, requestBody WorkerUploadArtifactRequest, content io.Reader) (UploadedArtifact, error) {
	if client == nil || client.httpClient == nil || ctx == nil || content == nil || !lowerHex(requestBody.SessionID, 32) {
		return UploadedArtifact{}, ErrProtocolInvalid
	}
	metadata, err := json.Marshal(requestBody)
	if err != nil || int64(len(metadata)) > client.jsonMaxBytes {
		return UploadedArtifact{}, ErrProtocolInvalid
	}
	reader, writer := io.Pipe()
	parts := multipart.NewWriter(writer)
	writeResult := make(chan error, 1)
	go func() {
		metadataHeader := make(textproto.MIMEHeader)
		metadataHeader.Set("Content-Disposition", `form-data; name="metadata"`)
		metadataHeader.Set("Content-Type", "application/json")
		metadataPart, partErr := parts.CreatePart(metadataHeader)
		if partErr == nil {
			_, partErr = metadataPart.Write(metadata)
		}
		if partErr == nil {
			contentHeader := make(textproto.MIMEHeader)
			contentHeader.Set("Content-Disposition", `form-data; name="content"`)
			contentHeader.Set("Content-Type", "application/octet-stream")
			var contentPart io.Writer
			contentPart, partErr = parts.CreatePart(contentHeader)
			if partErr == nil {
				var count int64
				count, partErr = io.Copy(contentPart, io.LimitReader(content, client.artifactMaxBytes+1))
				if partErr == nil && count > client.artifactMaxBytes {
					partErr = ErrProtocolInvalid
				}
			}
		}
		if closeErr := parts.Close(); partErr == nil {
			partErr = closeErr
		}
		if partErr != nil {
			_ = writer.CloseWithError(partErr)
		} else {
			_ = writer.Close()
		}
		writeResult <- partErr
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		client.baseURL+"/internal/v1/asset-worker/sink-sessions/"+requestBody.SessionID+"/artifacts", reader)
	if err != nil {
		_ = reader.Close()
		return UploadedArtifact{}, ErrProtocolInvalid
	}
	request.Header.Set("Content-Type", parts.FormDataContentType())
	request.Header.Set("Accept", "application/json")
	response, requestErr := client.httpClient.Do(request)
	writeErr := <-writeResult
	if requestErr != nil || writeErr != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		if errors.Is(writeErr, ErrProtocolInvalid) {
			return UploadedArtifact{}, ErrProtocolInvalid
		}
		return UploadedArtifact{}, ErrWorkerTemporarilyUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	var result UploadedArtifact
	if err := client.decodeJSONResponse(response, &result, false); err != nil {
		return UploadedArtifact{}, err
	}
	if !lowerHex(result.UploadID, 32) || !lowerHex(result.BlobID, 32) || result.Ordinal != requestBody.Artifact.Ordinal {
		return UploadedArtifact{}, ErrProtocolInvalid
	}
	return result, nil
}

func (client *WorkerClient) CommitManifest(ctx context.Context, request WorkerCommitManifestRequest) (WorkerCommitManifestResult, error) {
	if !lowerHex(request.SessionID, 32) {
		return WorkerCommitManifestResult{}, ErrProtocolInvalid
	}
	var result WorkerCommitManifestResult
	if err := client.postJSON(ctx, "/internal/v1/asset-worker/sink-sessions/"+request.SessionID+"/manifest", request, &result, false); err != nil {
		return WorkerCommitManifestResult{}, err
	}
	if result.SchemaVersion != 1 || !lowerHex(result.ArtifactSetID, 32) || !lowerHex(result.ManifestDigest, 64) {
		return WorkerCommitManifestResult{}, ErrProtocolInvalid
	}
	return result, nil
}

func (client *WorkerClient) Drain(ctx context.Context, request WorkerDrainRequest) error {
	var result struct{}
	return client.postJSON(ctx, "/internal/v1/asset-worker/drain", request, &result, false)
}

func (client *WorkerClient) postJSON(ctx context.Context, path string, requestBody, result any, allowNoContent bool) error {
	if client == nil || client.httpClient == nil || ctx == nil || !strings.HasPrefix(path, "/internal/v1/asset-worker/") {
		return ErrProtocolInvalid
	}
	payload, err := json.Marshal(requestBody)
	if err != nil || int64(len(payload)) > client.jsonMaxBytes {
		return ErrProtocolInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return ErrProtocolInvalid
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return ErrWorkerTemporarilyUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	return client.decodeJSONResponse(response, result, allowNoContent)
}

func (client *WorkerClient) decodeJSONResponse(response *http.Response, result any, allowNoContent bool) error {
	if client == nil || response == nil || response.Body == nil || result == nil {
		return ErrProtocolInvalid
	}
	if response.StatusCode == http.StatusNoContent {
		if allowNoContent {
			return ErrNoWork
		}
		return ErrProtocolInvalid
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, client.jsonMaxBytes+1))
	if err != nil || int64(len(body)) > client.jsonMaxBytes {
		return ErrProtocolInvalid
	}
	var envelope workerClientResponse
	if DecodeWorkerJSON(body, &envelope) != nil || envelope.SchemaVersion != 1 || envelope.Code == "" {
		return ErrProtocolInvalid
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Code != "ok" {
		if envelope.Code == "ok" || response.StatusCode >= 200 && response.StatusCode < 300 {
			return ErrProtocolInvalid
		}
		return workerClientError(envelope.Code)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || DecodeWorkerJSON(envelope.Data, result) != nil {
		return ErrProtocolInvalid
	}
	return nil
}

func (client *WorkerClient) decodeErrorResponse(response *http.Response) error {
	if client == nil || response == nil || response.Body == nil {
		return ErrWorkerTemporarilyUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, client.jsonMaxBytes+1))
	if err != nil || int64(len(body)) > client.jsonMaxBytes {
		return ErrProtocolInvalid
	}
	var envelope workerClientResponse
	if DecodeWorkerJSON(body, &envelope) != nil || envelope.SchemaVersion != 1 || envelope.Code == "" || envelope.Code == "ok" {
		return ErrProtocolInvalid
	}
	return workerClientError(envelope.Code)
}

type workerClientResponse struct {
	SchemaVersion int             `json:"schema_version"`
	Code          string          `json:"code"`
	Data          json.RawMessage `json:"data,omitempty"`
}

func workerClientError(code string) error {
	switch code {
	case "unauthenticated":
		return ErrWorkerUnauthenticated
	case "forbidden":
		return ErrWorkerQuarantined
	case "invalid_request", "body_too_large":
		return ErrProtocolInvalid
	case "conflict":
		return ErrGrantDenied
	case "fence_lost":
		return ErrAttemptLost
	case "temporarily_unavailable", "rate_limited", "feature_disabled":
		return ErrWorkerTemporarilyUnavailable
	default:
		return ErrProtocolInvalid
	}
}

func (client *WorkerClient) CloseIdleConnections() {
	if client != nil && client.httpClient != nil {
		client.httpClient.CloseIdleConnections()
	}
}

type WorkerProtocolClient interface {
	Handshake(context.Context, HandshakeRequest) (HandshakeResult, error)
	Pull(context.Context, WorkerPullRequest) (WorkerJobEnvelope, error)
	Heartbeat(context.Context, string, WorkerHeartbeatRequest) (WorkerHeartbeatResult, error)
	Transition(context.Context, WorkerTransitionRequest) (WorkerTransitionResult, error)
	ActivateInput(context.Context, WorkerInputActivateRequest) (WorkerInputActivation, error)
	ReadInput(context.Context, WorkerInputReadRequest) ([]byte, error)
	ActivateSink(context.Context, WorkerSinkActivateRequest) (WorkerSinkActivation, error)
	UploadArtifact(context.Context, WorkerUploadArtifactRequest, io.Reader) (UploadedArtifact, error)
	CommitManifest(context.Context, WorkerCommitManifestRequest) (WorkerCommitManifestResult, error)
	Drain(context.Context, WorkerDrainRequest) error
	CloseIdleConnections()
}

type WorkerCapability interface {
	Advertisement() CapabilityAdvertisement
	Execute(context.Context, WorkerCapabilityJob) ([]WorkerCapabilityArtifact, error)
}

type WorkerCapabilityJob struct {
	Parameters CanonicalParametersV1
	Input      WorkerCapabilityInput
}

type WorkerCapabilityArtifact struct {
	Declaration ArtifactDeclaration
	Content     io.Reader
}

type WorkerCapabilityInput interface {
	Info() WorkerInputSourceInfo
	ReadSequential(context.Context, int64) ([]byte, error)
	ReadRange(context.Context, int64, int64) ([]byte, error)
}

type WorkerCapabilitySet struct {
	capabilities   map[string]WorkerCapability
	advertisements []CapabilityAdvertisement
}

func NewProductionWorkerCapabilitySet() *WorkerCapabilitySet {
	return &WorkerCapabilitySet{capabilities: map[string]WorkerCapability{}}
}

func NewWorkerCapabilitySet(capabilities []WorkerCapability) (*WorkerCapabilitySet, error) {
	result := &WorkerCapabilitySet{
		capabilities:   make(map[string]WorkerCapability, len(capabilities)),
		advertisements: make([]CapabilityAdvertisement, 0, len(capabilities)),
	}
	definitions := make([]CapabilityDefinition, 0, len(capabilities))
	for _, capability := range capabilities {
		if capability == nil {
			return nil, ErrProtocolInvalid
		}
		advertisement := capability.Advertisement()
		definitions = append(definitions, CapabilityDefinition{
			Capability: advertisement.Capability, CapabilitySchema: advertisement.CapabilitySchema,
			PipelineFingerprint: advertisement.PipelineFingerprint, OutputProfile: advertisement.OutputProfile,
		})
		result.advertisements = append(result.advertisements, advertisement)
	}
	registry, err := NewCapabilityRegistry(definitions)
	if err != nil {
		return nil, err
	}
	for index, capability := range capabilities {
		advertisement := result.advertisements[index]
		if _, err := registry.validateAdvertisement(advertisement); err != nil {
			return nil, err
		}
		key := capabilityKey(advertisement.Capability, advertisement.CapabilitySchema, advertisement.PipelineFingerprint, advertisement.OutputProfile)
		if _, exists := result.capabilities[key]; exists {
			return nil, ErrProtocolInvalid
		}
		result.capabilities[key] = capability
	}
	return result, nil
}

func (set *WorkerCapabilitySet) Advertisements() []CapabilityAdvertisement {
	if set == nil {
		return nil
	}
	result := make([]CapabilityAdvertisement, len(set.advertisements))
	for index := range set.advertisements {
		result[index] = set.advertisements[index]
		result[index].InputModes = append([]ProtocolInputMode(nil), set.advertisements[index].InputModes...)
	}
	return result
}

func (set *WorkerCapabilitySet) capability(descriptor WorkDescriptorV1) (WorkerCapability, bool) {
	if set == nil {
		return nil, false
	}
	key := capabilityKey(descriptor.Capability, descriptor.CapabilitySchema, descriptor.PipelineFingerprint, descriptor.OutputProfile)
	capability, ok := set.capabilities[key]
	return capability, ok
}

type WorkerRunnerConfig struct {
	InstanceID        string
	IdentityRevision  int64
	InteractiveSlots  int
	BackgroundSlots   int
	HeartbeatInterval time.Duration
	PullBackoff       time.Duration
	GracePeriod       time.Duration
}

type WorkerRunner struct {
	client       WorkerProtocolClient
	capabilities *WorkerCapabilitySet
	config       WorkerRunnerConfig
	workerID     string
}

func NewWorkerRunner(client WorkerProtocolClient, capabilities *WorkerCapabilitySet, config WorkerRunnerConfig) (*WorkerRunner, error) {
	if client == nil || capabilities == nil || !lowerHex(config.InstanceID, 32) || config.IdentityRevision <= 0 ||
		config.InteractiveSlots < 0 || config.BackgroundSlots < 0 || config.InteractiveSlots+config.BackgroundSlots <= 0 ||
		config.HeartbeatInterval <= 0 || config.PullBackoff <= 0 || config.GracePeriod <= 0 || config.GracePeriod > 2*time.Minute {
		return nil, ErrProtocolInvalid
	}
	return &WorkerRunner{client: client, capabilities: capabilities, config: config}, nil
}

func (runner *WorkerRunner) Run(ctx context.Context) error {
	if runner == nil || ctx == nil {
		return ErrProtocolInvalid
	}
	if err := runner.handshake(ctx); err != nil {
		return err
	}
	defer runner.client.CloseIdleConnections()
	defer runner.drain()
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		err := runner.runOnce(ctx)
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		if !errors.Is(err, ErrNoWork) && !errors.Is(err, ErrWorkerTemporarilyUnavailable) {
			return err
		}
		timer := time.NewTimer(runner.config.PullBackoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
		}
	}
}

func (runner *WorkerRunner) handshake(ctx context.Context) error {
	if runner == nil || runner.client == nil || ctx == nil {
		return ErrProtocolInvalid
	}
	result, err := runner.client.Handshake(ctx, HandshakeRequest{
		SchemaVersion: 1, ProtocolVersion: WorkerProtocolVersion,
		InstanceID: runner.config.InstanceID, IdentityRevision: runner.config.IdentityRevision,
		InteractiveSlots: runner.config.InteractiveSlots, BackgroundSlots: runner.config.BackgroundSlots,
		Capabilities: runner.capabilities.Advertisements(),
	})
	if err != nil {
		return err
	}
	if result.TrustState != "active" || (result.HealthState != "ready" && result.HealthState != "degraded") ||
		result.CapabilityCount != len(runner.capabilities.advertisements) {
		return ErrProtocolInvalid
	}
	runner.workerID = result.WorkerID
	return nil
}

func (runner *WorkerRunner) runOnce(ctx context.Context) error {
	if runner == nil || ctx == nil || !lowerHex(runner.workerID, 32) {
		return ErrProtocolInvalid
	}
	envelope, err := runner.client.Pull(ctx, WorkerPullRequest{
		SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
	})
	if err != nil {
		return err
	}
	capability, ok := runner.capabilities.capability(envelope.Descriptor)
	if !ok {
		_, transitionErr := runner.client.Transition(ctx, WorkerTransitionRequest{
			SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
			JobID: envelope.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: envelope.TransitionRevision,
			To: ProcessingFailed, ErrorCode: ProcessingErrorProtocolIncompatible,
		})
		if transitionErr != nil {
			return transitionErr
		}
		return nil
	}
	return runner.runAttempt(ctx, envelope, capability)
}

func (runner *WorkerRunner) runAttempt(ctx context.Context, envelope WorkerJobEnvelope, capability WorkerCapability) error {
	heartbeat, err := runner.client.Heartbeat(ctx, envelope.JobID, WorkerHeartbeatRequest{
		SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID, AttemptID: envelope.AttemptID,
	})
	if err != nil {
		return err
	}
	if heartbeat.CancelRequested {
		return runner.cancelAttempt(ctx, envelope, heartbeat.TransitionRevision, heartbeat.CancelReason)
	}
	attemptCtx, cancelAttempt := context.WithCancel(ctx)
	stopHeartbeats := make(chan struct{})
	heartbeatsDone := make(chan struct{})
	heartbeatEvents := make(chan workerHeartbeatEvent, 1)
	go runner.heartbeatLoop(attemptCtx, envelope, cancelAttempt, stopHeartbeats, heartbeatsDone, heartbeatEvents)
	defer func() {
		close(stopHeartbeats)
		cancelAttempt()
		<-heartbeatsDone
	}()

	input, err := runner.client.ActivateInput(attemptCtx, WorkerInputActivateRequest{
		SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
		JobID: envelope.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: envelope.TransitionRevision,
		GrantID: envelope.InputActivation.GrantID, Secret: envelope.InputActivation.Secret,
	})
	if err != nil {
		return err
	}
	revision := input.TransitionRevision
	if envelope.Descriptor.Parameters.RequiresMaterialization {
		_, err = runner.client.Transition(attemptCtx, WorkerTransitionRequest{
			SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
			JobID: envelope.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: revision,
			To: ProcessingFailed, ErrorCode: ProcessingErrorMaterializationDisabled,
		})
		return err
	}
	transition, err := runner.client.Transition(attemptCtx, WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
		JobID: envelope.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: revision, To: ProcessingProcessing,
	})
	if err != nil {
		return err
	}
	revision = transition.Revision
	inputPort := &workerCapabilityInput{
		client: runner.client, workerID: runner.workerID, instanceID: runner.config.InstanceID,
		sessionID: input.SessionID, info: input.Source,
	}
	artifacts, err := capability.Execute(attemptCtx, WorkerCapabilityJob{Parameters: envelope.Descriptor.Parameters, Input: inputPort})
	if err != nil {
		return runner.handleAttemptInterruption(ctx, envelope, revision, heartbeatEvents, err)
	}
	transition, err = runner.client.Transition(attemptCtx, WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
		JobID: envelope.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: revision, To: ProcessingUploading,
	})
	if err != nil {
		return err
	}
	revision = transition.Revision
	sink, err := runner.client.ActivateSink(attemptCtx, WorkerSinkActivateRequest{
		SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
		JobID: envelope.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: revision,
		GrantID: envelope.SinkActivation.GrantID, Secret: envelope.SinkActivation.Secret,
	})
	if err != nil {
		return err
	}
	declarations := make([]ArtifactDeclaration, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Content == nil {
			return runner.failAttempt(ctx, envelope, revision, ProcessingErrorInvalidOutput)
		}
		if _, err := runner.client.UploadArtifact(attemptCtx, WorkerUploadArtifactRequest{
			SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
			SessionID: sink.SessionID, JobID: envelope.JobID, AttemptID: envelope.AttemptID, Artifact: artifact.Declaration,
		}, artifact.Content); err != nil {
			return err
		}
		declarations = append(declarations, artifact.Declaration)
	}
	_, err = runner.client.CommitManifest(attemptCtx, WorkerCommitManifestRequest{
		SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
		SessionID: sink.SessionID, JobID: envelope.JobID, AttemptID: envelope.AttemptID,
		SecurityPolicyRevision: envelope.Descriptor.SecurityPolicyRevision, Artifacts: declarations,
	})
	return err
}

func (runner *WorkerRunner) heartbeatLoop(
	ctx context.Context,
	envelope WorkerJobEnvelope,
	cancel context.CancelFunc,
	stop <-chan struct{},
	done chan<- struct{},
	events chan<- workerHeartbeatEvent,
) {
	defer close(done)
	ticker := time.NewTicker(runner.config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			result, err := runner.client.Heartbeat(ctx, envelope.JobID, WorkerHeartbeatRequest{
				SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID, AttemptID: envelope.AttemptID,
			})
			if err != nil || result.CancelRequested {
				select {
				case events <- workerHeartbeatEvent{result: result, err: err}:
				default:
				}
				cancel()
				return
			}
		}
	}
}

type workerHeartbeatEvent struct {
	result WorkerHeartbeatResult
	err    error
}

func (runner *WorkerRunner) handleAttemptInterruption(
	ctx context.Context,
	envelope WorkerJobEnvelope,
	revision int64,
	events <-chan workerHeartbeatEvent,
	executionErr error,
) error {
	select {
	case event := <-events:
		if event.err != nil {
			return event.err
		}
		if event.result.CancelRequested {
			return runner.cancelAttempt(ctx, envelope, event.result.TransitionRevision, event.result.CancelReason)
		}
	default:
	}
	if ctx.Err() != nil || errors.Is(executionErr, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return runner.failAttempt(ctx, envelope, revision, ProcessingErrorInvalidOutput)
}

func (runner *WorkerRunner) cancelAttempt(ctx context.Context, envelope WorkerJobEnvelope, revision int64, reason CancelReason) error {
	if revision <= 0 || (reason != CancelReasonInterestWithdrawn && reason != CancelReasonAdminRequested && reason != CancelReasonShutdown) {
		return ErrProtocolInvalid
	}
	_, err := runner.client.Transition(ctx, WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
		JobID: envelope.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: revision,
		To: ProcessingCanceled, CancelReason: reason,
	})
	return err
}

func (runner *WorkerRunner) failAttempt(ctx context.Context, envelope WorkerJobEnvelope, revision int64, code ProcessingErrorCode) error {
	_, err := runner.client.Transition(ctx, WorkerTransitionRequest{
		SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
		JobID: envelope.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: revision,
		To: ProcessingFailed, ErrorCode: code,
	})
	return err
}

func (runner *WorkerRunner) drain() {
	ctx, cancel := context.WithTimeout(context.Background(), runner.config.GracePeriod)
	defer cancel()
	_ = runner.client.Drain(ctx, WorkerDrainRequest{
		SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
	})
}

type workerCapabilityInput struct {
	client     WorkerProtocolClient
	workerID   string
	instanceID string
	sessionID  string
	info       WorkerInputSourceInfo
}

func (input *workerCapabilityInput) Info() WorkerInputSourceInfo { return input.info }

func (input *workerCapabilityInput) ReadSequential(ctx context.Context, length int64) ([]byte, error) {
	if input == nil || !input.info.Sequential {
		return nil, ErrProtocolInvalid
	}
	return input.client.ReadInput(ctx, WorkerInputReadRequest{
		SchemaVersion: 1, WorkerID: input.workerID, InstanceID: input.instanceID,
		SessionID: input.sessionID, Mode: "sequential", Length: length,
	})
}

func (input *workerCapabilityInput) ReadRange(ctx context.Context, offset, length int64) ([]byte, error) {
	if input == nil || !input.info.Range {
		return nil, ErrProtocolInvalid
	}
	return input.client.ReadInput(ctx, WorkerInputReadRequest{
		SchemaVersion: 1, WorkerID: input.workerID, InstanceID: input.instanceID,
		SessionID: input.sessionID, Mode: "range", Offset: offset, Length: length,
	})
}
