package provider

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"xirang/backend/internal/backupasset"
)

type DeletePointOutcome string

const (
	DeletePointDeleted       DeletePointOutcome = "deleted"
	DeletePointAlreadyAbsent DeletePointOutcome = "already_absent"
	DeletePointBlockedWORM   DeletePointOutcome = "blocked_worm"
)

var (
	ErrInvalidDeletePointRequest   = errors.New("invalid exact delete point request")
	ErrDeletePointWORM             = errors.New("point deletion blocked by WORM")
	ErrDeletePointIdentityConflict = errors.New("point deletion identity conflict")
)

// DeletePointRequest is the closed exact-point deletion input. The locator
// and access binding remain process-local and must never be serialized.
type DeletePointRequest struct {
	Snapshot               ReadSnapshot `json:"snapshot"`
	Point                  PointLocator `json:"-"`
	ExpectedSourceRevision string       `json:"expected_source_revision"`
	OperationID            string       `json:"operation_id"`
}

type DeletePointResult struct {
	Outcome       DeletePointOutcome `json:"outcome"`
	ReceiptDigest string             `json:"receipt_digest"`
}

// PointDeleter is a separately registered optional capability. A read adapter
// does not become mutating by default when a provider is registered.
type PointDeleter interface {
	ProviderKind() backupasset.ProviderKind
	DeletePoint(context.Context, DeletePointRequest) (DeletePointResult, error)
}

func (request DeletePointRequest) Validate() error {
	if backupasset.ValidateOpaqueID(request.Snapshot.RepositoryID) != nil || request.Snapshot.CapabilityRevision <= 0 ||
		request.Snapshot.Access.RepositoryID != request.Snapshot.RepositoryID || !validRestoreProvider(request.Snapshot.Access.Provider) ||
		!validRestoreDigest(request.ExpectedSourceRevision) || !validRestoreDigest(request.Snapshot.SourceRevision) ||
		backupasset.ValidateOpaqueID(request.OperationID) != nil || strings.TrimSpace(request.Point.Native) == "" ||
		strings.ContainsRune(request.Point.Native, '\x00') {
		return invalidDeletePointRequest("invalid exact delete point request")
	}
	return nil
}

func (request DeletePointRequest) requireSourceRevision() error {
	if request.Validate() != nil {
		return invalidDeletePointRequest("invalid exact delete point request")
	}
	if request.Snapshot.SourceRevision != request.ExpectedSourceRevision {
		return ErrDeletePointIdentityConflict
	}
	return nil
}

func ExecuteDeletePoint(ctx context.Context, port PointDeleter, request DeletePointRequest) (DeletePointResult, error) {
	if interfaceNil(port) {
		return DeletePointResult{}, invalidDeletePointRequest("point deleter unavailable")
	}
	if err := request.Validate(); err != nil {
		return DeletePointResult{}, err
	}
	result, err := port.DeletePoint(ctx, request)
	if err != nil {
		return result, err
	}
	if !validDeletePointResult(result) {
		return DeletePointResult{}, invalidDeletePointRequest("invalid delete point result")
	}
	return result, nil
}

func validDeletePointResult(result DeletePointResult) bool {
	switch result.Outcome {
	case DeletePointDeleted, DeletePointAlreadyAbsent, DeletePointBlockedWORM:
		return result.Outcome == DeletePointBlockedWORM || lowerHex(result.ReceiptDigest, 64)
	default:
		return false
	}
}

func deletionReceiptDigest(kind backupasset.ProviderKind, outcome DeletePointOutcome, operationID, identity string) (string, error) {
	if !validRestoreProvider(kind) || backupasset.ValidateOpaqueID(operationID) != nil || identity == "" {
		return "", invalidDeletePointRequest("invalid deletion receipt identity")
	}
	switch outcome {
	case DeletePointDeleted, DeletePointAlreadyAbsent:
	default:
		return "", invalidDeletePointRequest("invalid deletion receipt outcome")
	}
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang/backup-asset/point-deletion-receipt/v1")
	writer.String(string(kind))
	writer.String(string(outcome))
	writer.String(operationID)
	writer.String(identity)
	digest, err := writer.HexDigest()
	if err != nil {
		return "", fmt.Errorf("%w: deletion receipt", ErrInvalidDeletePointRequest)
	}
	return digest, nil
}

func invalidDeletePointRequest(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidDeletePointRequest, message)
}

func absoluteOrPathShapedLocator(value string) bool {
	return filepath.IsAbs(value) || strings.Contains(value, "/") || strings.Contains(value, `\`)
}
