package content

import (
	"context"
	"errors"
	"io"
	"math"
	"time"

	"xirang/backend/internal/backupasset"
)

var ErrInvalidSourceRequest = errors.New("invalid content source request")

type SourceMode string

const (
	SourceModeStat       SourceMode = "stat"
	SourceModeSequential SourceMode = "sequential"
	SourceModeRange      SourceMode = "range"
)

type ResolvedRange struct {
	Offset int64 `json:"-"`
	Length int64 `json:"-"`
}

type SourceRequest struct {
	Ref                 backupasset.AssetRef `json:"-"`
	CatalogGenerationID string               `json:"-"`
	ExpectedSource      string               `json:"-"`
	ExpectedEntry       string               `json:"-"`
	Mode                SourceMode           `json:"-"`
	MaxBytes            int64                `json:"-"`
	Range               *ResolvedRange       `json:"-"`
}

type SourceStat struct {
	Size              int64      `json:"-"`
	ModifiedAt        *time.Time `json:"-"`
	MediaType         string     `json:"-"`
	SourceFingerprint string     `json:"-"`
	EntryFingerprint  string     `json:"-"`
	FingerprintStrong bool       `json:"-"`
}

type SourceCapabilities struct {
	Provider   backupasset.ProviderKind      `json:"-"`
	Sequential bool                          `json:"-"`
	Range      bool                          `json:"-"`
	Reason     *backupasset.CapabilityReason `json:"-"`
}

type SourceSession interface {
	Stat() SourceStat
	Capabilities() SourceCapabilities
	Reader() SourceReader
	Revalidate(context.Context) error
	Close() error
}

type SourceReader interface {
	io.ReadCloser
	ProviderBytes() int64
}

type SourceResolver interface {
	OpenContentSource(context.Context, SourceRequest) (SourceSession, error)
	ValidateContentCacheRoot(context.Context, string) error
}

func ValidateSourceRequest(request SourceRequest) error {
	if backupasset.ValidateAssetRef(request.Ref) != nil ||
		backupasset.ValidateOpaqueID(request.CatalogGenerationID) != nil ||
		len(request.ExpectedSource) == 0 || len(request.ExpectedSource) > 128 ||
		len(request.ExpectedEntry) > 128 {
		return ErrInvalidSourceRequest
	}
	switch request.Mode {
	case SourceModeStat:
		if request.MaxBytes != 0 || request.Range != nil {
			return ErrInvalidSourceRequest
		}
	case SourceModeSequential:
		if request.MaxBytes <= 0 || request.Range != nil {
			return ErrInvalidSourceRequest
		}
	case SourceModeRange:
		if request.MaxBytes <= 0 || request.Range == nil || request.Range.Offset < 0 ||
			request.Range.Length <= 0 || request.Range.Length != request.MaxBytes ||
			request.Range.Offset > math.MaxInt64-request.Range.Length {
			return ErrInvalidSourceRequest
		}
	default:
		return ErrInvalidSourceRequest
	}
	return nil
}
