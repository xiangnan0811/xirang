package processing

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"io"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	workerCapabilities "xirang/backend/internal/backupasset/processing/capabilities"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
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

type ProductionToolRunner interface {
	RunInput(context.Context, workerCapabilities.ToolInvocation, io.Reader) (workerCapabilities.ToolResult, error)
}

type productionToolStreamRunner interface {
	RunInputStream(
		context.Context,
		workerCapabilities.ToolInvocation,
		io.Reader,
		func(io.Reader) error,
	) (workerCapabilities.ToolResult, error)
}

type ProductionWorkerCapabilityOptions struct {
	ToolRunner               ProductionToolRunner
	MalwareBundleFingerprint string
	BundleFingerprints       CapabilityBundleFingerprints
	AvailableCapabilities    map[string]bool
	Now                      func() time.Time
}

func NewProductionWorkerCapabilitySet() *WorkerCapabilitySet {
	result, err := newProductionWorkerCapabilitySet(ProductionWorkerCapabilityOptions{}, true)
	if err != nil {
		return &WorkerCapabilitySet{capabilities: map[string]WorkerCapability{}}
	}
	return result
}

func NewProductionWorkerCapabilitySetWithOptions(options ProductionWorkerCapabilityOptions) (*WorkerCapabilitySet, error) {
	return newProductionWorkerCapabilitySet(options, options.ToolRunner != nil)
}

func NewProductionWorkerCapabilitySetWithBundles(bundles CapabilityBundleFingerprints) (*WorkerCapabilitySet, error) {
	return newProductionWorkerCapabilitySet(ProductionWorkerCapabilityOptions{BundleFingerprints: bundles}, true)
}

func newProductionWorkerCapabilitySet(options ProductionWorkerCapabilityOptions, advertise bool) (*WorkerCapabilitySet, error) {
	if !advertise {
		return NewWorkerCapabilitySet(nil)
	}
	advertisements, err := productionCapabilityAdvertisementsWithBundles(options.BundleFingerprints)
	if err != nil {
		return nil, err
	}
	profiles := capabilityspec.WorkerProfiles()
	if len(advertisements) != len(profiles) {
		return nil, ErrProtocolInvalid
	}
	malwareBundles := options.BundleFingerprints[capabilityspec.CapabilityMalwareScan]
	if options.MalwareBundleFingerprint == "" && len(malwareBundles) == 1 {
		options.MalwareBundleFingerprint = malwareBundles[0]
	}
	if options.MalwareBundleFingerprint != "" && (!lowerHex(options.MalwareBundleFingerprint, 64) ||
		len(malwareBundles) > 0 && (len(malwareBundles) != 1 || malwareBundles[0] != options.MalwareBundleFingerprint)) {
		return nil, ErrProtocolInvalid
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	capabilities := make([]WorkerCapability, 0, len(advertisements))
	for index, advertisement := range advertisements {
		if options.AvailableCapabilities != nil && !options.AvailableCapabilities[profiles[index].Capability] {
			continue
		}
		if options.ToolRunner != nil && profiles[index].Capability == capabilityspec.CapabilityMalwareScan &&
			options.MalwareBundleFingerprint == "" {
			continue
		}
		capabilities = append(capabilities, productionWorkerCapability{
			advertisement: advertisement, profile: profiles[index], toolRunner: options.ToolRunner,
			malwareBundleFingerprint: options.MalwareBundleFingerprint, now: options.Now,
		})
	}
	result, err := NewWorkerCapabilitySet(capabilities)
	if err != nil {
		return nil, err
	}
	return result, nil
}

type productionWorkerCapability struct {
	advertisement            CapabilityAdvertisement
	profile                  capabilityspec.Profile
	toolRunner               ProductionToolRunner
	malwareBundleFingerprint string
	now                      func() time.Time
}

func (capability productionWorkerCapability) Advertisement() CapabilityAdvertisement {
	return capability.advertisement
}

func (capability productionWorkerCapability) Execute(ctx context.Context, job WorkerCapabilityJob) ([]WorkerCapabilityArtifact, error) {
	if ctx == nil || job.Input == nil || ValidateCanonicalParametersV1(job.Parameters) != nil || capability.profile.Validate() != nil {
		return nil, ErrProtocolInvalid
	}
	switch capability.profile.Capability {
	case capabilityspec.CapabilityImageThumbnail, capabilityspec.CapabilityImageOCR, capabilityspec.CapabilityDocumentConvert,
		capabilityspec.CapabilityMalwareScan, capabilityspec.CapabilityMediaProbe, capabilityspec.CapabilityMediaTranscode:
		return capability.executeExternal(ctx, job)
	case capabilityspec.CapabilityArchiveInspect, capabilityspec.CapabilityArchiveExtractEntry:
		if compressedTARMedia(job.Input.Info().MediaType) {
			return capability.executeCompressedTAR(ctx, job)
		}
	}
	source, err := readProductionCapabilityInput(ctx, job.Input, capability.profile.Limits.MaxInputBytes)
	if err != nil {
		return nil, err
	}
	switch capability.profile.Capability {
	case capabilityspec.CapabilityImageThumbnail:
		return capability.executeThumbnail(ctx, source, job)
	case capabilityspec.CapabilityTextExtract:
		return capability.executeText(source, job)
	case capabilityspec.CapabilityArchiveInspect:
		return capability.executeArchiveInspect(source, job)
	case capabilityspec.CapabilityArchiveExtractEntry:
		return capability.executeArchiveExtract(source, job)
	case capabilityspec.CapabilitySecretClassify:
		return capability.executeSecretClassification(source, job)
	default:
		return nil, ErrProtocolInvalid
	}
}

func (capability productionWorkerCapability) executeSecretClassification(
	source []byte,
	job WorkerCapabilityJob,
) ([]WorkerCapabilityArtifact, error) {
	info := job.Input.Info()
	if info.MediaType != "text/plain" || info.Size < 0 || info.Size > 16<<20 || int64(len(source)) != info.Size {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	result := workerCapabilities.SecretResult{Sensitivity: workerCapabilities.SensitivityUnknown}
	if info.FingerprintStrong {
		result = workerCapabilities.ClassifySecret(source, true)
	}
	categories := append([]string(nil), result.Categories...)
	if categories == nil {
		categories = []string{}
	}
	metadata, err := json.Marshal(struct {
		SchemaVersion int                            `json:"schema_version"`
		Sensitivity   workerCapabilities.Sensitivity `json:"sensitivity"`
		Categories    []string                       `json:"categories"`
	}{SchemaVersion: 1, Sensitivity: result.Sensitivity, Categories: categories})
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, job.Parameters.MaxOutputBytes)
	if err != nil || int64(len(metadata)) > maximumOutput {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	completeness := ArtifactComplete
	if result.Sensitivity == workerCapabilities.SensitivityUnknown {
		completeness = ArtifactPartial
	}
	return []WorkerCapabilityArtifact{
		productionArtifact(0, ArtifactRoleMetadata, "application/json", completeness, metadata),
	}, nil
}

func (capability productionWorkerCapability) executeExternal(ctx context.Context, job WorkerCapabilityJob) ([]WorkerCapabilityArtifact, error) {
	if capability.toolRunner == nil {
		return nil, workerCapabilities.ErrSecureWorkspaceUnavailable
	}
	if err := capability.preflightExternalInput(ctx, job.Input); err != nil {
		return nil, err
	}
	if capability.profile.Capability == capabilityspec.CapabilityImageOCR {
		return capability.executeOCRExternal(ctx, job)
	}
	if capability.profile.Capability == capabilityspec.CapabilityDocumentConvert &&
		job.Input.Info().MediaType == "application/pdf" {
		return capability.executePDFExternal(ctx, job)
	}
	invocation, err := workerCapabilities.BuildInvocation(capability.profile, workerCapabilities.ToolParameters{
		Width: job.Parameters.Width, Height: job.Parameters.Height, Quality: job.Parameters.Quality,
		Language: job.Parameters.Language, MediaType: job.Input.Info().MediaType,
	})
	if err != nil {
		return nil, err
	}
	input, err := newProductionCapabilityInputReader(ctx, job.Input, capability.profile.Limits.MaxInputBytes)
	if err != nil {
		return nil, err
	}
	result, err := capability.toolRunner.RunInput(ctx, invocation, input)
	if err != nil {
		return nil, err
	}
	switch capability.profile.Capability {
	case capabilityspec.CapabilityImageThumbnail:
		return capability.thumbnailArtifacts(result, job.Parameters)
	case capabilityspec.CapabilityImageOCR:
		return capability.ocrArtifacts(result, job.Parameters)
	case capabilityspec.CapabilityMalwareScan:
		return capability.malwareArtifacts(result, job)
	case capabilityspec.CapabilityDocumentConvert:
		return capability.documentArtifacts(result, job)
	case capabilityspec.CapabilityMediaProbe:
		return capability.mediaProbeArtifacts(result, job.Parameters)
	case capabilityspec.CapabilityMediaTranscode:
		return capability.mediaPreviewArtifacts(result, job.Parameters)
	default:
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
}

func (capability productionWorkerCapability) executePDFExternal(
	ctx context.Context,
	job WorkerCapabilityJob,
) ([]WorkerCapabilityArtifact, error) {
	pageInvocation, err := workerCapabilities.BuildInvocation(capability.profile, workerCapabilities.ToolParameters{
		MediaType: "application/pdf",
	})
	if err != nil {
		return nil, err
	}
	textInvocation, err := workerCapabilities.BuildPDFTextInvocation(capability.profile)
	if err != nil {
		return nil, err
	}

	pageInput, err := newProductionCapabilityInputReader(ctx, job.Input, capability.profile.Limits.MaxInputBytes)
	if err != nil {
		return nil, err
	}
	pageResult, err := capability.toolRunner.RunInput(ctx, pageInvocation, pageInput)
	if err != nil {
		return nil, err
	}
	pages, pageBytes, err := capability.validatePDFPageResult(pageResult, job.Parameters)
	if err != nil {
		return nil, err
	}

	textInput, err := newProductionCapabilityInputReader(ctx, job.Input, capability.profile.Limits.MaxInputBytes)
	if err != nil {
		return nil, err
	}
	textResult, err := capability.toolRunner.RunInput(ctx, textInvocation, textInput)
	if err != nil {
		return nil, err
	}
	text, err := capability.validatePDFTextResult(textResult, job.Parameters)
	if err != nil {
		return nil, err
	}
	return capability.pdfArtifacts(pages, text, pageBytes, job.Parameters)
}

func (capability productionWorkerCapability) validatePDFPageResult(
	result workerCapabilities.ToolResult,
	parameters CanonicalParametersV1,
) ([][]byte, int64, error) {
	maximumPages := min(capability.profile.Limits.MaxRenderedPages, parameters.MaxPages)
	if !validClosedPDFToolResult(result) || len(result.Outputs) == 0 || int64(len(result.Outputs)) > maximumPages {
		return nil, 0, workerCapabilities.ErrInvalidToolOutput
	}
	pages := make([][]byte, len(result.Outputs))
	for name, content := range result.Outputs {
		page, ok := canonicalPDFPageNumber(name)
		if !ok || page > len(pages) || pages[page-1] != nil {
			return nil, 0, workerCapabilities.ErrInvalidToolOutput
		}
		pages[page-1] = content
	}
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, parameters.MaxOutputBytes)
	maximumPixels := min(capability.profile.Limits.MaxPixels, parameters.MaxPixels)
	totalBytes := int64(0)
	for _, content := range pages {
		if content == nil || int64(len(content)) > maximumOutput-totalBytes {
			return nil, 0, workerCapabilities.ErrInvalidToolOutput
		}
		if _, err := workerCapabilities.ValidateRasterOutput(content, "image/png", maximumOutput, maximumPixels, 1); err != nil {
			return nil, 0, workerCapabilities.ErrInvalidToolOutput
		}
		totalBytes += int64(len(content))
	}
	return pages, totalBytes, nil
}

func (capability productionWorkerCapability) validatePDFTextResult(
	result workerCapabilities.ToolResult,
	parameters CanonicalParametersV1,
) ([]byte, error) {
	content, ok := result.Outputs["content.txt"]
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, parameters.MaxOutputBytes)
	if !validClosedPDFToolResult(result) || !ok || len(result.Outputs) != 1 ||
		int64(len(content)) > maximumOutput || !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	return content, nil
}

func (capability productionWorkerCapability) pdfArtifacts(
	pages [][]byte,
	text []byte,
	pageBytes int64,
	parameters CanonicalParametersV1,
) ([]WorkerCapabilityArtifact, error) {
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, parameters.MaxOutputBytes)
	maximumCount := min(capability.profile.Limits.MaxOutputCount, parameters.MaxOutputCount)
	if len(pages)+2 > maximumCount || int64(len(text)) > maximumOutput-pageBytes {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	metadata, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		Coverage      string `json:"coverage"`
		RenderedPages int    `json:"rendered_pages"`
	}{SchemaVersion: 1, Coverage: "partial", RenderedPages: len(pages)})
	totalBytes := pageBytes + int64(len(text))
	if err != nil || int64(len(metadata)) > maximumOutput-totalBytes {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}

	artifacts := make([]WorkerCapabilityArtifact, 0, len(pages)+2)
	for _, content := range pages {
		artifacts = append(artifacts, productionArtifact(len(artifacts), ArtifactRoleThumbnail, "image/png", ArtifactPartial, content))
	}
	artifacts = append(artifacts, productionArtifact(len(artifacts), ArtifactRoleContent, "text/plain", ArtifactPartial, text))
	artifacts = append(artifacts, productionArtifact(len(artifacts), ArtifactRoleMetadata, "application/json", ArtifactPartial, metadata))
	return artifacts, nil
}

func validClosedPDFToolResult(result workerCapabilities.ToolResult) bool {
	return result.ExitCode == 0 && result.Stdout == "" && result.Stderr == "" &&
		!result.StdoutTruncated && !result.StderrTruncated
}

func canonicalPDFPageNumber(name string) (int, bool) {
	if !strings.HasPrefix(name, "page-") || !strings.HasSuffix(name, ".png") {
		return 0, false
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(name, "page-"), ".png")
	page, err := strconv.Atoi(encoded)
	return page, err == nil && page > 0 && strconv.Itoa(page) == encoded
}

func (capability productionWorkerCapability) preflightExternalInput(ctx context.Context, input WorkerCapabilityInput) error {
	if capability.profile.Capability == capabilityspec.CapabilityMalwareScan {
		return nil
	}
	info := input.Info()
	if !info.Range || info.Size <= 0 || info.Size > capability.profile.Limits.MaxInputBytes {
		return workerCapabilities.ErrInputLimit
	}
	readRange := func(offset, length int64) ([]byte, error) {
		payload, err := input.ReadRange(ctx, offset, length)
		if err != nil || int64(len(payload)) != length {
			if err != nil {
				return nil, err
			}
			return nil, workerCapabilities.ErrInvalidToolOutput
		}
		return payload, nil
	}
	switch capability.profile.Capability {
	case capabilityspec.CapabilityImageThumbnail, capabilityspec.CapabilityImageOCR:
		payload, err := readProductionCapabilityInput(ctx, input, capability.profile.Limits.MaxInputBytes)
		if err != nil {
			return err
		}
		maxFrames := int(capability.profile.Limits.MaxFrames)
		if maxFrames == 0 {
			maxFrames = int(capability.profile.Limits.MaxPages)
		}
		_, err = workerCapabilities.InspectRaster(
			payload, info.MediaType, capability.profile.Limits.MaxInputBytes,
			capability.profile.Limits.MaxPixels, maxFrames,
		)
		return err
	case capabilityspec.CapabilityDocumentConvert:
		payload, err := readProductionCapabilityInput(ctx, input, capability.profile.Limits.MaxInputBytes)
		if err != nil {
			return err
		}
		_, err = workerCapabilities.PlanDocument(payload, info.MediaType)
		return err
	case capabilityspec.CapabilityMediaProbe:
		prefix, err := readRange(0, min(info.Size, int64(64<<10)))
		if err != nil {
			return err
		}
		_, err = workerCapabilities.PlanMedia(prefix, info.MediaType, workerCapabilities.MediaProbe)
		return err
	case capabilityspec.CapabilityMediaTranscode:
		prefix, err := readRange(0, min(info.Size, int64(64<<10)))
		if err != nil {
			return err
		}
		_, err = workerCapabilities.PlanMedia(prefix, info.MediaType, workerCapabilities.MediaPreview)
		return err
	default:
		return workerCapabilities.ErrInvalidInvocation
	}
}

func (capability productionWorkerCapability) executeOCRExternal(
	ctx context.Context,
	job WorkerCapabilityJob,
) ([]WorkerCapabilityArtifact, error) {
	input, err := newProductionCapabilityInputReader(ctx, job.Input, capability.profile.Limits.MaxInputBytes)
	if err != nil {
		return nil, err
	}
	mediaType := job.Input.Info().MediaType
	var toolInput io.Reader = input
	if mediaType == "image/webp" || mediaType == "image/tiff" {
		normalize, err := workerCapabilities.BuildRasterNormalizeInvocation(capability.profile, mediaType)
		if err != nil {
			return nil, err
		}
		result, err := capability.toolRunner.RunInput(ctx, normalize, toolInput)
		if err != nil {
			return nil, err
		}
		normalized, ok := result.Outputs["normalized.png"]
		if !ok || len(result.Outputs) != 1 || result.ExitCode != 0 || result.Stdout != "" || result.Stderr != "" ||
			result.StdoutTruncated || result.StderrTruncated {
			return nil, workerCapabilities.ErrInvalidToolOutput
		}
		if _, err := workerCapabilities.ValidateRasterOutput(
			normalized, "image/png", capability.profile.Limits.MaxOutputBytes,
			capability.profile.Limits.MaxPixels, 1,
		); err != nil {
			return nil, workerCapabilities.ErrInvalidToolOutput
		}
		toolInput = bytes.NewReader(normalized)
		mediaType = "image/png"
	}
	invocation, err := workerCapabilities.BuildInvocation(capability.profile, workerCapabilities.ToolParameters{
		Language: job.Parameters.Language, MediaType: mediaType,
	})
	if err != nil {
		return nil, err
	}
	result, err := capability.toolRunner.RunInput(ctx, invocation, toolInput)
	if err != nil {
		return nil, err
	}
	return capability.ocrArtifacts(result, job.Parameters)
}

func (capability productionWorkerCapability) thumbnailArtifacts(
	result workerCapabilities.ToolResult,
	parameters CanonicalParametersV1,
) ([]WorkerCapabilityArtifact, error) {
	content, ok := result.Outputs["thumbnail.png"]
	if !ok || len(result.Outputs) != 1 || result.ExitCode != 0 || result.Stdout != "" || result.Stderr != "" ||
		result.StdoutTruncated || result.StderrTruncated {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, parameters.MaxOutputBytes)
	info, err := workerCapabilities.ValidateRasterOutput(
		content, "image/png", maximumOutput, capability.profile.Limits.MaxPixels, 1,
	)
	if err != nil || info.Width > parameters.Width || info.Height > parameters.Height {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	metadata, err := json.Marshal(struct {
		SchemaVersion int `json:"schema_version"`
		Width         int `json:"width"`
		Height        int `json:"height"`
	}{SchemaVersion: 1, Width: info.Width, Height: info.Height})
	if err != nil || int64(len(content)+len(metadata)) > maximumOutput {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	return []WorkerCapabilityArtifact{
		productionArtifact(0, ArtifactRoleThumbnail, "image/png", ArtifactComplete, content),
		productionArtifact(1, ArtifactRoleMetadata, "application/json", ArtifactComplete, metadata),
	}, nil
}

func (capability productionWorkerCapability) documentArtifacts(
	result workerCapabilities.ToolResult,
	job WorkerCapabilityJob,
) ([]WorkerCapabilityArtifact, error) {
	if result.ExitCode != 0 || result.StdoutTruncated || result.StderrTruncated {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	names := make([]string, 0, len(result.Outputs))
	for name := range result.Outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	artifacts := make([]WorkerCapabilityArtifact, 0, len(names)+1)
	totalBytes := int64(0)
	pages := 0
	for _, name := range names {
		content := result.Outputs[name]
		totalBytes += int64(len(content))
		switch {
		case strings.HasPrefix(name, "page-") && strings.HasSuffix(name, ".png"):
			page, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "page-"), ".png"))
			config, format, decodeErr := image.DecodeConfig(bytes.NewReader(content))
			pixels, pixelsOK := checkedPixelProduct(config.Width, config.Height)
			if err != nil || decodeErr != nil || format != "png" || page != pages+1 || page > int(capability.profile.Limits.MaxRenderedPages) ||
				config.Width <= 0 || config.Height <= 0 || !pixelsOK || pixels > capability.profile.Limits.MaxPixels {
				return nil, workerCapabilities.ErrInvalidToolOutput
			}
			artifacts = append(artifacts, productionArtifact(len(artifacts), ArtifactRoleThumbnail, "image/png", ArtifactPartial, content))
			pages++
		case name == "content.txt":
			if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
				return nil, workerCapabilities.ErrInvalidToolOutput
			}
			artifacts = append(artifacts, productionArtifact(len(artifacts), ArtifactRoleContent, "text/plain", ArtifactPartial, content))
		case name == "input.pdf":
			if _, err := workerCapabilities.PlanDocument(content, "application/pdf"); err != nil {
				return nil, workerCapabilities.ErrInvalidToolOutput
			}
			artifacts = append(artifacts, productionArtifact(len(artifacts), ArtifactRoleContent, "application/pdf", ArtifactPartial, content))
		default:
			return nil, workerCapabilities.ErrInvalidToolOutput
		}
	}
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, job.Parameters.MaxOutputBytes)
	maximumCount := min(capability.profile.Limits.MaxOutputCount, job.Parameters.MaxOutputCount)
	if len(artifacts) == 0 || len(artifacts)+1 > maximumCount || totalBytes > maximumOutput {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	metadata, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		Coverage      string `json:"coverage"`
		RenderedPages int    `json:"rendered_pages"`
	}{SchemaVersion: 1, Coverage: "partial", RenderedPages: pages})
	if err != nil || totalBytes+int64(len(metadata)) > maximumOutput {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	artifacts = append(artifacts, productionArtifact(len(artifacts), ArtifactRoleMetadata, "application/json", ArtifactPartial, metadata))
	return artifacts, nil
}

type rawMediaProbe struct {
	Programs     []struct{} `json:"programs"`
	StreamGroups []struct{} `json:"stream_groups"`
	Streams      []struct {
		Index     int    `json:"index"`
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width,omitempty"`
		Height    int    `json:"height,omitempty"`
		Duration  string `json:"duration,omitempty"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func (capability productionWorkerCapability) mediaProbeArtifacts(
	result workerCapabilities.ToolResult,
	parameters CanonicalParametersV1,
) ([]WorkerCapabilityArtifact, error) {
	if result.ExitCode != 0 || result.StdoutTruncated || result.StderrTruncated || result.Stderr != "" ||
		len(result.Outputs) != 0 || len(result.Stdout) > 1<<20 {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	decoder := json.NewDecoder(strings.NewReader(result.Stdout))
	decoder.DisallowUnknownFields()
	var raw rawMediaProbe
	if decoder.Decode(&raw) != nil || ensureJSONEOF(decoder) != nil || raw.Programs == nil || len(raw.Programs) != 0 ||
		raw.StreamGroups == nil || len(raw.StreamGroups) != 0 || len(raw.Streams) == 0 ||
		len(raw.Streams) > int(capability.profile.Limits.MaxStreams) {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	durationMillis, err := parseMediaDuration(raw.Format.Duration)
	if err != nil || durationMillis > capability.profile.Limits.MaxDurationMillis || durationMillis > parameters.MaxDurationMillis {
		return nil, workerCapabilities.ErrInputLimit
	}
	type streamDTO struct {
		Index    int    `json:"index"`
		Kind     string `json:"kind"`
		Codec    string `json:"codec"`
		Width    int    `json:"width,omitempty"`
		Height   int    `json:"height,omitempty"`
		Duration int64  `json:"duration_millis,omitempty"`
	}
	streams := make([]streamDTO, 0, len(raw.Streams))
	for _, stream := range raw.Streams {
		pixels, pixelsOK := checkedPixelProduct(stream.Width, stream.Height)
		if stream.Index < 0 || !closedMediaCodec(stream.CodecType, stream.CodecName) || stream.Width < 0 || stream.Height < 0 ||
			stream.Width > 3840 || stream.Height > 2160 || !pixelsOK || pixels > capability.profile.Limits.MaxPixels {
			return nil, workerCapabilities.ErrInvalidToolOutput
		}
		streamDuration := int64(0)
		if stream.Duration != "" {
			streamDuration, err = parseMediaDuration(stream.Duration)
			if err != nil || streamDuration > durationMillis {
				return nil, workerCapabilities.ErrInvalidToolOutput
			}
		}
		streams = append(streams, streamDTO{
			Index: stream.Index, Kind: stream.CodecType, Codec: stream.CodecName,
			Width: stream.Width, Height: stream.Height, Duration: streamDuration,
		})
	}
	metadata, err := json.Marshal(struct {
		SchemaVersion  int         `json:"schema_version"`
		DurationMillis int64       `json:"duration_millis"`
		Streams        []streamDTO `json:"streams"`
	}{SchemaVersion: 1, DurationMillis: durationMillis, Streams: streams})
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, parameters.MaxOutputBytes)
	if err != nil || int64(len(metadata)) > maximumOutput {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	return []WorkerCapabilityArtifact{
		productionArtifact(0, ArtifactRoleMetadata, "application/json", ArtifactComplete, metadata),
	}, nil
}

func parseMediaDuration(value string) (int64, error) {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > 24*60*60 {
		return 0, workerCapabilities.ErrInvalidToolOutput
	}
	return int64(math.Round(seconds * 1000)), nil
}

func closedMediaCodec(kind, codec string) bool {
	switch kind {
	case "video":
		return codec == "h264" || codec == "hevc" || codec == "vp8" || codec == "vp9" || codec == "av1"
	case "audio":
		return codec == "aac" || codec == "mp3" || codec == "opus" || codec == "vorbis" || codec == "flac"
	default:
		return false
	}
}

func (capability productionWorkerCapability) mediaPreviewArtifacts(
	result workerCapabilities.ToolResult,
	parameters CanonicalParametersV1,
) ([]WorkerCapabilityArtifact, error) {
	if result.ExitCode != 0 || result.StdoutTruncated || result.StderrTruncated || len(result.Outputs) == 0 || len(result.Outputs) > 2 {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	preview, ok := result.Outputs["preview.mp4"]
	if !ok {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	if _, err := workerCapabilities.PlanMedia(preview, "video/mp4", workerCapabilities.MediaProbe); err != nil {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	artifacts := []WorkerCapabilityArtifact{
		productionArtifact(0, ArtifactRoleContent, "video/mp4", ArtifactPartial, preview),
	}
	totalBytes := int64(len(preview))
	if poster, exists := result.Outputs["poster.png"]; exists {
		config, format, err := image.DecodeConfig(bytes.NewReader(poster))
		pixels, pixelsOK := checkedPixelProduct(config.Width, config.Height)
		if err != nil || format != "png" || config.Width <= 0 || config.Height <= 0 ||
			!pixelsOK || pixels > capability.profile.Limits.MaxPixels {
			return nil, workerCapabilities.ErrInvalidToolOutput
		}
		totalBytes += int64(len(poster))
		artifacts = append(artifacts, productionArtifact(len(artifacts), ArtifactRoleThumbnail, "image/png", ArtifactPartial, poster))
	}
	metadata, err := json.Marshal(struct {
		SchemaVersion  int    `json:"schema_version"`
		Coverage       string `json:"coverage"`
		DurationMillis int64  `json:"duration_millis"`
	}{SchemaVersion: 1, Coverage: "partial", DurationMillis: min(parameters.MaxDurationMillis, capability.profile.Limits.MaxDurationMillis)})
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, parameters.MaxOutputBytes)
	if err != nil || totalBytes+int64(len(metadata)) > maximumOutput || len(artifacts)+1 > min(capability.profile.Limits.MaxOutputCount, parameters.MaxOutputCount) {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	artifacts = append(artifacts, productionArtifact(len(artifacts), ArtifactRoleMetadata, "application/json", ArtifactPartial, metadata))
	return artifacts, nil
}

func (capability productionWorkerCapability) malwareArtifacts(
	result workerCapabilities.ToolResult,
	job WorkerCapabilityJob,
) ([]WorkerCapabilityArtifact, error) {
	if capability.malwareBundleFingerprint == "" || capability.now == nil {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	state, err := parseClamScanResult(result)
	if err != nil {
		return nil, err
	}
	category := ""
	if state == capabilityspec.ScanFinding {
		category = "malware"
	}
	metadataValue := capabilityspec.MalwareResult{
		SchemaVersion: 1, EngineFamily: "clamav", SignatureBundleFingerprint: capability.malwareBundleFingerprint,
		Result: state, FindingCategory: category, ScannedBytes: job.Input.Info().Size,
		Completeness: capabilityspec.CoverageComplete, ScannedAt: capability.now().UTC().Format(time.RFC3339),
	}
	if metadataValue.Validate() != nil {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	metadata, err := json.Marshal(metadataValue)
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, job.Parameters.MaxOutputBytes)
	if err != nil || int64(len(metadata)) > maximumOutput {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	return []WorkerCapabilityArtifact{
		productionArtifact(0, ArtifactRoleMetadata, "application/json", ArtifactComplete, metadata),
	}, nil
}

func parseClamScanResult(result workerCapabilities.ToolResult) (capabilityspec.ScanState, error) {
	if result.StdoutTruncated || result.StderrTruncated || result.Stderr != "" || len(result.Outputs) != 0 {
		return "", workerCapabilities.ErrInvalidToolOutput
	}
	if result.ExitCode == 0 {
		if result.Stdout != "" {
			return "", workerCapabilities.ErrInvalidToolOutput
		}
		return capabilityspec.ScanNoFinding, nil
	}
	if result.ExitCode != 1 {
		return "", workerCapabilities.ErrInvalidToolOutput
	}
	const (
		prefix                  = "input.bin: "
		suffix                  = " FOUND\n"
		maximumSignatureBytes   = 128
		limitExceededNamePrefix = "Heuristics.Limits.Exceeded."
	)
	if !strings.HasPrefix(result.Stdout, prefix) || !strings.HasSuffix(result.Stdout, suffix) ||
		strings.Count(result.Stdout, "\n") != 1 {
		return "", workerCapabilities.ErrInvalidToolOutput
	}
	signature := strings.TrimSuffix(strings.TrimPrefix(result.Stdout, prefix), suffix)
	if len(signature) == 0 || len(signature) > maximumSignatureBytes || strings.TrimSpace(signature) != signature {
		return "", workerCapabilities.ErrInvalidToolOutput
	}
	for _, character := range []byte(signature) {
		if character < 0x20 || character > 0x7e {
			return "", workerCapabilities.ErrInvalidToolOutput
		}
	}
	if strings.HasPrefix(signature, limitExceededNamePrefix) {
		return "", workerCapabilities.ErrInputLimit
	}
	return capabilityspec.ScanFinding, nil
}

func (capability productionWorkerCapability) ocrArtifacts(
	result workerCapabilities.ToolResult,
	parameters CanonicalParametersV1,
) ([]WorkerCapabilityArtifact, error) {
	content, ok := result.Outputs["ocr.txt"]
	if !ok || len(result.Outputs) != 1 || result.ExitCode != 0 || result.Stdout != "" || result.Stderr != "" ||
		result.StdoutTruncated || result.StderrTruncated || !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, parameters.MaxOutputBytes)
	if int64(len(content)) > maximumOutput {
		return nil, workerCapabilities.ErrInputLimit
	}
	metadata, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		Coverage      string `json:"coverage"`
		Language      string `json:"language"`
	}{SchemaVersion: 1, Coverage: "complete", Language: parameters.Language})
	if err != nil || int64(len(content)+len(metadata)) > maximumOutput {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	return []WorkerCapabilityArtifact{
		productionArtifact(0, ArtifactRoleOCR, "text/plain", ArtifactComplete, content),
		productionArtifact(1, ArtifactRoleMetadata, "application/json", ArtifactComplete, metadata),
	}, nil
}

type productionCapabilityInputReader struct {
	ctx     context.Context
	input   WorkerCapabilityInput
	info    WorkerInputSourceInfo
	offset  int64
	maximum int64
}

func newProductionCapabilityInputReader(
	ctx context.Context,
	input WorkerCapabilityInput,
	maximum int64,
) (*productionCapabilityInputReader, error) {
	info := input.Info()
	if info.Size < 0 || info.Size > maximum || !info.Sequential && !info.Range {
		return nil, workerCapabilities.ErrInputLimit
	}
	return &productionCapabilityInputReader{ctx: ctx, input: input, info: info, maximum: maximum}, nil
}

func (reader *productionCapabilityInputReader) Read(payload []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if reader.offset >= reader.info.Size {
		return 0, io.EOF
	}
	length := min(int64(len(payload)), reader.info.Size-reader.offset, int64(1<<20))
	if length <= 0 {
		return 0, io.ErrNoProgress
	}
	var (
		chunk []byte
		err   error
	)
	if reader.info.Range {
		chunk, err = reader.input.ReadRange(reader.ctx, reader.offset, length)
	} else {
		chunk, err = reader.input.ReadSequential(reader.ctx, length)
	}
	if err != nil {
		return 0, err
	}
	if len(chunk) == 0 || int64(len(chunk)) > length || reader.offset+int64(len(chunk)) > reader.maximum {
		return 0, io.ErrUnexpectedEOF
	}
	copy(payload, chunk)
	reader.offset += int64(len(chunk))
	return len(chunk), nil
}

func (capability productionWorkerCapability) executeThumbnail(
	ctx context.Context,
	source []byte,
	job WorkerCapabilityJob,
) ([]WorkerCapabilityArtifact, error) {
	result, err := workerCapabilities.Thumbnail(ctx, source, job.Input.Info().MediaType, workerCapabilities.ImageOptions{
		Width: job.Parameters.Width, Height: job.Parameters.Height,
	})
	if err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(struct {
		SchemaVersion int `json:"schema_version"`
		Width         int `json:"width"`
		Height        int `json:"height"`
	}{SchemaVersion: 1, Width: result.Width, Height: result.Height})
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, job.Parameters.MaxOutputBytes)
	if err != nil || int64(len(result.Content)+len(metadata)) > maximumOutput {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	return []WorkerCapabilityArtifact{
		productionArtifact(0, ArtifactRoleThumbnail, result.MediaType, ArtifactComplete, result.Content),
		productionArtifact(1, ArtifactRoleMetadata, "application/json", ArtifactComplete, metadata),
	}, nil
}

func (capability productionWorkerCapability) executeText(source []byte, job WorkerCapabilityJob) ([]WorkerCapabilityArtifact, error) {
	result, err := workerCapabilities.ExtractText(source, job.Input.Info().MediaType, workerCapabilities.TextLimits{
		MaxBytes: capability.profile.Limits.MaxInputBytes,
		MaxRunes: int(capability.profile.Limits.MaxRunes),
		MaxLines: int(capability.profile.Limits.MaxLines),
	})
	if err != nil {
		return nil, err
	}
	content := []byte(result.Text)
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, job.Parameters.MaxOutputBytes)
	if int64(len(content)) > maximumOutput {
		return nil, workerCapabilities.ErrInputLimit
	}
	metadata, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		Coverage      string `json:"coverage"`
		Truncated     bool   `json:"truncated"`
		InputBytes    int64  `json:"input_bytes"`
		Runes         int    `json:"runes"`
		Lines         int    `json:"lines"`
	}{
		SchemaVersion: 1, Coverage: string(result.Coverage), Truncated: result.Truncated,
		InputBytes: result.InputBytes, Runes: result.Runes, Lines: result.Lines,
	})
	if err != nil || int64(len(content)+len(metadata)) > maximumOutput {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	completeness := ArtifactComplete
	if result.Truncated {
		completeness = ArtifactPartial
	}
	return []WorkerCapabilityArtifact{
		productionArtifact(0, ArtifactRoleContent, "text/plain", completeness, content),
		productionArtifact(1, ArtifactRoleMetadata, "application/json", completeness, metadata),
	}, nil
}

func (capability productionWorkerCapability) executeArchiveInspect(source []byte, job WorkerCapabilityJob) ([]WorkerCapabilityArtifact, error) {
	index, err := workerCapabilities.InspectArchive(source, job.Input.Info().MediaType, capability.archiveLimits(job, false))
	if err != nil {
		return nil, err
	}
	return capability.archiveIndexArtifacts(index, job)
}

func (capability productionWorkerCapability) archiveIndexArtifacts(
	index workerCapabilities.ArchiveIndex,
	job WorkerCapabilityJob,
) ([]WorkerCapabilityArtifact, error) {
	type archiveEntryDTO struct {
		ID          string `json:"id"`
		ParentID    string `json:"parent_id,omitempty"`
		DisplayName string `json:"display_name"`
		Size        int64  `json:"size"`
		MediaType   string `json:"media_type"`
	}
	metadataValue := struct {
		SchemaVersion int               `json:"schema_version"`
		Entries       []archiveEntryDTO `json:"entries"`
		ExpandedBytes int64             `json:"expanded_bytes"`
		Complete      bool              `json:"complete"`
	}{SchemaVersion: 1, Entries: make([]archiveEntryDTO, 0, len(index.Entries)), ExpandedBytes: index.ExpandedBytes, Complete: index.Complete}
	for _, entry := range index.Entries {
		metadataValue.Entries = append(metadataValue.Entries, archiveEntryDTO{
			ID: entry.ID, ParentID: entry.ParentID, DisplayName: entry.DisplayName, Size: entry.Size, MediaType: entry.MediaType,
		})
	}
	metadata, err := json.Marshal(metadataValue)
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, job.Parameters.MaxOutputBytes)
	if err != nil || int64(len(metadata)) > maximumOutput {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	return []WorkerCapabilityArtifact{
		productionArtifact(0, ArtifactRoleMetadata, "application/json", ArtifactComplete, metadata),
	}, nil
}

func (capability productionWorkerCapability) executeArchiveExtract(source []byte, job WorkerCapabilityJob) ([]WorkerCapabilityArtifact, error) {
	if job.Parameters.MemberStart != job.Parameters.MemberEnd {
		return nil, workerCapabilities.ErrArchiveMember
	}
	limits := capability.archiveLimits(job, true)
	index, err := workerCapabilities.InspectArchive(source, job.Input.Info().MediaType, limits)
	if err != nil {
		return nil, err
	}
	var member *workerCapabilities.ArchiveEntry
	for entryIndex := range index.Entries {
		if index.Entries[entryIndex].Ordinal == job.Parameters.MemberStart {
			member = &index.Entries[entryIndex]
			break
		}
	}
	if member == nil {
		return nil, workerCapabilities.ErrArchiveMember
	}
	content, mediaType, err := workerCapabilities.ExtractArchiveEntry(
		source, job.Input.Info().MediaType, member.ID, limits,
	)
	if err != nil {
		return nil, err
	}
	mediaType = canonicalArchiveMemberMedia(mediaType)
	if !profileAllowsOutput(capability.profile, "content", mediaType) {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	return capability.archiveMemberArtifacts(*member, content, mediaType, job)
}

func (capability productionWorkerCapability) archiveMemberArtifacts(
	member workerCapabilities.ArchiveEntry,
	content []byte,
	mediaType string,
	job WorkerCapabilityJob,
) ([]WorkerCapabilityArtifact, error) {
	metadata, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		MemberID      string `json:"member_id"`
		DisplayName   string `json:"display_name"`
		Size          int64  `json:"size"`
		MediaType     string `json:"media_type"`
	}{SchemaVersion: 1, MemberID: member.ID, DisplayName: member.DisplayName, Size: int64(len(content)), MediaType: mediaType})
	maximumOutput := min(capability.profile.Limits.MaxOutputBytes, job.Parameters.MaxOutputBytes)
	if err != nil || int64(len(content)+len(metadata)) > maximumOutput {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	return []WorkerCapabilityArtifact{
		productionArtifact(0, ArtifactRoleContent, mediaType, ArtifactComplete, content),
		productionArtifact(1, ArtifactRoleMetadata, "application/json", ArtifactComplete, metadata),
	}, nil
}

func (capability productionWorkerCapability) executeCompressedTAR(
	ctx context.Context,
	job WorkerCapabilityJob,
) ([]WorkerCapabilityArtifact, error) {
	limits := capability.archiveLimits(job, capability.profile.Capability == capabilityspec.CapabilityArchiveExtractEntry)
	compressedBytes := job.Input.Info().Size
	var index workerCapabilities.ArchiveIndex
	if err := capability.consumeCompressedTAR(ctx, job, func(stream io.Reader) error {
		var inspectErr error
		index, inspectErr = workerCapabilities.InspectTARStream(stream, compressedBytes, limits)
		return inspectErr
	}); err != nil {
		return nil, err
	}
	if capability.profile.Capability == capabilityspec.CapabilityArchiveInspect {
		return capability.archiveIndexArtifacts(index, job)
	}
	if job.Parameters.MemberStart != job.Parameters.MemberEnd || !job.Input.Info().Range {
		return nil, workerCapabilities.ErrArchiveMember
	}
	var member *workerCapabilities.ArchiveEntry
	for entryIndex := range index.Entries {
		if index.Entries[entryIndex].Ordinal == job.Parameters.MemberStart {
			member = &index.Entries[entryIndex]
			break
		}
	}
	if member == nil {
		return nil, workerCapabilities.ErrArchiveMember
	}
	var (
		content   []byte
		mediaType string
	)
	if err := capability.consumeCompressedTAR(ctx, job, func(stream io.Reader) error {
		var extractErr error
		content, mediaType, extractErr = workerCapabilities.ExtractTARStream(
			stream, compressedBytes, member.ID, limits,
		)
		return extractErr
	}); err != nil {
		return nil, err
	}
	mediaType = canonicalArchiveMemberMedia(mediaType)
	if !profileAllowsOutput(capability.profile, "content", mediaType) {
		return nil, workerCapabilities.ErrInvalidToolOutput
	}
	return capability.archiveMemberArtifacts(*member, content, mediaType, job)
}

func (capability productionWorkerCapability) consumeCompressedTAR(
	ctx context.Context,
	job WorkerCapabilityJob,
	consume func(io.Reader) error,
) error {
	streamRunner, ok := capability.toolRunner.(productionToolStreamRunner)
	if !ok || consume == nil {
		return workerCapabilities.ErrSecureWorkspaceUnavailable
	}
	invocation, err := workerCapabilities.BuildArchiveDecompressInvocation(capability.profile, job.Input.Info().MediaType)
	if err != nil {
		return err
	}
	input, err := newProductionCapabilityInputReader(ctx, job.Input, capability.profile.Limits.MaxInputBytes)
	if err != nil {
		return err
	}
	magicBytes := compressedTARMagicBytes(job.Input.Info().MediaType)
	if len(magicBytes) == 0 {
		return workerCapabilities.ErrInvalidToolOutput
	}
	prefix := make([]byte, len(magicBytes))
	if _, err := io.ReadFull(input, prefix); err != nil || !bytes.Equal(prefix, magicBytes) {
		return workerCapabilities.ErrInvalidToolOutput
	}
	validation, err := newCompressedStreamValidation(
		job.Input.Info().MediaType,
		io.MultiReader(bytes.NewReader(prefix), input),
		capability.profile.Limits.MaxInputBytes,
		min(capability.profile.Limits.MaxExpandedBytes, job.Parameters.MaxExpandedBytes),
		int(capability.profile.Limits.MaxArchiveEntries),
	)
	if err != nil {
		return err
	}
	result, runErr := streamRunner.RunInputStream(ctx, invocation, validation.Input(), consume)
	validationErr := validation.Finish()
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) ||
			errors.Is(runErr, workerCapabilities.ErrInputLimit) || validationErr == nil {
			return runErr
		}
		return validationErr
	}
	if validationErr != nil {
		return validationErr
	}
	if result.ExitCode != 0 || result.Stdout != "" || result.StdoutTruncated || result.Stderr != "" ||
		result.StderrTruncated || len(result.Outputs) != 0 {
		return workerCapabilities.ErrInvalidToolOutput
	}
	return nil
}

type compressedStreamValidation struct {
	input  io.Reader
	writer *io.PipeWriter
	done   <-chan error
}

func newCompressedStreamValidation(
	mediaType string,
	input io.Reader,
	maximumInput int64,
	maximumExpanded int64,
	maximumRecords int,
) (*compressedStreamValidation, error) {
	if input == nil || maximumInput <= 0 || maximumExpanded <= 0 || maximumRecords <= 0 {
		return nil, workerCapabilities.ErrInputLimit
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		var err error
		switch mediaType {
		case "application/gzip":
			err = validateSingleGzipStream(reader, maximumExpanded)
		case "application/x-xz":
			err = validateSingleXZStream(reader, maximumInput, maximumRecords)
		case "application/zstd":
			err = validateSingleZstdFrame(reader)
		default:
			err = workerCapabilities.ErrInvalidToolOutput
		}
		_ = reader.CloseWithError(err)
		done <- err
	}()
	return &compressedStreamValidation{input: io.TeeReader(input, writer), writer: writer, done: done}, nil
}

func (validation *compressedStreamValidation) Input() io.Reader {
	if validation == nil {
		return nil
	}
	return validation.input
}

func (validation *compressedStreamValidation) Finish() error {
	if validation == nil || validation.writer == nil || validation.done == nil {
		return workerCapabilities.ErrInvalidToolOutput
	}
	_ = validation.writer.Close()
	return <-validation.done
}

func validateSingleGzipStream(input io.Reader, maximumExpanded int64) error {
	buffered := bufio.NewReaderSize(input, 64<<10)
	reader, err := gzip.NewReader(buffered)
	if err != nil {
		return workerCapabilities.ErrInvalidToolOutput
	}
	reader.Multistream(false)
	expanded, readErr := io.Copy(io.Discard, io.LimitReader(reader, maximumExpanded+1))
	closeErr := reader.Close()
	if expanded > maximumExpanded {
		return workerCapabilities.ErrInputLimit
	}
	if readErr != nil || closeErr != nil {
		return workerCapabilities.ErrInvalidToolOutput
	}
	if _, err := buffered.ReadByte(); !errors.Is(err, io.EOF) {
		return workerCapabilities.ErrInvalidToolOutput
	}
	return nil
}

type xzStreamObserver struct {
	maximum int64
	total   int64
	header  []byte
	tail    []byte
	tailMax int
}

func validateSingleXZStream(input io.Reader, maximumInput int64, maximumRecords int) error {
	tailMax := 32 + maximumRecords*18
	observer := &xzStreamObserver{
		maximum: maximumInput,
		header:  make([]byte, 0, 12),
		tail:    make([]byte, 0, tailMax),
		tailMax: tailMax,
	}
	if _, err := io.CopyBuffer(observer, input, make([]byte, 64<<10)); err != nil {
		if errors.Is(err, workerCapabilities.ErrInputLimit) {
			return err
		}
		return workerCapabilities.ErrInvalidToolOutput
	}
	return observer.Validate(maximumRecords)
}

func (observer *xzStreamObserver) Write(payload []byte) (int, error) {
	if observer.total > observer.maximum-int64(len(payload)) {
		return 0, workerCapabilities.ErrInputLimit
	}
	originalLength := len(payload)
	observer.total += int64(originalLength)
	if len(observer.header) < cap(observer.header) {
		needed := cap(observer.header) - len(observer.header)
		if needed > len(payload) {
			needed = len(payload)
		}
		observer.header = append(observer.header, payload[:needed]...)
	}
	if len(payload) >= observer.tailMax {
		observer.tail = append(observer.tail[:0], payload[len(payload)-observer.tailMax:]...)
		return originalLength, nil
	}
	if overflow := len(observer.tail) + len(payload) - observer.tailMax; overflow > 0 {
		copy(observer.tail, observer.tail[overflow:])
		observer.tail = observer.tail[:len(observer.tail)-overflow]
	}
	observer.tail = append(observer.tail, payload...)
	return originalLength, nil
}

func (observer *xzStreamObserver) Validate(maximumRecords int) error {
	if len(observer.header) != 12 || len(observer.tail) < 24 ||
		!bytes.Equal(observer.header[:6], []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}) ||
		binary.LittleEndian.Uint32(observer.header[8:12]) != crc32.ChecksumIEEE(observer.header[6:8]) {
		return workerCapabilities.ErrInvalidToolOutput
	}
	footer := observer.tail[len(observer.tail)-12:]
	if !bytes.Equal(footer[10:12], []byte{'Y', 'Z'}) ||
		binary.LittleEndian.Uint32(footer[:4]) != crc32.ChecksumIEEE(footer[4:10]) ||
		!bytes.Equal(footer[8:10], observer.header[6:8]) {
		return workerCapabilities.ErrInvalidToolOutput
	}
	indexSize := (uint64(binary.LittleEndian.Uint32(footer[4:8])) + 1) * 4
	if indexSize < 8 || indexSize > uint64(len(observer.tail)-12) {
		return workerCapabilities.ErrInputLimit
	}
	indexStart := len(observer.tail) - 12 - int(indexSize)
	index := observer.tail[indexStart : len(observer.tail)-12]
	if len(index) < 8 || index[0] != 0 ||
		binary.LittleEndian.Uint32(index[len(index)-4:]) != crc32.ChecksumIEEE(index[:len(index)-4]) {
		return workerCapabilities.ErrInvalidToolOutput
	}
	body := index[:len(index)-4]
	offset := 1
	records, next, ok := decodeXZVLI(body, offset)
	if !ok || records > uint64(maximumRecords) {
		return workerCapabilities.ErrInputLimit
	}
	offset = next
	var paddedBlocks uint64
	for record := uint64(0); record < records; record++ {
		unpadded, nextOffset, valid := decodeXZVLI(body, offset)
		if !valid || unpadded == 0 {
			return workerCapabilities.ErrInvalidToolOutput
		}
		offset = nextOffset
		_, nextOffset, valid = decodeXZVLI(body, offset)
		if !valid {
			return workerCapabilities.ErrInvalidToolOutput
		}
		offset = nextOffset
		padded := (unpadded + 3) &^ uint64(3)
		if padded < unpadded || paddedBlocks > ^uint64(0)-padded {
			return workerCapabilities.ErrInputLimit
		}
		paddedBlocks += padded
	}
	for ; offset < len(body); offset++ {
		if body[offset] != 0 {
			return workerCapabilities.ErrInvalidToolOutput
		}
	}
	streamSize := uint64(24) + indexSize + paddedBlocks
	if streamSize != uint64(observer.total) {
		return workerCapabilities.ErrInvalidToolOutput
	}
	return nil
}

func decodeXZVLI(payload []byte, offset int) (uint64, int, bool) {
	var value uint64
	for index := 0; index < 9 && offset+index < len(payload); index++ {
		part := payload[offset+index]
		if index == 8 && part&0x80 != 0 {
			return 0, offset, false
		}
		value |= uint64(part&0x7f) << (index * 7)
		if part&0x80 == 0 {
			if index > 0 && part == 0 {
				return 0, offset, false
			}
			return value, offset + index + 1, true
		}
	}
	return 0, offset, false
}

func validateSingleZstdFrame(input io.Reader) error {
	var magic [4]byte
	if _, err := io.ReadFull(input, magic[:]); err != nil || !bytes.Equal(magic[:], []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		return workerCapabilities.ErrInvalidToolOutput
	}
	descriptor, err := readCompressedByte(input)
	if err != nil || descriptor&0x18 != 0 {
		return workerCapabilities.ErrInvalidToolOutput
	}
	singleSegment := descriptor&0x20 != 0
	if !singleSegment {
		if _, err := readCompressedByte(input); err != nil {
			return workerCapabilities.ErrInvalidToolOutput
		}
	}
	dictionarySize := []int{0, 1, 2, 4}[descriptor&0x03]
	contentSizeFlag := int(descriptor >> 6)
	contentSizeBytes := 0
	switch contentSizeFlag {
	case 0:
		if singleSegment {
			contentSizeBytes = 1
		}
	case 1:
		contentSizeBytes = 2
	case 2:
		contentSizeBytes = 4
	case 3:
		contentSizeBytes = 8
	}
	if err := discardCompressedBytes(input, int64(dictionarySize+contentSizeBytes)); err != nil {
		return workerCapabilities.ErrInvalidToolOutput
	}
	for blocks := 0; ; blocks++ {
		if blocks >= 1_000_000 {
			return workerCapabilities.ErrInputLimit
		}
		var header [3]byte
		if _, err := io.ReadFull(input, header[:]); err != nil {
			return workerCapabilities.ErrInvalidToolOutput
		}
		value := uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16
		last := value&1 != 0
		blockType := (value >> 1) & 0x03
		blockSize := int64(value >> 3)
		if blockType == 3 || blockSize > 128<<10 {
			return workerCapabilities.ErrInvalidToolOutput
		}
		payloadBytes := blockSize
		if blockType == 1 {
			payloadBytes = 1
		}
		if err := discardCompressedBytes(input, payloadBytes); err != nil {
			return workerCapabilities.ErrInvalidToolOutput
		}
		if last {
			break
		}
	}
	if descriptor&0x04 != 0 {
		if err := discardCompressedBytes(input, 4); err != nil {
			return workerCapabilities.ErrInvalidToolOutput
		}
	}
	var trailing [1]byte
	count, err := io.ReadFull(input, trailing[:])
	if count != 0 || !errors.Is(err, io.EOF) {
		return workerCapabilities.ErrInvalidToolOutput
	}
	return nil
}

func readCompressedByte(input io.Reader) (byte, error) {
	var payload [1]byte
	_, err := io.ReadFull(input, payload[:])
	return payload[0], err
}

func discardCompressedBytes(input io.Reader, count int64) error {
	if count < 0 {
		return workerCapabilities.ErrInvalidToolOutput
	}
	read, err := io.CopyN(io.Discard, input, count)
	if err != nil || read != count {
		return workerCapabilities.ErrInvalidToolOutput
	}
	return nil
}

func (capability productionWorkerCapability) archiveLimits(
	job WorkerCapabilityJob,
	extract bool,
) workerCapabilities.ArchiveLimits {
	maxMemberBytes := capability.profile.Limits.MaxMemberBytes
	if maxMemberBytes == 0 {
		maxMemberBytes = 256 << 20
	}
	if extract {
		maxMemberBytes = min(maxMemberBytes, job.Parameters.MaxOutputBytes)
	} else {
		maxMemberBytes = min(maxMemberBytes, job.Parameters.MaxExpandedBytes)
	}
	return workerCapabilities.ArchiveLimits{
		MaxEntries: int(capability.profile.Limits.MaxArchiveEntries),
		MaxDepth:   int(capability.profile.Limits.MaxArchiveDepth),
		MaxExpandedBytes: min(
			capability.profile.Limits.MaxExpandedBytes,
			job.Parameters.MaxExpandedBytes,
		),
		MaxCompressionRatio: capability.profile.Limits.MaxCompressionRatio,
		MaxMemberBytes:      maxMemberBytes,
	}
}

func compressedTARMedia(mediaType string) bool {
	return len(compressedTARMagicBytes(mediaType)) != 0
}

func compressedTARMagicBytes(mediaType string) []byte {
	switch mediaType {
	case "application/gzip":
		return []byte{0x1f, 0x8b}
	case "application/x-xz":
		return []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}
	case "application/zstd":
		return []byte{0x28, 0xb5, 0x2f, 0xfd}
	default:
		return nil
	}
}

func canonicalArchiveMemberMedia(value string) string {
	switch {
	case value == "text/plain" || strings.HasPrefix(value, "text/plain;"):
		return "text/plain"
	case strings.HasPrefix(value, "image/png"):
		return "image/png"
	case strings.HasPrefix(value, "image/jpeg"):
		return "image/jpeg"
	case strings.HasPrefix(value, "application/pdf"):
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

func profileAllowsOutput(profile capabilityspec.Profile, role, mediaType string) bool {
	for _, output := range profile.Outputs {
		if output.Role != role {
			continue
		}
		for _, allowed := range output.MediaTypes {
			if mediaType == allowed {
				return true
			}
		}
	}
	return false
}

func readProductionCapabilityInput(ctx context.Context, input WorkerCapabilityInput, maximum int64) ([]byte, error) {
	info := input.Info()
	if info.Size < 0 || info.Size > maximum || !info.Sequential && !info.Range {
		return nil, workerCapabilities.ErrInputLimit
	}
	result := make([]byte, 0, min(info.Size, int64(1<<20)))
	const chunkSize int64 = 1 << 20
	for int64(len(result)) < info.Size {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := info.Size - int64(len(result))
		length := min(remaining, chunkSize)
		var (
			chunk []byte
			err   error
		)
		if info.Range {
			chunk, err = input.ReadRange(ctx, int64(len(result)), length)
		} else {
			chunk, err = input.ReadSequential(ctx, length)
		}
		if err != nil || len(chunk) == 0 || int64(len(chunk)) > length || int64(len(result)+len(chunk)) > maximum {
			if err != nil {
				return nil, err
			}
			return nil, ErrProtocolInvalid
		}
		result = append(result, chunk...)
	}
	return result, nil
}

var productionCoverageAll = []byte(`{"schema_version":1,"kind":"all"}`)

func productionArtifact(
	ordinal int,
	role ArtifactRole,
	mediaType string,
	completeness ArtifactCompleteness,
	content []byte,
) WorkerCapabilityArtifact {
	digest := sha256.Sum256(content)
	return WorkerCapabilityArtifact{
		Declaration: ArtifactDeclaration{
			Ordinal: ordinal, Role: role, MediaType: mediaType, PlaintextSize: int64(len(content)),
			PlaintextDigest: fmt.Sprintf("%x", digest), Completeness: completeness,
			CoverageCanonical: append([]byte(nil), productionCoverageAll...),
		},
		Content: bytes.NewReader(append([]byte(nil), content...)),
	}
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
		materializing, transitionErr := runner.client.Transition(attemptCtx, WorkerTransitionRequest{
			SchemaVersion: 1, WorkerID: runner.workerID, InstanceID: runner.config.InstanceID,
			JobID: envelope.JobID, AttemptID: envelope.AttemptID, ExpectedRevision: revision,
			To: ProcessingMaterializing,
		})
		if transitionErr != nil {
			return transitionErr
		}
		revision = materializing.Revision
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
	return runner.failAttempt(ctx, envelope, revision, mapCapabilityError(executionErr))
}

func mapCapabilityError(err error) ProcessingErrorCode {
	switch {
	case errors.Is(err, capabilityspec.ErrUnsupportedMedia):
		return ProcessingErrorUnsupportedFormat
	case errors.Is(err, workerCapabilities.ErrArchiveEncrypted):
		return ProcessingErrorEncryptedArchive
	case errors.Is(err, workerCapabilities.ErrInputLimit):
		return ProcessingErrorInputTooLarge
	case errors.Is(err, workerCapabilities.ErrSecureWorkspaceUnavailable):
		return ProcessingErrorMaterializationDisabled
	case errors.Is(err, workerCapabilities.ErrToolTimeout), errors.Is(err, context.DeadlineExceeded):
		return ProcessingErrorTimeout
	case errors.Is(err, workerCapabilities.ErrToolFailed):
		return ProcessingErrorWorkerCrash
	default:
		return ProcessingErrorInvalidOutput
	}
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
