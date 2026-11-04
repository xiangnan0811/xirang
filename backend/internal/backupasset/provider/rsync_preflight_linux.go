//go:build linux

package provider

import (
	"context"
	"fmt"
)

func preflightRsyncManagedRoot(ctx context.Context, preflighter *RsyncTreePreflighter, managedRoot string, request RsyncTreePreflightRequest) (RsyncTreePreflightEvidence, error) {
	if ctx == nil {
		return RsyncTreePreflightEvidence{}, fmt.Errorf("managed Rsync preflight context is required")
	}
	if err := ctx.Err(); err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	if err := validateRsyncTreePreflightRequest(request); err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	root, err := normalizeRsyncManagedRoot(managedRoot)
	if err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	tree, err := openRsyncManagedTreeReadOnly(root)
	if err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	defer func() { _ = tree.Close() }()
	if err := tree.VerifyRootIdentity(); err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	marker, err := tree.readRepositoryMarker()
	if err != nil {
		return RsyncTreePreflightEvidence{}, err
	}
	if rsyncTreeDigest(marker) != request.RepositoryMarkerDigest {
		return RsyncTreePreflightEvidence{}, fmt.Errorf("%w: managed repository marker changed", errRsyncManagedTreeUnsafe)
	}
	return preflighter.Preflight(ctx, tree, request)
}
