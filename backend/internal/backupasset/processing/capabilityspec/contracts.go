package capabilityspec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidContract  = errors.New("invalid capability contract")
	ErrUnsupportedMedia = errors.New("unsupported capability media")
)

const MaxDiagnosticBytes = 256

type FailureCode string

const (
	FailureUnsupportedFormat       FailureCode = "unsupported_format"
	FailureEncryptedArchive        FailureCode = "encrypted_archive"
	FailureInputTooLarge           FailureCode = "input_too_large"
	FailureMaterializationDisabled FailureCode = "materialization_disabled"
	FailureQuotaBusy               FailureCode = "quota_busy"
	FailureTimeout                 FailureCode = "timeout"
	FailureWorkerCrash             FailureCode = "worker_crash"
	FailureInvalidOutput           FailureCode = "invalid_output"
	FailureDigestMismatch          FailureCode = "digest_mismatch"
	FailureSandboxViolation        FailureCode = "sandbox_violation"
	FailureNetworkViolation        FailureCode = "network_violation"
)

func (value FailureCode) valid() bool {
	switch value {
	case FailureUnsupportedFormat, FailureEncryptedArchive, FailureInputTooLarge,
		FailureMaterializationDisabled, FailureQuotaBusy, FailureTimeout,
		FailureWorkerCrash, FailureInvalidOutput, FailureDigestMismatch,
		FailureSandboxViolation, FailureNetworkViolation:
		return true
	default:
		return false
	}
}

type ReasonCode string

const (
	ReasonMIMENotAllowlisted         ReasonCode = "mime_not_allowlisted"
	ReasonActiveContentRejected      ReasonCode = "active_content_rejected"
	ReasonArchiveEncrypted           ReasonCode = "archive_encrypted"
	ReasonPageLimit                  ReasonCode = "page_limit"
	ReasonPixelLimit                 ReasonCode = "pixel_limit"
	ReasonArchiveRatioLimit          ReasonCode = "archive_ratio_limit"
	ReasonSecureWorkspaceUnavailable ReasonCode = "secure_workspace_unavailable"
	ReasonToolDeadline               ReasonCode = "tool_deadline"
	ReasonWorkerMemoryBusy           ReasonCode = "worker_memory_busy"
	ReasonTmpfsBusy                  ReasonCode = "tmpfs_busy"
	ReasonToolExit                   ReasonCode = "tool_exit"
	ReasonProcessTreeKilled          ReasonCode = "process_tree_killed"
	ReasonOutputSchemaInvalid        ReasonCode = "output_schema_invalid"
	ReasonOutputMIMEMismatch         ReasonCode = "output_mime_mismatch"
	ReasonForbiddenSyscall           ReasonCode = "forbidden_syscall"
	ReasonWorkspaceEscape            ReasonCode = "workspace_escape"
	ReasonNetworkAttempt             ReasonCode = "network_attempt"
)

func (value ReasonCode) valid() bool {
	switch value {
	case ReasonMIMENotAllowlisted, ReasonActiveContentRejected, ReasonArchiveEncrypted,
		ReasonPageLimit, ReasonPixelLimit, ReasonArchiveRatioLimit,
		ReasonSecureWorkspaceUnavailable, ReasonToolDeadline, ReasonWorkerMemoryBusy,
		ReasonTmpfsBusy, ReasonToolExit, ReasonProcessTreeKilled,
		ReasonOutputSchemaInvalid, ReasonOutputMIMEMismatch, ReasonForbiddenSyscall,
		ReasonWorkspaceEscape, ReasonNetworkAttempt:
		return true
	default:
		return false
	}
}

type Diagnostic struct {
	Failure FailureCode      `json:"failure"`
	Reason  ReasonCode       `json:"reason"`
	Params  map[string]int64 `json:"params,omitempty"`
}

func (value Diagnostic) Validate() error {
	if !value.Failure.valid() || !value.Reason.valid() || len(value.Params) > 4 {
		return ErrInvalidContract
	}
	for key, number := range value.Params {
		switch key {
		case "limit", "observed", "page", "stream":
		default:
			return ErrInvalidContract
		}
		if number < 0 {
			return ErrInvalidContract
		}
	}
	payload, err := json.Marshal(value)
	if err != nil || len(payload) > MaxDiagnosticBytes {
		return ErrInvalidContract
	}
	return nil
}

type ProcessingOutcome string

const (
	OutcomeSucceeded ProcessingOutcome = "succeeded"
	OutcomeFailed    ProcessingOutcome = "failed"
)

type ScanState string

const (
	ScanNotScanned ScanState = "not_scanned"
	ScanNoFinding  ScanState = "no_finding"
	ScanFinding    ScanState = "finding"
	ScanStale      ScanState = "stale"
)

type CoverageState string

const (
	CoverageComplete CoverageState = "complete"
	CoveragePartial  CoverageState = "partial"
)

type MalwareResult struct {
	SchemaVersion              int           `json:"schema_version"`
	EngineFamily               string        `json:"engine_family"`
	SignatureBundleFingerprint string        `json:"signature_bundle_fingerprint"`
	Result                     ScanState     `json:"result"`
	FindingCategory            string        `json:"finding_category,omitempty"`
	ScannedBytes               int64         `json:"scanned_bytes"`
	Completeness               CoverageState `json:"completeness"`
	ScannedAt                  string        `json:"scanned_at"`
}

func (value MalwareResult) Validate() error {
	if value.SchemaVersion != 1 || value.EngineFamily != "clamav" ||
		!lowerHex(value.SignatureBundleFingerprint, 64) || value.ScannedBytes < 0 ||
		(value.Completeness != CoverageComplete && value.Completeness != CoveragePartial) {
		return ErrInvalidContract
	}
	switch value.Result {
	case ScanNotScanned, ScanNoFinding, ScanStale:
		if value.FindingCategory != "" {
			return ErrInvalidContract
		}
	case ScanFinding:
		if value.FindingCategory != "test_signature" && value.FindingCategory != "malware" && value.FindingCategory != "suspicious" {
			return ErrInvalidContract
		}
	default:
		return ErrInvalidContract
	}
	parsed, err := time.Parse(time.RFC3339, value.ScannedAt)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339) != value.ScannedAt {
		return ErrInvalidContract
	}
	return nil
}

func (value MalwareResult) ProcessingOutcome() ProcessingOutcome {
	if value.Validate() == nil {
		return OutcomeSucceeded
	}
	return OutcomeFailed
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func validateClosedIdentifier(value string, maximum int) error {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n/\\") {
		return fmt.Errorf("%w: invalid identifier", ErrInvalidContract)
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return fmt.Errorf("%w: invalid identifier", ErrInvalidContract)
	}
	return nil
}
