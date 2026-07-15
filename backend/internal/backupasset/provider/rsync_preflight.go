package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
)

const maxRsyncTreePreflightTTL = 24 * time.Hour

type RsyncTreeQuotaSignal string

const (
	RsyncTreeQuotaSignalUnknown RsyncTreeQuotaSignal = "unknown"
)

// RsyncTreeCapacityRequirement carries only conservative thresholds. It does
// not treat a filesystem-reported LINK_MAX value as an exact link budget.
type RsyncTreeCapacityRequirement struct {
	RequiredFreeBytes  uint64
	RequiredFreeInodes uint64
	ParentLinkCount    uint64
	LinkSafetyCeiling  uint64
}

// RsyncTreePreflightRequest contains the facts that must stay stable between
// a local filesystem probe and later managed-mode activation. Paths are input
// only and never appear in the returned evidence.
type RsyncTreePreflightRequest struct {
	TaskID                 uint
	ExpectedTaskRevision   uint64
	Mode                   backupasset.TaskPublicationMode
	LocalSourceRoot        string
	RepositoryMarkerDigest string
	CapabilityRevision     uint64
	Capacity               RsyncTreeCapacityRequirement
}

// RsyncTreePreflightEvidence is an internal provider result. Exact capacity
// numbers are intentionally JSON-hidden; later service code must project them
// to safe estimate buckets rather than expose filesystem facts directly.
type RsyncTreePreflightEvidence struct {
	ID                        string                          `json:"id"`
	Digest                    string                          `json:"-"`
	TaskID                    uint                            `json:"-"`
	ExpectedTaskRevision      uint64                          `json:"-"`
	Mode                      backupasset.TaskPublicationMode `json:"mode"`
	RepositoryMarkerDigest    string                          `json:"-"`
	ManagedRootIdentityDigest string                          `json:"-"`
	SourceIdentityDigest      string                          `json:"-"`
	CapabilityRevision        uint64                          `json:"-"`
	ExpiresAt                 time.Time                       `json:"expires_at"`
	HardlinkVerified          bool                            `json:"-"`
	RenameNoReplaceVerified   bool                            `json:"-"`
	DirectoryFsyncVerified    bool                            `json:"-"`
	FreeBytes                 uint64                          `json:"-"`
	FreeInodes                uint64                          `json:"-"`
	ParentLinkCount           uint64                          `json:"-"`
	QuotaSignal               RsyncTreeQuotaSignal            `json:"-"`
}

type rsyncTreeCapacitySnapshot struct {
	FreeBytes   uint64
	FreeInodes  uint64
	QuotaSignal RsyncTreeQuotaSignal
}

// RsyncTreePreflighter owns only short-lived local filesystem probing. It does
// not persist preflight state or activate a Task; those operations belong to
// the Task/repository service added later in this Child.
type RsyncTreePreflighter struct {
	now func() time.Time
	ttl time.Duration
}

func NewRsyncTreePreflighter(now func() time.Time, ttl time.Duration) (*RsyncTreePreflighter, error) {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if ttl <= 0 || ttl > maxRsyncTreePreflightTTL {
		return nil, fmt.Errorf("%w: invalid Rsync tree preflight TTL", backupasset.ErrInvalidState)
	}
	return &RsyncTreePreflighter{now: now, ttl: ttl}, nil
}

// PreflightManagedRoot is the repository-facing entry point. It accepts a
// process-local path but never exposes the trusted dirfd tree used internally.
func (preflighter *RsyncTreePreflighter) PreflightManagedRoot(ctx context.Context, managedRoot string, request RsyncTreePreflightRequest) (RsyncTreePreflightEvidence, error) {
	if preflighter == nil {
		return RsyncTreePreflightEvidence{}, fmt.Errorf("%w: managed Rsync preflighter unavailable", backupasset.ErrInvalidState)
	}
	return preflightRsyncManagedRoot(ctx, preflighter, managedRoot, request)
}

func (preflighter *RsyncTreePreflighter) Preflight(ctx context.Context, tree *rsyncManagedTree, request RsyncTreePreflightRequest) (RsyncTreePreflightEvidence, error) {
	if ctx == nil || preflighter == nil || tree == nil {
		return RsyncTreePreflightEvidence{}, fmt.Errorf("%w: invalid Rsync tree preflight dependencies", backupasset.ErrInvalidState)
	}
	if err := ctx.Err(); err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	if err := validateRsyncTreePreflightRequest(request); err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	marker, err := tree.readRepositoryMarker()
	if err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	markerDigest := rsyncTreeDigest(marker)
	if markerDigest != request.RepositoryMarkerDigest {
		return RsyncTreePreflightEvidence{}, fmt.Errorf("%w: managed repository marker changed", errRsyncManagedTreeUnsafe)
	}
	rootIdentityDigest := tree.identityDigest(markerDigest)
	sourceIdentityDigest, err := tree.validateLocalSourceRoot(request.LocalSourceRoot)
	if err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	if err := ctx.Err(); err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	probe, err := tree.ProbeCommitPrimitives()
	if err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	if request.Mode == backupasset.PublicationVersionedHardlink && !probe.HardlinkVerified {
		return RsyncTreePreflightEvidence{}, fmt.Errorf("%w: managed tree hard links unavailable", errRsyncManagedTreeUnsafe)
	}
	capacity, err := tree.capacitySnapshot()
	if err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	if err := capacity.validate(request.Capacity); err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	if err := tree.VerifyRootIdentity(); err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	if err := ctx.Err(); err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	preflightID, err := backupasset.NewOpaqueID()
	if err != nil {
		return RsyncTreePreflightEvidence{}, fmt.Errorf("create Rsync tree preflight ID: %w", err)
	}
	now := preflighter.now().UTC()
	if now.IsZero() {
		return RsyncTreePreflightEvidence{}, fmt.Errorf("%w: invalid Rsync tree preflight clock", backupasset.ErrInvalidState)
	}
	evidence := RsyncTreePreflightEvidence{
		ID:                        preflightID,
		TaskID:                    request.TaskID,
		ExpectedTaskRevision:      request.ExpectedTaskRevision,
		Mode:                      request.Mode,
		RepositoryMarkerDigest:    markerDigest,
		ManagedRootIdentityDigest: rootIdentityDigest,
		SourceIdentityDigest:      sourceIdentityDigest,
		CapabilityRevision:        request.CapabilityRevision,
		ExpiresAt:                 now.Add(preflighter.ttl),
		HardlinkVerified:          probe.HardlinkVerified,
		RenameNoReplaceVerified:   probe.RenameNoReplaceVerified,
		DirectoryFsyncVerified:    probe.DirectoryFsyncVerified,
		FreeBytes:                 capacity.FreeBytes,
		FreeInodes:                capacity.FreeInodes,
		ParentLinkCount:           request.Capacity.ParentLinkCount,
		QuotaSignal:               capacity.QuotaSignal,
	}
	evidence.Digest = evidence.digest()
	return evidence, nil
}

func validateRsyncTreePreflightRequest(request RsyncTreePreflightRequest) error {
	if request.TaskID == 0 || request.ExpectedTaskRevision == 0 || request.CapabilityRevision == 0 ||
		!validRsyncTreeDigest(request.RepositoryMarkerDigest) || strings.TrimSpace(request.LocalSourceRoot) == "" {
		return fmt.Errorf("%w: invalid Rsync tree preflight request", backupasset.ErrInvalidState)
	}
	switch request.Mode {
	case backupasset.PublicationVersionedHardlink, backupasset.PublicationVersionedFullCopy:
	default:
		return fmt.Errorf("%w: invalid Rsync tree preflight mode", backupasset.ErrInvalidState)
	}
	if request.Capacity.LinkSafetyCeiling > 0 && request.Capacity.ParentLinkCount >= request.Capacity.LinkSafetyCeiling {
		return fmt.Errorf("%w: managed tree parent link safety budget exhausted", errRsyncManagedTreeUnsafe)
	}
	return nil
}

func (capacity rsyncTreeCapacitySnapshot) validate(requirement RsyncTreeCapacityRequirement) error {
	if capacity.FreeBytes < requirement.RequiredFreeBytes || capacity.FreeInodes < requirement.RequiredFreeInodes {
		return fmt.Errorf("%w: insufficient managed tree capacity", errRsyncManagedTreeUnsafe)
	}
	if requirement.LinkSafetyCeiling > 0 && requirement.ParentLinkCount >= requirement.LinkSafetyCeiling {
		return fmt.Errorf("%w: managed tree parent link safety budget exhausted", errRsyncManagedTreeUnsafe)
	}
	return nil
}

func (tree *rsyncManagedTree) identityDigest(markerDigest string) string {
	return rsyncTreeDigest([]byte(strings.Join([]string{
		"rsync-managed-root-v1",
		markerDigest,
		strconv.FormatUint(tree.rootDevice, 10),
		strconv.FormatUint(tree.rootInode, 10),
		strconv.FormatUint(tree.rootMountID, 10),
	}, "\n")))
}

func (evidence RsyncTreePreflightEvidence) digest() string {
	return rsyncTreeDigest([]byte(strings.Join([]string{
		"rsync-tree-preflight-v1",
		evidence.ID,
		strconv.FormatUint(uint64(evidence.TaskID), 10),
		strconv.FormatUint(evidence.ExpectedTaskRevision, 10),
		string(evidence.Mode),
		evidence.RepositoryMarkerDigest,
		evidence.ManagedRootIdentityDigest,
		evidence.SourceIdentityDigest,
		strconv.FormatUint(evidence.CapabilityRevision, 10),
		strconv.FormatInt(evidence.ExpiresAt.UTC().UnixNano(), 10),
		strconv.FormatBool(evidence.HardlinkVerified),
		strconv.FormatBool(evidence.RenameNoReplaceVerified),
		strconv.FormatBool(evidence.DirectoryFsyncVerified),
		strconv.FormatUint(evidence.FreeBytes, 10),
		strconv.FormatUint(evidence.FreeInodes, 10),
		strconv.FormatUint(evidence.ParentLinkCount, 10),
		string(evidence.QuotaSignal),
	}, "\n")))
}

func rsyncTreeDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validRsyncTreeDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
