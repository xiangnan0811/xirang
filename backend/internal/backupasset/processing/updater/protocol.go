package updater

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"
)

var ErrProtocolInvalid = errors.New("updater protocol invalid")

const maximumProtocolBytes = 64 << 10

type RegisterCandidateRequest struct {
	SchemaVersion int          `json:"schema_version"`
	Receipt       InboxReceipt `json:"receipt"`
}

type RegisterCandidateResult struct {
	SchemaVersion int    `json:"schema_version"`
	CandidateID   string `json:"candidate_id"`
}

type PullActivationRequest struct {
	SchemaVersion     int    `json:"schema_version"`
	ActiveFingerprint string `json:"active_fingerprint"`
}

type ActivationDirective struct {
	SchemaVersion          int    `json:"schema_version"`
	CandidateID            string `json:"candidate_id"`
	ExpectedOldFingerprint string `json:"expected_old_fingerprint"`
	NewFingerprint         string `json:"new_fingerprint"`
}

type PullActivationResult struct {
	SchemaVersion     int                  `json:"schema_version"`
	RetryAfterSeconds int                  `json:"retry_after_seconds"`
	ScanRequested     bool                 `json:"scan_requested"`
	Directive         *ActivationDirective `json:"directive"`
}

type ActivationReportRequest struct {
	SchemaVersion int               `json:"schema_version"`
	Receipt       ActivationReceipt `json:"receipt"`
}

type ActivationDecision string

const (
	ActivationDecisionCommit   ActivationDecision = "commit"
	ActivationDecisionRollback ActivationDecision = "rollback"
)

type ActivationReportResult struct {
	SchemaVersion     int                `json:"schema_version"`
	Decision          ActivationDecision `json:"decision"`
	ActiveFingerprint string             `json:"active_fingerprint"`
}

func DecodeRegisterCandidateRequest(payload []byte) (RegisterCandidateRequest, error) {
	var value RegisterCandidateRequest
	if err := decodeProtocolJSON(payload, &value); err != nil || requireNonNullMembers(payload, "schema_version", "receipt") != nil ||
		value.SchemaVersion != 1 || validateInboxReceipt(value.Receipt) != nil {
		return RegisterCandidateRequest{}, ErrProtocolInvalid
	}
	return value, nil
}

func DecodePullActivationRequest(payload []byte) (PullActivationRequest, error) {
	var value PullActivationRequest
	if err := decodeProtocolJSON(payload, &value); err != nil || requireNonNullMembers(payload, "schema_version", "active_fingerprint") != nil ||
		value.SchemaVersion != 1 ||
		value.ActiveFingerprint != "" && !lowerHex(value.ActiveFingerprint, 64) {
		return PullActivationRequest{}, ErrProtocolInvalid
	}
	return value, nil
}

func DecodeActivationReportRequest(payload []byte) (ActivationReportRequest, error) {
	var value ActivationReportRequest
	if err := decodeProtocolJSON(payload, &value); err != nil || requireNonNullMembers(payload, "schema_version", "receipt") != nil ||
		value.SchemaVersion != 1 || validateActivationReceipt(value.Receipt) != nil {
		return ActivationReportRequest{}, ErrProtocolInvalid
	}
	return value, nil
}

func ValidateRegisterCandidateResult(value RegisterCandidateResult) error {
	if value.SchemaVersion != 1 || !lowerHex(value.CandidateID, 32) {
		return ErrProtocolInvalid
	}
	return nil
}

func ValidateActivationDirective(value ActivationDirective) error {
	if value.SchemaVersion != 1 || !lowerHex(value.CandidateID, 32) ||
		value.ExpectedOldFingerprint != "" && !lowerHex(value.ExpectedOldFingerprint, 64) ||
		!lowerHex(value.NewFingerprint, 64) || value.NewFingerprint == value.ExpectedOldFingerprint {
		return ErrProtocolInvalid
	}
	return nil
}

func ValidatePullActivationResult(value PullActivationResult) error {
	if value.SchemaVersion != 1 || value.RetryAfterSeconds < 1 || value.RetryAfterSeconds > 60 {
		return ErrProtocolInvalid
	}
	if value.Directive != nil && ValidateActivationDirective(*value.Directive) != nil {
		return ErrProtocolInvalid
	}
	return nil
}

func ValidateActivationReportResult(value ActivationReportResult) error {
	if value.SchemaVersion != 1 || value.Decision != ActivationDecisionCommit && value.Decision != ActivationDecisionRollback ||
		value.ActiveFingerprint != "" && !lowerHex(value.ActiveFingerprint, 64) {
		return ErrProtocolInvalid
	}
	return nil
}

func validateInboxReceipt(value InboxReceipt) error {
	if value.SchemaVersion != 1 || value.SourceKind != "builtin" && value.SourceKind != "admin_registered" ||
		!validIdentifier(value.SourceID, 128) || !semanticVersion.MatchString(value.Version) ||
		!lowerHex(value.ManifestDigest, 64) || !lowerHex(value.SigningKeyFingerprint, 64) ||
		!lowerHex(value.BundleFingerprint, 64) || !lowerHex(value.BundleSHA256, 64) ||
		value.VerifiedAt.IsZero() || value.VerifiedAt.Location() != time.UTC || len(value.Capabilities) == 0 || len(value.Capabilities) > 32 {
		return ErrProtocolInvalid
	}
	lastIdentity := ""
	for _, capability := range value.Capabilities {
		identity := capability.Capability + "\x00" + capability.Schema
		if !validIdentifier(capability.Capability, 64) || !validIdentifier(capability.Schema, 64) || identity <= lastIdentity ||
			!validIdentifier(capability.ToolRevision, 128) || !validIdentifier(capability.ModelRevision, 128) ||
			!validIdentifier(capability.DataRevision, 128) || len(capability.Profiles) == 0 || len(capability.Profiles) > 16 {
			return ErrProtocolInvalid
		}
		lastIdentity = identity
		lastProfile := ""
		for _, profile := range capability.Profiles {
			if !validIdentifier(profile, 64) || profile <= lastProfile {
				return ErrProtocolInvalid
			}
			lastProfile = profile
		}
	}
	return nil
}

func validateActivationReceipt(value ActivationReceipt) error {
	if value.SchemaVersion != 1 || !lowerHex(value.CandidateID, 32) ||
		value.OldFingerprint != "" && !lowerHex(value.OldFingerprint, 64) || !lowerHex(value.NewFingerprint, 64) ||
		value.NewFingerprint == value.OldFingerprint || value.State != "swapped" {
		return ErrProtocolInvalid
	}
	return nil
}

func decodeProtocolJSON(payload []byte, target any) error {
	if len(payload) == 0 || len(payload) > maximumProtocolBytes || !utf8.Valid(payload) || !json.Valid(payload) ||
		rejectDuplicateJSONMembers(payload) != nil {
		return ErrProtocolInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrProtocolInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrProtocolInvalid
	}
	return nil
}

func requireNonNullMembers(payload []byte, names ...string) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(payload, &members); err != nil {
		return ErrProtocolInvalid
	}
	for _, name := range names {
		value, ok := members[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ErrProtocolInvalid
		}
	}
	return nil
}
