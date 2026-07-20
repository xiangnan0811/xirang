package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrTransportUnsafe        = errors.New("updater transport unsafe")
	ErrUpdaterUnauthenticated = errors.New("updater transport unauthenticated")
	ErrTemporarilyUnavailable = errors.New("updater temporarily unavailable")
)

type UpdaterClientConfig struct {
	SocketPath     string
	JSONMaxBytes   int64
	RequestTimeout time.Duration
}

type UpdaterClient struct {
	httpClient   *http.Client
	jsonMaxBytes int64
}

func NewUpdaterClient(config UpdaterClientConfig) (*UpdaterClient, error) {
	if config.JSONMaxBytes == 0 {
		config.JSONMaxBytes = maximumProtocolBytes
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 30 * time.Second
	}
	if config.SocketPath == "" || !filepath.IsAbs(config.SocketPath) || filepath.Clean(config.SocketPath) != config.SocketPath ||
		strings.ContainsAny(config.SocketPath, "\x00\r\n") || config.JSONMaxBytes <= 0 || config.JSONMaxBytes > maximumProtocolBytes ||
		config.RequestTimeout <= 0 || config.RequestTimeout > 2*time.Minute {
		return nil, ErrTransportUnsafe
	}
	dialer := &net.Dialer{Timeout: config.RequestTimeout}
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true, ForceAttemptHTTP2: false,
		ResponseHeaderTimeout: config.RequestTimeout, IdleConnTimeout: config.RequestTimeout,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", config.SocketPath)
		},
	}
	return newUpdaterClient(&http.Client{Transport: transport, Timeout: config.RequestTimeout}, "http://asset-worker-updater.local", config.JSONMaxBytes)
}

func newUpdaterClient(httpClient *http.Client, rawBaseURL string, jsonMaxBytes int64) (*UpdaterClient, error) {
	if httpClient == nil || jsonMaxBytes <= 0 || jsonMaxBytes > maximumProtocolBytes {
		return nil, ErrTransportUnsafe
	}
	base, err := url.Parse(rawBaseURL)
	if err != nil || base.Scheme != "http" || base.Host != "asset-worker-updater.local" || base.User != nil ||
		base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
		return nil, ErrTransportUnsafe
	}
	copyClient := *httpClient
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &UpdaterClient{httpClient: &copyClient, jsonMaxBytes: jsonMaxBytes}, nil
}

func (client *UpdaterClient) RegisterCandidate(ctx context.Context, request RegisterCandidateRequest) (RegisterCandidateResult, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return RegisterCandidateResult{}, ErrProtocolInvalid
	}
	if _, err := DecodeRegisterCandidateRequest(payload); err != nil {
		return RegisterCandidateResult{}, err
	}
	var result RegisterCandidateResult
	if err := client.postJSON(ctx, "/internal/v1/asset-worker-updater/candidates", payload, &result); err != nil {
		return RegisterCandidateResult{}, err
	}
	if err := ValidateRegisterCandidateResult(result); err != nil {
		return RegisterCandidateResult{}, err
	}
	return result, nil
}

func (client *UpdaterClient) PullActivation(ctx context.Context, request PullActivationRequest) (PullActivationResult, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return PullActivationResult{}, ErrProtocolInvalid
	}
	if _, err := DecodePullActivationRequest(payload); err != nil {
		return PullActivationResult{}, err
	}
	var result PullActivationResult
	if err := client.postJSON(ctx, "/internal/v1/asset-worker-updater/activations/pull", payload, &result); err != nil {
		return PullActivationResult{}, err
	}
	if err := ValidatePullActivationResult(result); err != nil {
		return PullActivationResult{}, err
	}
	return result, nil
}

func (client *UpdaterClient) ReportActivation(ctx context.Context, request ActivationReportRequest) (ActivationReportResult, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return ActivationReportResult{}, ErrProtocolInvalid
	}
	if _, err := DecodeActivationReportRequest(payload); err != nil {
		return ActivationReportResult{}, err
	}
	var result ActivationReportResult
	if err := client.postJSON(ctx, "/internal/v1/asset-worker-updater/activations/report", payload, &result); err != nil {
		return ActivationReportResult{}, err
	}
	if err := ValidateActivationReportResult(result); err != nil {
		return ActivationReportResult{}, err
	}
	return result, nil
}

func (client *UpdaterClient) postJSON(ctx context.Context, path string, payload []byte, result any) error {
	if client == nil || client.httpClient == nil || ctx == nil || result == nil || len(payload) == 0 ||
		int64(len(payload)) > client.jsonMaxBytes || !validUpdaterRoute(path) {
		return ErrProtocolInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://asset-worker-updater.local"+path, bytes.NewReader(payload))
	if err != nil {
		return ErrProtocolInvalid
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return ErrTemporarilyUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	if response.Header.Get("Content-Type") != "application/json" {
		return ErrProtocolInvalid
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, client.jsonMaxBytes+1))
	if err != nil || int64(len(body)) > client.jsonMaxBytes {
		return ErrProtocolInvalid
	}
	var envelope updaterClientEnvelope
	if decodeProtocolJSON(body, &envelope) != nil || envelope.SchemaVersion != 1 || envelope.Code == "" {
		return ErrProtocolInvalid
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || envelope.Code != "ok" {
		if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices || envelope.Code == "ok" {
			return ErrProtocolInvalid
		}
		return updaterClientError(envelope.Code)
	}
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) || decodeProtocolJSON(envelope.Data, result) != nil {
		return ErrProtocolInvalid
	}
	return nil
}

func validUpdaterRoute(path string) bool {
	switch path {
	case "/internal/v1/asset-worker-updater/candidates",
		"/internal/v1/asset-worker-updater/activations/pull",
		"/internal/v1/asset-worker-updater/activations/report":
		return true
	default:
		return false
	}
}

type updaterClientEnvelope struct {
	SchemaVersion int             `json:"schema_version"`
	Code          string          `json:"code"`
	Data          json.RawMessage `json:"data,omitempty"`
}

func updaterClientError(code string) error {
	switch code {
	case "unauthenticated":
		return ErrUpdaterUnauthenticated
	case "invalid_request", "body_too_large", "conflict":
		return ErrProtocolInvalid
	case "temporarily_unavailable", "rate_limited", "feature_disabled":
		return ErrTemporarilyUnavailable
	default:
		return ErrProtocolInvalid
	}
}

func (client *UpdaterClient) CloseIdleConnections() {
	if client != nil && client.httpClient != nil {
		client.httpClient.CloseIdleConnections()
	}
}
