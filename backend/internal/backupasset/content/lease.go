package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
)

var (
	ErrInvalidContentLease = errors.New("invalid content lease")
	ErrContentLeaseClosed  = errors.New("content lease closed")
)

const leaseDetachedCleanupTimeout = 5 * time.Second

type ContentLeaseController interface {
	Acquire(context.Context, backupasset.AcquireLeaseRequest) (backupasset.Lease, error)
	Renew(context.Context, backupasset.LeaseFence) (backupasset.Lease, error)
	ValidateFence(context.Context, backupasset.LeaseFence) error
	Release(context.Context, backupasset.LeaseFence) error
	Takeover(context.Context, backupasset.TakeoverLeaseRequest) (backupasset.Lease, error)
}

type ContentLeaseRequest struct {
	Ref     backupasset.AssetRef `json:"-"`
	GrantID string               `json:"-"`
}

type ContentLeaseBinding struct {
	LeaseID          string    `json:"-"`
	AttemptID        string    `json:"-"`
	FenceTokenHash   string    `json:"-"`
	LeaseExpiresAt   time.Time `json:"-"`
	AbsoluteDeadline time.Time `json:"-"`
}

type ContentLeaseSession struct {
	controller      ContentLeaseController
	fence           backupasset.LeaseFence
	binding         ContentLeaseBinding
	lastHeartbeatAt time.Time
	mu              sync.Mutex
	released        bool
	releaseErr      error
}

func AcquireContentLease(
	ctx context.Context,
	controller ContentLeaseController,
	request ContentLeaseRequest,
) (*ContentLeaseSession, error) {
	if controller == nil || backupasset.ValidateAssetRef(request.Ref) != nil || backupasset.ValidateOpaqueID(request.GrantID) != nil {
		return nil, ErrInvalidContentLease
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lease, err := controller.Acquire(ctx, backupasset.AcquireLeaseRequest{
		RecoveryPointID: request.Ref.RecoveryPointID, HolderType: backupasset.LeaseHolderContentSession,
		OwnerID: request.GrantID,
	})
	if err != nil {
		return nil, err
	}
	binding, err := contentLeaseBinding(lease, request.Ref.RecoveryPointID, request.GrantID)
	if err != nil {
		cleanupCtx, cancelCleanup := boundedDetachedLeaseCleanup(ctx)
		releaseErr := controller.Release(cleanupCtx, lease.Fence)
		cancelCleanup()
		return nil, errors.Join(err, releaseErr)
	}
	return &ContentLeaseSession{
		controller: controller, fence: lease.Fence, binding: binding,
		lastHeartbeatAt: lease.LastHeartbeatAt.UTC(),
	}, nil
}

func (session *ContentLeaseSession) Binding() ContentLeaseBinding {
	if session == nil {
		return ContentLeaseBinding{}
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.binding
}

func (session *ContentLeaseSession) Renew(ctx context.Context) (ContentLeaseBinding, error) {
	if session == nil || session.controller == nil {
		return ContentLeaseBinding{}, ErrInvalidContentLease
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.released {
		return ContentLeaseBinding{}, ErrContentLeaseClosed
	}
	return session.renewLocked(nonNilContext(ctx))
}

func (session *ContentLeaseSession) Heartbeat(
	ctx context.Context,
	now time.Time,
	interval time.Duration,
) (ContentLeaseBinding, error) {
	if session == nil || session.controller == nil || now.IsZero() || interval <= 0 {
		return ContentLeaseBinding{}, ErrInvalidContentLease
	}
	now = now.UTC()
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.released {
		return ContentLeaseBinding{}, ErrContentLeaseClosed
	}
	ctx = nonNilContext(ctx)
	if now.Before(session.lastHeartbeatAt.Add(interval)) {
		if err := session.controller.ValidateFence(ctx, session.fence); err != nil {
			return ContentLeaseBinding{}, err
		}
		return session.binding, nil
	}
	return session.renewLocked(ctx)
}

func (session *ContentLeaseSession) renewLocked(ctx context.Context) (ContentLeaseBinding, error) {
	lease, err := session.controller.Renew(ctx, session.fence)
	if err != nil {
		return ContentLeaseBinding{}, err
	}
	binding, err := contentLeaseBinding(lease, session.fence.RecoveryPointID, session.fence.OwnerID)
	if err != nil || lease.Fence != session.fence || lease.LastHeartbeatAt.UTC().Before(session.lastHeartbeatAt) {
		return ContentLeaseBinding{}, ErrInvalidContentLease
	}
	session.binding = binding
	session.lastHeartbeatAt = lease.LastHeartbeatAt.UTC()
	return binding, nil
}

func (session *ContentLeaseSession) Validate(ctx context.Context) error {
	if session == nil || session.controller == nil {
		return ErrInvalidContentLease
	}
	session.mu.Lock()
	if session.released {
		session.mu.Unlock()
		return ErrContentLeaseClosed
	}
	fence := session.fence
	session.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	return session.controller.ValidateFence(ctx, fence)
}

func (session *ContentLeaseSession) Release(ctx context.Context) error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	if session.released {
		err := session.releaseErr
		session.mu.Unlock()
		return err
	}
	session.released = true
	fence := session.fence
	controller := session.controller
	session.mu.Unlock()
	if controller == nil {
		return ErrInvalidContentLease
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := controller.Release(ctx, fence)
	session.mu.Lock()
	session.releaseErr = err
	session.mu.Unlock()
	return err
}

func (session *ContentLeaseSession) Close() error {
	cleanupCtx, cancelCleanup := boundedDetachedLeaseCleanup(context.Background())
	defer cancelCleanup()
	return session.Release(cleanupCtx)
}

// ContentLeaseCleanup is intentionally not a delivery session. A reconciler
// may only inspect its private binding and release the rotated cleanup fence.
type ContentLeaseCleanup struct {
	controller ContentLeaseController
	fence      backupasset.LeaseFence
	binding    ContentLeaseBinding
	mu         sync.Mutex
	released   bool
	releaseErr error
}

func TakeoverContentLeaseForCleanup(
	ctx context.Context,
	controller ContentLeaseController,
	leaseID string,
	grantID string,
) (*ContentLeaseCleanup, error) {
	if controller == nil || backupasset.ValidateOpaqueID(leaseID) != nil || backupasset.ValidateOpaqueID(grantID) != nil {
		return nil, ErrInvalidContentLease
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lease, err := controller.Takeover(ctx, backupasset.TakeoverLeaseRequest{LeaseID: leaseID, OwnerID: grantID})
	if err != nil {
		return nil, err
	}
	binding, err := contentLeaseBinding(lease, lease.RecoveryPointID, grantID)
	if err != nil || lease.ID != leaseID {
		cleanupCtx, cancelCleanup := boundedDetachedLeaseCleanup(ctx)
		releaseErr := controller.Release(cleanupCtx, lease.Fence)
		cancelCleanup()
		return nil, errors.Join(ErrInvalidContentLease, releaseErr)
	}
	return &ContentLeaseCleanup{controller: controller, fence: lease.Fence, binding: binding}, nil
}

func (cleanup *ContentLeaseCleanup) Binding() ContentLeaseBinding {
	if cleanup == nil {
		return ContentLeaseBinding{}
	}
	cleanup.mu.Lock()
	defer cleanup.mu.Unlock()
	return cleanup.binding
}

func (cleanup *ContentLeaseCleanup) Release(ctx context.Context) error {
	if cleanup == nil {
		return nil
	}
	cleanup.mu.Lock()
	if cleanup.released {
		err := cleanup.releaseErr
		cleanup.mu.Unlock()
		return err
	}
	cleanup.released = true
	fence := cleanup.fence
	controller := cleanup.controller
	cleanup.mu.Unlock()
	if controller == nil {
		return ErrInvalidContentLease
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := controller.Release(ctx, fence)
	cleanup.mu.Lock()
	cleanup.releaseErr = err
	cleanup.mu.Unlock()
	return err
}

func contentLeaseBinding(lease backupasset.Lease, pointID, grantID string) (ContentLeaseBinding, error) {
	fence := lease.Fence
	if backupasset.ValidateOpaqueID(lease.ID) != nil || lease.ID != fence.LeaseID ||
		backupasset.ValidateOpaqueID(pointID) != nil || lease.RecoveryPointID != pointID || fence.RecoveryPointID != pointID ||
		backupasset.ValidateOpaqueID(grantID) != nil || lease.OwnerID != grantID || fence.OwnerID != grantID ||
		lease.HolderType != backupasset.LeaseHolderContentSession || fence.HolderType != backupasset.LeaseHolderContentSession ||
		lease.Status != backupasset.LeaseActive || backupasset.ValidateOpaqueID(fence.AttemptID) != nil ||
		!lowerHexOfLength(fence.FenceToken, 64) || lease.LeaseExpiresAt.IsZero() || lease.AbsoluteDeadline.IsZero() ||
		lease.LastHeartbeatAt.IsZero() || lease.LastHeartbeatAt.After(lease.LeaseExpiresAt) ||
		lease.LeaseExpiresAt.After(lease.AbsoluteDeadline) {
		return ContentLeaseBinding{}, ErrInvalidContentLease
	}
	sum := sha256.Sum256([]byte(fence.FenceToken))
	return ContentLeaseBinding{
		LeaseID: lease.ID, AttemptID: fence.AttemptID, FenceTokenHash: hex.EncodeToString(sum[:]),
		LeaseExpiresAt: lease.LeaseExpiresAt.UTC(), AbsoluteDeadline: lease.AbsoluteDeadline.UTC(),
	}, nil
}

func lowerHexOfLength(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func boundedDetachedLeaseCleanup(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(nonNilContext(parent)), leaseDetachedCleanupTimeout)
}

var _ interface{ Close() error } = (*ContentLeaseSession)(nil)
