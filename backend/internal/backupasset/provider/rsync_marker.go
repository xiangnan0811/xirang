package provider

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"xirang/backend/internal/backupasset"
)

const rsyncTreeMarkerSchemaV1 = 1

type rsyncTreeAttemptMarkerBodyV1 struct {
	Version int                `json:"version"`
	Attempt RsyncTreeAttemptV1 `json:"attempt"`
}

type rsyncTreeAttemptMarkerWireV1 struct {
	Version           int                `json:"version"`
	Attempt           RsyncTreeAttemptV1 `json:"attempt"`
	AuthenticationTag string             `json:"authentication_tag"`
}

type rsyncTreeCommitMarkerSigningV1 struct {
	Version int               `json:"version"`
	Commit  RsyncTreeCommitV1 `json:"commit"`
}

type rsyncTreeCommitMarkerWireV1 struct {
	Version           int               `json:"version"`
	Commit            RsyncTreeCommitV1 `json:"commit"`
	AuthenticationTag string            `json:"authentication_tag"`
}

func encodeRsyncTreeAttemptMarkerV1(attempt RsyncTreeAttemptV1, key []byte) ([]byte, error) {
	if err := attempt.Validate(); err != nil {
		return nil, err
	}
	if !validRsyncTreeMarkerKey(key) {
		return nil, fmt.Errorf("%w: invalid managed Rsync marker key", backupasset.ErrInvalidState)
	}
	body := rsyncTreeAttemptMarkerBodyV1{Version: rsyncTreeMarkerSchemaV1, Attempt: attempt}
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode managed Rsync attempt marker: %w", err)
	}
	wire := rsyncTreeAttemptMarkerWireV1{Version: body.Version, Attempt: body.Attempt, AuthenticationTag: rsyncTreeMarkerTag(key, encodedBody)}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode managed Rsync attempt marker: %w", err)
	}
	return encoded, nil
}

func decodeRsyncTreeAttemptMarkerV1(raw, key []byte) (RsyncTreeAttemptV1, error) {
	if len(raw) == 0 || len(raw) > maxTaggedPublicationPayloadBytes || !validRsyncTreeMarkerKey(key) {
		return RsyncTreeAttemptV1{}, fmt.Errorf("%w: invalid managed Rsync attempt marker", backupasset.ErrInvalidState)
	}
	decoder, err := strictTaggedPayloadDecoder(string(raw))
	if err != nil {
		return RsyncTreeAttemptV1{}, fmt.Errorf("%w: invalid managed Rsync attempt marker", backupasset.ErrInvalidState)
	}
	var wire rsyncTreeAttemptMarkerWireV1
	if err := decoder.Decode(&wire); err != nil {
		return RsyncTreeAttemptV1{}, fmt.Errorf("%w: invalid managed Rsync attempt marker", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RsyncTreeAttemptV1{}, fmt.Errorf("%w: trailing managed Rsync attempt marker", backupasset.ErrInvalidState)
	}
	if wire.Version != rsyncTreeMarkerSchemaV1 || !validRsyncTreeDigest(wire.AuthenticationTag) {
		return RsyncTreeAttemptV1{}, fmt.Errorf("%w: invalid managed Rsync attempt marker", backupasset.ErrInvalidState)
	}
	if err := wire.Attempt.Validate(); err != nil {
		return RsyncTreeAttemptV1{}, err
	}
	body, err := json.Marshal(rsyncTreeAttemptMarkerBodyV1{Version: wire.Version, Attempt: wire.Attempt})
	if err != nil {
		return RsyncTreeAttemptV1{}, fmt.Errorf("%w: invalid managed Rsync attempt marker", backupasset.ErrInvalidState)
	}
	want, err := hex.DecodeString(rsyncTreeMarkerTag(key, body))
	if err != nil {
		return RsyncTreeAttemptV1{}, fmt.Errorf("%w: invalid managed Rsync attempt marker", backupasset.ErrInvalidState)
	}
	got, err := hex.DecodeString(wire.AuthenticationTag)
	if err != nil || !hmac.Equal(got, want) {
		return RsyncTreeAttemptV1{}, fmt.Errorf("%w: managed Rsync attempt marker authentication failed", backupasset.ErrConflict)
	}
	return wire.Attempt, nil
}

func encodeRsyncTreeCommitMarkerV1(commit RsyncTreeCommitV1, key []byte) (RsyncTreeCommitV1, []byte, error) {
	if !validRsyncTreeMarkerKey(key) {
		return RsyncTreeCommitV1{}, nil, fmt.Errorf("%w: invalid managed Rsync marker key", backupasset.ErrInvalidState)
	}
	if commit.CommitMarkerDigest != "" {
		return RsyncTreeCommitV1{}, nil, fmt.Errorf("%w: managed Rsync commit marker digest must be unset", backupasset.ErrInvalidState)
	}
	validationCopy := commit
	validationCopy.CommitMarkerDigest = strings.Repeat("0", sha256.Size*2)
	if err := validationCopy.Validate(); err != nil {
		return RsyncTreeCommitV1{}, nil, err
	}
	signingPayload, err := json.Marshal(rsyncTreeCommitMarkerSigningV1{Version: rsyncTreeMarkerSchemaV1, Commit: commit})
	if err != nil {
		return RsyncTreeCommitV1{}, nil, fmt.Errorf("encode managed Rsync commit marker: %w", err)
	}
	commit.CommitMarkerDigest = rsyncTreeDigest(signingPayload)
	if err := commit.Validate(); err != nil {
		return RsyncTreeCommitV1{}, nil, err
	}
	body, err := json.Marshal(rsyncTreeCommitMarkerSigningV1{Version: rsyncTreeMarkerSchemaV1, Commit: commit})
	if err != nil {
		return RsyncTreeCommitV1{}, nil, fmt.Errorf("encode managed Rsync commit marker: %w", err)
	}
	wire := rsyncTreeCommitMarkerWireV1{Version: rsyncTreeMarkerSchemaV1, Commit: commit, AuthenticationTag: rsyncTreeMarkerTag(key, body)}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return RsyncTreeCommitV1{}, nil, fmt.Errorf("encode managed Rsync commit marker: %w", err)
	}
	return commit, encoded, nil
}

func decodeRsyncTreeCommitMarkerV1(raw, key []byte) (RsyncTreeCommitV1, error) {
	if len(raw) == 0 || len(raw) > maxTaggedPublicationPayloadBytes || !validRsyncTreeMarkerKey(key) {
		return RsyncTreeCommitV1{}, fmt.Errorf("%w: invalid managed Rsync commit marker", backupasset.ErrInvalidState)
	}
	decoder, err := strictTaggedPayloadDecoder(string(raw))
	if err != nil {
		return RsyncTreeCommitV1{}, fmt.Errorf("%w: invalid managed Rsync commit marker", backupasset.ErrInvalidState)
	}
	var wire rsyncTreeCommitMarkerWireV1
	if err := decoder.Decode(&wire); err != nil {
		return RsyncTreeCommitV1{}, fmt.Errorf("%w: invalid managed Rsync commit marker", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RsyncTreeCommitV1{}, fmt.Errorf("%w: trailing managed Rsync commit marker", backupasset.ErrInvalidState)
	}
	if wire.Version != rsyncTreeMarkerSchemaV1 || !validRsyncTreeDigest(wire.AuthenticationTag) {
		return RsyncTreeCommitV1{}, fmt.Errorf("%w: invalid managed Rsync commit marker", backupasset.ErrInvalidState)
	}
	if err := wire.Commit.Validate(); err != nil {
		return RsyncTreeCommitV1{}, err
	}
	withoutDigest := wire.Commit
	actualDigest := withoutDigest.CommitMarkerDigest
	withoutDigest.CommitMarkerDigest = ""
	signingPayload, err := json.Marshal(rsyncTreeCommitMarkerSigningV1{Version: wire.Version, Commit: withoutDigest})
	if err != nil || actualDigest != rsyncTreeDigest(signingPayload) {
		return RsyncTreeCommitV1{}, fmt.Errorf("%w: managed Rsync commit marker digest mismatch", backupasset.ErrConflict)
	}
	body, err := json.Marshal(rsyncTreeCommitMarkerSigningV1{Version: wire.Version, Commit: wire.Commit})
	if err != nil {
		return RsyncTreeCommitV1{}, fmt.Errorf("%w: invalid managed Rsync commit marker", backupasset.ErrInvalidState)
	}
	want, _ := hex.DecodeString(rsyncTreeMarkerTag(key, body))
	got, err := hex.DecodeString(wire.AuthenticationTag)
	if err != nil || !hmac.Equal(got, want) {
		return RsyncTreeCommitV1{}, fmt.Errorf("%w: managed Rsync commit marker authentication failed", backupasset.ErrConflict)
	}
	return wire.Commit, nil
}

func validRsyncTreeMarkerKey(key []byte) bool { return len(key) >= sha256.Size }

func rsyncTreeMarkerTag(key, payload []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
