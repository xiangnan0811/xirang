//go:build !linux

package provider

import "context"

func preflightRsyncManagedRoot(context.Context, *RsyncTreePreflighter, string, RsyncTreePreflightRequest) (RsyncTreePreflightEvidence, error) {
	return RsyncTreePreflightEvidence{}, errRsyncManagedTreeUnsupported
}
