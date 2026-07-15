//go:build !linux

package provider

import "context"

func buildRsyncTreeManifest(context.Context, int, ManifestLimits) (rsyncTreeManifest, error) {
	return rsyncTreeManifest{}, errRsyncManagedTreeUnsupported
}
