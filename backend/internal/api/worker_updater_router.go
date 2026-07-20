package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/updater"
)

type WorkerUpdaterProtocolAPI interface {
	RegisterUpdaterCandidate(context.Context, updater.UpdaterTransportIdentity, updater.RegisterCandidateRequest) (updater.RegisterCandidateResult, error)
	PullUpdaterActivation(context.Context, updater.UpdaterTransportIdentity, updater.PullActivationRequest) (updater.PullActivationResult, error)
	ReportUpdaterActivation(context.Context, updater.UpdaterTransportIdentity, updater.ActivationReportRequest) (updater.ActivationReportResult, error)
}

type WorkerUpdaterRouterConfig struct {
	JSONMaxBytes int64
	Now          func() time.Time
}

type updaterTransportIdentityContextKey struct{}

func ContextWithUpdaterTransportIdentity(ctx context.Context, identity updater.UpdaterTransportIdentity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, updaterTransportIdentityContextKey{}, identity)
}

func UpdaterConnContext(ctx context.Context, connection net.Conn) context.Context {
	identity, ok := updater.UpdaterIdentityFromConn(connection)
	if !ok {
		return ctx
	}
	return ContextWithUpdaterTransportIdentity(ctx, identity)
}

func updaterIdentityFromContext(ctx context.Context) (updater.UpdaterTransportIdentity, bool) {
	if ctx == nil {
		return updater.UpdaterTransportIdentity{}, false
	}
	identity, ok := ctx.Value(updaterTransportIdentityContextKey{}).(updater.UpdaterTransportIdentity)
	if !ok || len(identity.Fingerprint) != 64 || identity.PeerPID <= 0 {
		return updater.UpdaterTransportIdentity{}, false
	}
	return identity, true
}

func NewWorkerUpdaterRouter(service WorkerUpdaterProtocolAPI, config WorkerUpdaterRouterConfig) (http.Handler, error) {
	if service == nil || config.JSONMaxBytes <= 0 || config.JSONMaxBytes > 64<<10 {
		return nil, updater.ErrProtocolInvalid
	}
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	router := &workerUpdaterRouter{
		service: service, config: config,
		rates: make(map[string]updaterRouterRate),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/asset-worker-updater/candidates", router.handle(router.registerCandidate))
	mux.HandleFunc("POST /internal/v1/asset-worker-updater/activations/pull", router.handle(router.pullActivation))
	mux.HandleFunc("POST /internal/v1/asset-worker-updater/activations/report", router.handle(router.reportActivation))
	return mux, nil
}

type workerUpdaterRouter struct {
	service WorkerUpdaterProtocolAPI
	config  WorkerUpdaterRouterConfig
	mu      sync.Mutex
	rates   map[string]updaterRouterRate
}

type updaterRouterRate struct {
	window time.Time
	count  int
}

func (router *workerUpdaterRouter) handle(next func(http.ResponseWriter, *http.Request, updater.UpdaterTransportIdentity, []byte)) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		identity, ok := updaterIdentityFromContext(request.Context())
		if !ok {
			writeUpdaterError(response, http.StatusUnauthorized, "unauthenticated")
			return
		}
		allowed, retryAfter := router.allow(identity.Fingerprint, router.config.Now().UTC())
		if !allowed {
			response.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeUpdaterError(response, http.StatusTooManyRequests, "rate_limited")
			return
		}
		payload, status, err := readUpdaterJSON(response, request, router.config.JSONMaxBytes)
		if err != nil {
			code := "invalid_request"
			if status == http.StatusRequestEntityTooLarge {
				code = "body_too_large"
			}
			writeUpdaterError(response, status, code)
			return
		}
		next(response, request, identity, payload)
	}
}

func (router *workerUpdaterRouter) registerCandidate(response http.ResponseWriter, request *http.Request, identity updater.UpdaterTransportIdentity, payload []byte) {
	decoded, err := updater.DecodeRegisterCandidateRequest(payload)
	if err != nil {
		writeUpdaterError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := router.service.RegisterUpdaterCandidate(request.Context(), identity, decoded)
	if err != nil {
		writeMappedUpdaterError(response, err)
		return
	}
	if updater.ValidateRegisterCandidateResult(result) != nil {
		writeUpdaterError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	writeUpdaterData(response, http.StatusOK, result)
}

func (router *workerUpdaterRouter) pullActivation(response http.ResponseWriter, request *http.Request, identity updater.UpdaterTransportIdentity, payload []byte) {
	decoded, err := updater.DecodePullActivationRequest(payload)
	if err != nil {
		writeUpdaterError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := router.service.PullUpdaterActivation(request.Context(), identity, decoded)
	if err != nil {
		writeMappedUpdaterError(response, err)
		return
	}
	if updater.ValidatePullActivationResult(result) != nil {
		writeUpdaterError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	writeUpdaterData(response, http.StatusOK, result)
}

func (router *workerUpdaterRouter) reportActivation(response http.ResponseWriter, request *http.Request, identity updater.UpdaterTransportIdentity, payload []byte) {
	decoded, err := updater.DecodeActivationReportRequest(payload)
	if err != nil {
		writeUpdaterError(response, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := router.service.ReportUpdaterActivation(request.Context(), identity, decoded)
	if err != nil {
		writeMappedUpdaterError(response, err)
		return
	}
	if updater.ValidateActivationReportResult(result) != nil {
		writeUpdaterError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	writeUpdaterData(response, http.StatusOK, result)
}

func readUpdaterJSON(response http.ResponseWriter, request *http.Request, maximum int64) ([]byte, int, error) {
	if request == nil || request.Body == nil || request.URL == nil || request.URL.RawQuery != "" || len(request.TransferEncoding) != 0 {
		return nil, http.StatusBadRequest, updater.ErrProtocolInvalid
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || len(parameters) != 0 {
		return nil, http.StatusBadRequest, updater.ErrProtocolInvalid
	}
	request.Body = http.MaxBytesReader(response, request.Body, maximum)
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, http.StatusRequestEntityTooLarge, err
		}
		return nil, http.StatusBadRequest, err
	}
	if len(payload) == 0 {
		return nil, http.StatusBadRequest, updater.ErrProtocolInvalid
	}
	return payload, http.StatusOK, nil
}

func (router *workerUpdaterRouter) allow(fingerprint string, now time.Time) (bool, int) {
	router.mu.Lock()
	defer router.mu.Unlock()
	rate := router.rates[fingerprint]
	if rate.window.IsZero() || !now.Before(rate.window.Add(time.Minute)) {
		rate = updaterRouterRate{window: now}
	}
	if rate.count >= 120 {
		retry := int(rate.window.Add(time.Minute).Sub(now).Seconds())
		if retry < 1 {
			retry = 1
		}
		return false, retry
	}
	rate.count++
	router.rates[fingerprint] = rate
	return true, 0
}

type updaterRouterEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	Code          string `json:"code"`
	Data          any    `json:"data,omitempty"`
}

func writeUpdaterData(response http.ResponseWriter, status int, data any) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(updaterRouterEnvelope{SchemaVersion: 1, Code: "ok", Data: data})
}

func writeUpdaterError(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(updaterRouterEnvelope{SchemaVersion: 1, Code: code})
}

func writeMappedUpdaterError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, updater.ErrUpdaterUnauthenticated):
		writeUpdaterError(response, http.StatusUnauthorized, "unauthenticated")
	case errors.Is(err, processing.ErrProcessingDisabled):
		writeUpdaterError(response, http.StatusServiceUnavailable, "feature_disabled")
	case errors.Is(err, updater.ErrProtocolInvalid), errors.Is(err, processing.ErrInvalidContract):
		writeUpdaterError(response, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, backupasset.ErrConflict), errors.Is(err, processing.ErrRevisionConflict):
		writeUpdaterError(response, http.StatusConflict, "conflict")
	default:
		writeUpdaterError(response, http.StatusServiceUnavailable, "temporarily_unavailable")
	}
}
