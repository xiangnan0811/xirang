package provider

import (
	"context"
	"fmt"
	"time"

	"xirang/backend/internal/backupasset"
)

// RclonePortableContentResolver binds a sealed Catalog entry to an exact
// authenticated Portable manifest entry and its destination observation.
type RclonePortableContentResolver interface {
	ResolvePortableContent(context.Context, RclonePortablePublicationRequest, RcloneCommitV1, string) (RclonePortableContentBinding, error)
}

type RclonePortableContentBinding struct {
	ManifestVersion  int
	Record           CatalogRecord
	PhysicalModTime  time.Time
	ValidateSnapshot func(context.Context) error
}

func (strategy *RclonePublicationStrategy) ResolvePortableContent(
	ctx context.Context, request RclonePortablePublicationRequest, expected RcloneCommitV1, physicalPath string,
) (RclonePortableContentBinding, error) {
	if strategy == nil || strategy.portable == nil || strategy.portable.remote == nil || !request.ReadDeadline.IsZero() ||
		!validRcloneLogicalPath(physicalPath, request.ManifestOptions.Limits.MaxDepth) ||
		validatePortableExpectedIdentity(request, expected) != nil {
		return RclonePortableContentBinding{}, fmt.Errorf("%w: invalid portable content evidence", backupasset.ErrInvalidState)
	}
	readRequest, err := portableReadRequest(ctx, request)
	if err != nil {
		return RclonePortableContentBinding{}, err
	}
	readCtx, cancel := context.WithDeadline(ctx, readRequest.ReadDeadline)
	var source, destination rcloneCanonicalManifestEntry
	found := false
	_, err = strategy.portable.readPublished(readCtx, readRequest, &expected, func(currentSource, currentDestination rcloneCanonicalManifestEntry) error {
		if currentSource.PhysicalPath != physicalPath {
			return nil
		}
		if found || currentSource.Kind != "file" || currentDestination.Kind != "file" ||
			currentDestination.PhysicalPath != physicalPath {
			return ErrRcloneManifestObservationMismatch
		}
		found = true
		source, destination = currentSource, currentDestination
		return nil
	})
	if err == nil {
		err = readCtx.Err()
	}
	cancel()
	if err != nil {
		return RclonePortableContentBinding{}, err
	}
	if !found {
		return RclonePortableContentBinding{}, ErrRcloneManifestObservationMismatch
	}
	record, err := rcloneCatalogRecord(source, nil)
	if err != nil {
		return RclonePortableContentBinding{}, err
	}
	physicalModTime, err := time.Parse(time.RFC3339Nano, destination.ModTime)
	if err != nil {
		return RclonePortableContentBinding{}, ErrRcloneManifestObservationMismatch
	}

	// Retain no source bundle or manifest tree across a stream's lifetime.
	base := request
	base.Source = RclonePrivateLocator{}
	base.CopyDest = nil
	base.ParentLeaseEligible = false
	base.Manifest = RcloneManifestBundle{}
	base.ManifestOptions.SymlinkTargetReader = nil
	base = cloneRclonePortablePublicationRequest(base)
	if base.Attempt.Portable != nil {
		portable := *base.Attempt.Portable
		if portable.CopyDest != nil {
			copyDest := *portable.CopyDest
			portable.CopyDest = &copyDest
		}
		base.Attempt.Portable = &portable
	}
	expected.ManifestChunkDigests = append([]string(nil), expected.ManifestChunkDigests...)
	expected.Portable = cloneRclonePortableCommit(expected.Portable)
	publisher := strategy.portable
	verify := func(validateCtx context.Context) error {
		read, err := portableReadRequest(validateCtx, base)
		if err != nil {
			return err
		}
		bounded, cancel := context.WithDeadline(validateCtx, read.ReadDeadline)
		defer cancel()
		if _, err := publisher.readCommit(bounded, read, &expected); err != nil {
			return err
		}
		observation, err := publisher.remote.Observe(bounded, read, read.DataRoot)
		if err != nil {
			return err
		}
		if err := bounded.Err(); err != nil {
			return err
		}
		if observation.Version != 1 || observation.ObservationDigest != expected.DestinationObservationDigest ||
			observation.EntryCount != expected.ManifestEntryCount || observation.LogicalBytes != expected.LogicalBytes {
			return ErrRcloneDestinationUnstable
		}
		return nil
	}
	return RclonePortableContentBinding{
		ManifestVersion: int(request.Attempt.ManifestSchemaRevision), Record: record,
		PhysicalModTime: physicalModTime.UTC(), ValidateSnapshot: verify,
	}, nil
}

var _ RclonePortableContentResolver = (*RclonePublicationStrategy)(nil)
