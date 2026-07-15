package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
)

const (
	rsyncManagedRootMarkerVersion    = 1
	RsyncManagedTreeLayoutRevisionV1 = "rsync-tree:v1"
)

// RsyncManagedRootBootstrapRequest contains only process-local root ownership
// facts. It is constructed from the legacy link and repository identity, never
// from an API path field.
type RsyncManagedRootBootstrapRequest struct {
	ManagedRoot  string    `json:"-"`
	RepositoryID string    `json:"-"`
	MarkerKey    []byte    `json:"-"`
	CreatedAt    time.Time `json:"-"`
}

// RsyncManagedRootBootstrapEvidence is internal proof that a root has an
// authenticated repository marker and stable trusted-root identity.
type RsyncManagedRootBootstrapEvidence struct {
	Created                   bool   `json:"-"`
	RepositoryMarkerDigest    string `json:"-"`
	ManagedRootIdentityDigest string `json:"-"`
}

type rsyncManagedRootMarkerBodyV1 struct {
	Version      int       `json:"version"`
	RepositoryID string    `json:"repository_id"`
	Layout       string    `json:"layout"`
	CreatedAt    time.Time `json:"created_at"`
}

type rsyncManagedRootMarkerWireV1 struct {
	Version           int       `json:"version"`
	RepositoryID      string    `json:"repository_id"`
	Layout            string    `json:"layout"`
	CreatedAt         time.Time `json:"created_at"`
	AuthenticationTag string    `json:"authentication_tag"`
}

// BootstrapRsyncManagedRoot creates a previously absent managed root or
// revalidates its authenticated ownership marker. It never overwrites an
// existing marker and fails closed for an unowned or malformed directory.
func BootstrapRsyncManagedRoot(ctx context.Context, request RsyncManagedRootBootstrapRequest) (RsyncManagedRootBootstrapEvidence, error) {
	if ctx == nil || ctx.Err() != nil {
		if ctx != nil {
			return RsyncManagedRootBootstrapEvidence{}, ctx.Err()
		}
		return RsyncManagedRootBootstrapEvidence{}, fmt.Errorf("%w: managed Rsync root bootstrap context is required", backupasset.ErrInvalidState)
	}
	managedRoot, err := normalizeRsyncManagedRoot(request.ManagedRoot)
	if err != nil ||
		backupasset.ValidateOpaqueID(request.RepositoryID) != nil || !validRsyncTreeMarkerKey(request.MarkerKey) ||
		request.CreatedAt.IsZero() {
		return RsyncManagedRootBootstrapEvidence{}, fmt.Errorf("%w: invalid managed Rsync root bootstrap request", backupasset.ErrInvalidState)
	}
	request.ManagedRoot = managedRoot
	return bootstrapRsyncManagedRoot(ctx, cloneRsyncManagedRootBootstrapRequest(request))
}

// ValidateRsyncManagedRootSeparation proves that another local directory does
// not overlap a managed root. Both inputs are process-local and are never
// returned or serialized by this package.
func ValidateRsyncManagedRootSeparation(ctx context.Context, managedRoot, otherRoot string) error {
	if ctx == nil || ctx.Err() != nil {
		if ctx != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: managed Rsync root separation context is required", backupasset.ErrInvalidState)
	}
	managedRoot, err := normalizeRsyncManagedRoot(managedRoot)
	if err != nil {
		return fmt.Errorf("%w: invalid managed Rsync root separation request", backupasset.ErrInvalidState)
	}
	otherRoot, err = normalizeRsyncManagedRoot(otherRoot)
	if err != nil {
		return fmt.Errorf("%w: invalid managed Rsync root separation request", backupasset.ErrInvalidState)
	}
	return validateRsyncManagedRootSeparation(ctx, managedRoot, otherRoot)
}

func cloneRsyncManagedRootBootstrapRequest(request RsyncManagedRootBootstrapRequest) RsyncManagedRootBootstrapRequest {
	request.MarkerKey = append([]byte(nil), request.MarkerKey...)
	request.CreatedAt = request.CreatedAt.UTC()
	return request
}

func encodeRsyncManagedRootMarkerV1(request RsyncManagedRootBootstrapRequest) ([]byte, error) {
	body := rsyncManagedRootMarkerBodyV1{
		Version: rsyncManagedRootMarkerVersion, RepositoryID: request.RepositoryID,
		Layout: RsyncManagedTreeLayoutRevisionV1, CreatedAt: request.CreatedAt.UTC(),
	}
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode managed Rsync root marker: %w", err)
	}
	wire := rsyncManagedRootMarkerWireV1{
		Version: body.Version, RepositoryID: body.RepositoryID, Layout: body.Layout, CreatedAt: body.CreatedAt,
		AuthenticationTag: rsyncTreeMarkerTag(request.MarkerKey, encodedBody),
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode managed Rsync root marker: %w", err)
	}
	return encoded, nil
}

func decodeRsyncManagedRootMarkerV1(raw []byte, request RsyncManagedRootBootstrapRequest) error {
	if len(raw) == 0 || len(raw) > 64<<10 || !validRsyncTreeMarkerKey(request.MarkerKey) {
		return fmt.Errorf("%w: invalid managed Rsync root marker", backupasset.ErrInvalidState)
	}
	decoder, err := strictTaggedPayloadDecoder(string(raw))
	if err != nil {
		return fmt.Errorf("%w: invalid managed Rsync root marker", backupasset.ErrInvalidState)
	}
	var wire rsyncManagedRootMarkerWireV1
	if err := decoder.Decode(&wire); err != nil {
		return fmt.Errorf("%w: invalid managed Rsync root marker", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing managed Rsync root marker", backupasset.ErrInvalidState)
	}
	if wire.Version != rsyncManagedRootMarkerVersion || wire.RepositoryID != request.RepositoryID ||
		wire.Layout != RsyncManagedTreeLayoutRevisionV1 || wire.CreatedAt.IsZero() || !validRsyncTreeDigest(wire.AuthenticationTag) ||
		!wire.CreatedAt.Equal(wire.CreatedAt.UTC()) {
		return fmt.Errorf("%w: managed Rsync root marker identity mismatch", backupasset.ErrConflict)
	}
	body, err := json.Marshal(rsyncManagedRootMarkerBodyV1{
		Version: wire.Version, RepositoryID: wire.RepositoryID, Layout: wire.Layout, CreatedAt: wire.CreatedAt.UTC(),
	})
	if err != nil {
		return fmt.Errorf("%w: encode managed Rsync root marker verification", backupasset.ErrInvalidState)
	}
	if !strings.EqualFold(wire.AuthenticationTag, rsyncTreeMarkerTag(request.MarkerKey, body)) {
		return fmt.Errorf("%w: managed Rsync root marker authentication failed", backupasset.ErrConflict)
	}
	return nil
}
