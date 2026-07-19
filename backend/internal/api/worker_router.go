package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing"
)

type WorkerProtocolAPI interface {
	Handshake(context.Context, processing.WorkerTransportIdentity, processing.HandshakeRequest) (processing.HandshakeResult, error)
	Pull(context.Context, processing.WorkerTransportIdentity, processing.WorkerPullRequest) (processing.WorkerJobEnvelope, error)
	Heartbeat(context.Context, processing.WorkerTransportIdentity, processing.WorkerHeartbeatRequest) (processing.WorkerHeartbeatResult, error)
	Transition(context.Context, processing.WorkerTransportIdentity, processing.WorkerTransitionRequest) (processing.WorkerTransitionResult, error)
	ActivateInput(context.Context, processing.WorkerTransportIdentity, processing.WorkerInputActivateRequest) (processing.WorkerInputActivation, error)
	OpenInput(context.Context, processing.WorkerTransportIdentity, processing.WorkerInputReadRequest) (content.AttemptReadHandle, error)
	ActivateSink(context.Context, processing.WorkerTransportIdentity, processing.WorkerSinkActivateRequest) (processing.WorkerSinkActivation, error)
	UploadArtifact(context.Context, processing.WorkerTransportIdentity, processing.WorkerUploadArtifactRequest, io.Reader) (processing.UploadedArtifact, error)
	CommitManifest(context.Context, processing.WorkerTransportIdentity, processing.WorkerCommitManifestRequest) (processing.WorkerCommitManifestResult, error)
	Drain(context.Context, processing.WorkerTransportIdentity, processing.WorkerDrainRequest) error
}

type WorkerRouterConfig struct {
	JSONMaxBytes     int64
	ArtifactMaxBytes int64
	Now              func() time.Time
}

type workerTransportIdentityContextKey struct{}

func ContextWithWorkerTransportIdentity(ctx context.Context, identity processing.WorkerTransportIdentity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, workerTransportIdentityContextKey{}, identity)
}

// WorkerConnContext transfers identity established by the authenticated
// listener onto requests served by the dedicated Worker HTTP server. Plain
// connections deliberately receive no Worker identity.
func WorkerConnContext(ctx context.Context, connection net.Conn) context.Context {
	identity, ok := processing.WorkerIdentityFromConn(connection)
	if !ok {
		return ctx
	}
	return ContextWithWorkerTransportIdentity(ctx, identity)
}

func NewWorkerRouter(service WorkerProtocolAPI, config WorkerRouterConfig) (http.Handler, error) {
	if service == nil || config.JSONMaxBytes <= 0 || config.ArtifactMaxBytes <= 0 {
		return nil, processing.ErrProtocolInvalid
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	router := &workerRouter{
		service: service, config: config,
		limiter: workerIdentityLimiter{entries: make(map[string]workerIdentityRate)},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/asset-worker/handshake", router.withIdentityRate("handshake", 12, router.handshake))
	mux.HandleFunc("POST /internal/v1/asset-worker/leases/pull", router.withIdentityRate("pull", 120, router.pull))
	mux.HandleFunc("POST /internal/v1/asset-worker/jobs/{jobId}/heartbeat", router.withIdentityRate("heartbeat", 360, router.heartbeat))
	mux.HandleFunc("POST /internal/v1/asset-worker/jobs/{jobId}/transitions", router.withIdentityRate("transition", 360, router.transition))
	mux.HandleFunc("POST /internal/v1/asset-worker/jobs/{jobId}/input/activate", router.withIdentityRate("input_activate", 60, router.activateInput))
	mux.HandleFunc("POST /internal/v1/asset-worker/input-sessions/{sessionId}/ranges", router.withIdentityRate("input_read", 600, router.openInput))
	mux.HandleFunc("POST /internal/v1/asset-worker/jobs/{jobId}/sink/activate", router.withIdentityRate("sink_activate", 60, router.activateSink))
	mux.HandleFunc("POST /internal/v1/asset-worker/sink-sessions/{sessionId}/artifacts", router.withIdentityRate("artifact_upload", 120, router.uploadArtifact))
	mux.HandleFunc("POST /internal/v1/asset-worker/sink-sessions/{sessionId}/manifest", router.withIdentityRate("manifest", 60, router.commitManifest))
	mux.HandleFunc("POST /internal/v1/asset-worker/drain", router.withIdentityRate("drain", 12, router.drain))
	return mux, nil
}

type workerRouter struct {
	service WorkerProtocolAPI
	config  WorkerRouterConfig
	limiter workerIdentityLimiter
}

type workerIdentityLimiter struct {
	mu      sync.Mutex
	entries map[string]workerIdentityRate
}

type workerIdentityRate struct {
	windowStart time.Time
	count       int
}

func (router *workerRouter) withIdentityRate(label string, limit int, next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		identity, ok := workerIdentity(request)
		if !ok {
			next(response, request)
			return
		}
		allowed, retryAfter := router.limiter.allow(identity, label, limit, router.config.Now().UTC())
		if !allowed {
			response.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeWorkerError(response, http.StatusTooManyRequests, "rate_limited")
			return
		}
		next(response, request)
	}
}

func (limiter *workerIdentityLimiter) allow(identity processing.WorkerTransportIdentity, label string, limit int, now time.Time) (bool, int) {
	key := string(identity.Kind) + ":" + identity.Fingerprint + ":" + label
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	entry := limiter.entries[key]
	if entry.windowStart.IsZero() || !now.Before(entry.windowStart.Add(time.Minute)) {
		entry = workerIdentityRate{windowStart: now}
	}
	if entry.count >= limit {
		retry := int(entry.windowStart.Add(time.Minute).Sub(now).Seconds())
		if retry < 1 {
			retry = 1
		}
		limiter.entries[key] = entry
		return false, retry
	}
	entry.count++
	limiter.entries[key] = entry
	if len(limiter.entries) > 4096 {
		for candidate, value := range limiter.entries {
			if !now.Before(value.windowStart.Add(2 * time.Minute)) {
				delete(limiter.entries, candidate)
			}
		}
	}
	return true, 0
}

func (router *workerRouter) handshake(response http.ResponseWriter, request *http.Request) {
	identity, ok := workerIdentity(request)
	if !ok {
		writeWorkerError(response, http.StatusUnauthorized, "unauthenticated")
		return
	}
	status, payload, err := readWorkerBody(response, request, minWorkerBodyLimit(router.config.JSONMaxBytes, 64<<10))
	if err != nil {
		writeWorkerError(response, status, workerErrorCode(status))
		return
	}
	decoded, err := processing.DecodeHandshakeRequest(payload)
	if err != nil {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := router.service.Handshake(request.Context(), identity, decoded)
	if err != nil {
		writeMappedWorkerError(response, err)
		return
	}
	writeWorkerData(response, http.StatusOK, result)
}

func (router *workerRouter) pull(response http.ResponseWriter, request *http.Request) {
	identity, ok := workerIdentity(request)
	if !ok {
		writeWorkerError(response, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var payload processing.WorkerPullRequest
	if code, err := decodeWorkerRequest(response, request, router.config.JSONMaxBytes, &payload); err != nil {
		writeWorkerError(response, code, workerErrorCode(code))
		return
	}
	result, err := router.service.Pull(request.Context(), identity, payload)
	if err != nil {
		writeMappedWorkerError(response, err)
		return
	}
	writeWorkerData(response, http.StatusOK, result)
}

func (router *workerRouter) heartbeat(response http.ResponseWriter, request *http.Request) {
	identity, ok := workerIdentity(request)
	if !ok {
		writeWorkerError(response, http.StatusUnauthorized, "unauthenticated")
		return
	}
	if !validWorkerPathID(request.PathValue("jobId")) {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	var payload processing.WorkerHeartbeatRequest
	if code, err := decodeWorkerRequest(response, request, minWorkerBodyLimit(router.config.JSONMaxBytes, 16<<10), &payload); err != nil {
		writeWorkerError(response, code, workerErrorCode(code))
		return
	}
	result, err := router.service.Heartbeat(request.Context(), identity, payload)
	if err != nil {
		writeMappedWorkerError(response, err)
		return
	}
	writeWorkerData(response, http.StatusOK, result)
}

func (router *workerRouter) transition(response http.ResponseWriter, request *http.Request) {
	identity, ok := workerIdentity(request)
	if !ok {
		writeWorkerError(response, http.StatusUnauthorized, "unauthenticated")
		return
	}
	jobID := request.PathValue("jobId")
	if !validWorkerPathID(jobID) {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	var payload processing.WorkerTransitionRequest
	if code, err := decodeWorkerRequest(response, request, minWorkerBodyLimit(router.config.JSONMaxBytes, 16<<10), &payload); err != nil {
		writeWorkerError(response, code, workerErrorCode(code))
		return
	}
	if payload.JobID != jobID {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := router.service.Transition(request.Context(), identity, payload)
	if err != nil {
		writeMappedWorkerError(response, err)
		return
	}
	writeWorkerData(response, http.StatusOK, result)
}

func (router *workerRouter) activateInput(response http.ResponseWriter, request *http.Request) {
	identity, ok := workerIdentity(request)
	if !ok {
		writeWorkerError(response, http.StatusUnauthorized, "unauthenticated")
		return
	}
	jobID := request.PathValue("jobId")
	if !validWorkerPathID(jobID) {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	var payload processing.WorkerInputActivateRequest
	if code, err := decodeWorkerRequest(response, request, minWorkerBodyLimit(router.config.JSONMaxBytes, 8<<10), &payload); err != nil {
		writeWorkerError(response, code, workerErrorCode(code))
		return
	}
	if payload.JobID != jobID {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := router.service.ActivateInput(request.Context(), identity, payload)
	if err != nil {
		writeMappedWorkerError(response, err)
		return
	}
	writeWorkerData(response, http.StatusOK, result)
}

func (router *workerRouter) openInput(response http.ResponseWriter, request *http.Request) {
	identity, ok := workerIdentity(request)
	if !ok {
		writeWorkerError(response, http.StatusUnauthorized, "unauthenticated")
		return
	}
	sessionID := request.PathValue("sessionId")
	if !validWorkerPathID(sessionID) {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	var payload processing.WorkerInputReadRequest
	if code, err := decodeWorkerRequest(response, request, minWorkerBodyLimit(router.config.JSONMaxBytes, 8<<10), &payload); err != nil {
		writeWorkerError(response, code, workerErrorCode(code))
		return
	}
	if payload.SessionID != sessionID {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	reader, err := router.service.OpenInput(request.Context(), identity, payload)
	if err != nil {
		writeMappedWorkerError(response, err)
		return
	}
	defer func() { _ = reader.Close() }()
	response.Header().Set("Content-Type", "application/octet-stream")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(http.StatusOK)
	_, _ = io.Copy(response, reader)
}

func (router *workerRouter) activateSink(response http.ResponseWriter, request *http.Request) {
	identity, ok := workerIdentity(request)
	if !ok {
		writeWorkerError(response, http.StatusUnauthorized, "unauthenticated")
		return
	}
	jobID := request.PathValue("jobId")
	if !validWorkerPathID(jobID) {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	var payload processing.WorkerSinkActivateRequest
	if code, err := decodeWorkerRequest(response, request, minWorkerBodyLimit(router.config.JSONMaxBytes, 8<<10), &payload); err != nil {
		writeWorkerError(response, code, workerErrorCode(code))
		return
	}
	if payload.JobID != jobID {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := router.service.ActivateSink(request.Context(), identity, payload)
	if err != nil {
		writeMappedWorkerError(response, err)
		return
	}
	writeWorkerData(response, http.StatusOK, result)
}

func (router *workerRouter) uploadArtifact(response http.ResponseWriter, request *http.Request) {
	identity, ok := workerIdentity(request)
	if !ok {
		writeWorkerError(response, http.StatusUnauthorized, "unauthenticated")
		return
	}
	sessionID := request.PathValue("sessionId")
	if !validWorkerPathID(sessionID) {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	metadataLimit := minWorkerBodyLimit(router.config.JSONMaxBytes, 64<<10)
	totalLimit := router.config.ArtifactMaxBytes + metadataLimit + 64<<10
	if totalLimit < router.config.ArtifactMaxBytes {
		writeWorkerError(response, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, totalLimit)
	parts, err := request.MultipartReader()
	if err != nil {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	metadataPart, err := parts.NextPart()
	if err != nil || metadataPart.FormName() != "metadata" || metadataPart.FileName() != "" {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	metadata, err := io.ReadAll(io.LimitReader(metadataPart, metadataLimit+1))
	_ = metadataPart.Close()
	if err != nil || int64(len(metadata)) > metadataLimit {
		writeWorkerError(response, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}
	var payload processing.WorkerUploadArtifactRequest
	if err := processing.DecodeWorkerJSON(metadata, &payload); err != nil || payload.SessionID != sessionID {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	contentPart, err := parts.NextPart()
	if err != nil || contentPart.FormName() != "content" || contentPart.FileName() != "" ||
		contentPart.Header.Get("Content-Type") != "application/octet-stream" {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	content := &workerArtifactMultipartReader{
		part: contentPart, parts: parts, remaining: router.config.ArtifactMaxBytes,
	}
	result, err := router.service.UploadArtifact(request.Context(), identity, payload, content)
	if err != nil {
		_ = contentPart.Close()
		writeMappedWorkerUploadError(response, err)
		return
	}
	if err := content.Verify(); err != nil {
		_ = contentPart.Close()
		writeMappedWorkerUploadError(response, err)
		return
	}
	_ = contentPart.Close()
	writeWorkerData(response, http.StatusOK, result)
}

func (router *workerRouter) commitManifest(response http.ResponseWriter, request *http.Request) {
	identity, ok := workerIdentity(request)
	if !ok {
		writeWorkerError(response, http.StatusUnauthorized, "unauthenticated")
		return
	}
	sessionID := request.PathValue("sessionId")
	if !validWorkerPathID(sessionID) {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	var payload processing.WorkerCommitManifestRequest
	if code, err := decodeWorkerRequest(response, request, minWorkerBodyLimit(router.config.JSONMaxBytes, 64<<10), &payload); err != nil {
		writeWorkerError(response, code, workerErrorCode(code))
		return
	}
	if payload.SessionID != sessionID {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := router.service.CommitManifest(request.Context(), identity, payload)
	if err != nil {
		writeMappedWorkerError(response, err)
		return
	}
	writeWorkerData(response, http.StatusOK, result)
}

func (router *workerRouter) drain(response http.ResponseWriter, request *http.Request) {
	identity, ok := workerIdentity(request)
	if !ok {
		writeWorkerError(response, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var payload processing.WorkerDrainRequest
	if code, err := decodeWorkerRequest(response, request, minWorkerBodyLimit(router.config.JSONMaxBytes, 8<<10), &payload); err != nil {
		writeWorkerError(response, code, workerErrorCode(code))
		return
	}
	if err := router.service.Drain(request.Context(), identity, payload); err != nil {
		writeMappedWorkerError(response, err)
		return
	}
	writeWorkerData(response, http.StatusOK, struct{}{})
}

type workerArtifactMultipartReader struct {
	part        *multipart.Part
	parts       *multipart.Reader
	remaining   int64
	verified    bool
	terminalErr error
}

func (reader *workerArtifactMultipartReader) Read(payload []byte) (int, error) {
	if reader.terminalErr != nil {
		return 0, reader.terminalErr
	}
	if reader.verified {
		return 0, io.EOF
	}
	if len(payload) == 0 {
		return 0, nil
	}
	if reader.remaining == 0 {
		var probe [1]byte
		count, err := reader.part.Read(probe[:])
		if count != 0 {
			reader.terminalErr = errWorkerBodyTooLarge
			return 0, reader.terminalErr
		}
		if errors.Is(err, io.EOF) {
			return 0, reader.verifyTrailingPart()
		}
		if err != nil {
			reader.terminalErr = processing.ErrProtocolInvalid
			return 0, reader.terminalErr
		}
		return 0, nil
	}
	if int64(len(payload)) > reader.remaining {
		payload = payload[:reader.remaining]
	}
	count, err := reader.part.Read(payload)
	reader.remaining -= int64(count)
	if errors.Is(err, io.EOF) {
		trailingErr := reader.verifyTrailingPart()
		if trailingErr != nil {
			return count, trailingErr
		}
		return count, io.EOF
	}
	if err != nil {
		reader.terminalErr = processing.ErrProtocolInvalid
		return count, reader.terminalErr
	}
	return count, nil
}

func (reader *workerArtifactMultipartReader) Verify() error {
	if reader.terminalErr != nil {
		return reader.terminalErr
	}
	if reader.verified {
		return nil
	}
	_, err := io.Copy(io.Discard, reader)
	return err
}

func (reader *workerArtifactMultipartReader) verifyTrailingPart() error {
	trailing, err := reader.parts.NextPart()
	if errors.Is(err, io.EOF) {
		reader.verified = true
		return nil
	}
	if trailing != nil {
		_ = trailing.Close()
	}
	reader.terminalErr = processing.ErrProtocolInvalid
	return reader.terminalErr
}

func workerIdentity(request *http.Request) (processing.WorkerTransportIdentity, bool) {
	if request == nil {
		return processing.WorkerTransportIdentity{}, false
	}
	identity, ok := request.Context().Value(workerTransportIdentityContextKey{}).(processing.WorkerTransportIdentity)
	if !ok || identity.Fingerprint == "" || (identity.Kind != processing.WorkerTransportLocal && identity.Kind != processing.WorkerTransportMTLS) {
		return processing.WorkerTransportIdentity{}, false
	}
	return identity, true
}

var errWorkerBodyTooLarge = errors.New("worker protocol body too large")

func decodeWorkerRequest(response http.ResponseWriter, request *http.Request, maximum int64, destination any) (int, error) {
	status, payload, err := readWorkerBody(response, request, maximum)
	if err != nil {
		return status, err
	}
	if err := processing.DecodeWorkerJSON(payload, destination); err != nil {
		return http.StatusBadRequest, err
	}
	return http.StatusOK, nil
}

func readWorkerBody(response http.ResponseWriter, request *http.Request, maximum int64) (int, []byte, error) {
	if request.Body == nil {
		return http.StatusBadRequest, nil, processing.ErrProtocolInvalid
	}
	request.Body = http.MaxBytesReader(response, request.Body, maximum)
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		var maximumError *http.MaxBytesError
		if errors.As(err, &maximumError) {
			return http.StatusRequestEntityTooLarge, nil, errWorkerBodyTooLarge
		}
		return http.StatusBadRequest, nil, processing.ErrProtocolInvalid
	}
	return http.StatusOK, payload, nil
}

type workerResponse struct {
	SchemaVersion int    `json:"schema_version"`
	Code          string `json:"code"`
	Data          any    `json:"data,omitempty"`
}

func writeWorkerData(response http.ResponseWriter, status int, data any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(workerResponse{SchemaVersion: 1, Code: "ok", Data: data})
}

func writeWorkerError(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(workerResponse{SchemaVersion: 1, Code: code})
}

func writeMappedWorkerError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, processing.ErrWorkerUnauthenticated):
		writeWorkerError(response, http.StatusUnauthorized, "unauthenticated")
	case errors.Is(err, processing.ErrWorkerQuarantined):
		writeWorkerError(response, http.StatusForbidden, "forbidden")
	case errors.Is(err, processing.ErrProtocolInvalid), errors.Is(err, processing.ErrInvalidContract):
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, processing.ErrNoWork):
		response.WriteHeader(http.StatusNoContent)
	case errors.Is(err, processing.ErrAttemptLost), errors.Is(err, processing.ErrManifestFenceLost):
		writeWorkerError(response, http.StatusConflict, "fence_lost")
	case errors.Is(err, processing.ErrGrantDenied), errors.Is(err, processing.ErrRevisionConflict):
		writeWorkerError(response, http.StatusConflict, "conflict")
	default:
		writeWorkerError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}

func writeMappedWorkerUploadError(response http.ResponseWriter, err error) {
	if errors.Is(err, errWorkerBodyTooLarge) {
		writeWorkerError(response, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}
	if errors.Is(err, processing.ErrProtocolInvalid) {
		writeWorkerError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	writeMappedWorkerError(response, err)
}

func workerErrorCode(status int) string {
	if status == http.StatusRequestEntityTooLarge {
		return "body_too_large"
	}
	return "invalid_request"
}

func validWorkerPathID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func minWorkerBodyLimit(configured, fixed int64) int64 {
	if configured < fixed {
		return configured
	}
	return fixed
}
