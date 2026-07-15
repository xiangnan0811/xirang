//go:build !linux

package provider

import "context"

func bootstrapRsyncManagedRoot(context.Context, RsyncManagedRootBootstrapRequest) (RsyncManagedRootBootstrapEvidence, error) {
	return RsyncManagedRootBootstrapEvidence{}, errRsyncManagedTreeUnsupported
}

func validateRsyncManagedRootSeparation(context.Context, string, string) error {
	return errRsyncManagedTreeUnsupported
}
