package fileaccess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

var (
	ErrInvalidLocator    = errors.New("invalid file locator")
	ErrOutsideRoot       = errors.New("file locator outside root")
	ErrSymlinkDenied     = errors.New("symbolic link access denied")
	ErrNotRegular        = errors.New("file is not regular")
	ErrNotDirectory      = errors.New("file is not a directory")
	ErrResourceLimit     = errors.New("file access resource limit exceeded")
	ErrSourceChanged     = errors.New("file source changed during operation")
	ErrStrictUnavailable = errors.New("strict file access unavailable")
)

type InputPolicy string
type SymlinkPolicy string
type OpenTypePolicy string

const (
	StrictRelativeLocator    InputPolicy    = "strict_relative_locator"
	LegacyAbsoluteOrRelative InputPolicy    = "legacy_absolute_or_relative"
	NeverFollow              SymlinkPolicy  = "never_follow"
	FollowOnlyWithinRoot     SymlinkPolicy  = "follow_only_within_root"
	RegularFilesOnly         OpenTypePolicy = "regular_files_only"
)

type Policy struct {
	Input     InputPolicy
	Symlinks  SymlinkPolicy
	OpenTypes OpenTypePolicy
}

var (
	ProviderPolicy = Policy{Input: StrictRelativeLocator, Symlinks: NeverFollow, OpenTypes: RegularFilesOnly}
	LegacyPolicy   = Policy{Input: LegacyAbsoluteOrRelative, Symlinks: FollowOnlyWithinRoot, OpenTypes: RegularFilesOnly}
)

type Root struct {
	Path string
}

type Locator struct {
	Path string `json:"-"`
	root bool
}

func RootLocator() Locator { return Locator{root: true} }

type EntryType string

const (
	EntryFile      EntryType = "file"
	EntryDirectory EntryType = "directory"
	EntrySymlink   EntryType = "symlink"
	EntrySpecial   EntryType = "special"
)

type Entry struct {
	Name           string
	Locator        Locator `json:"-"`
	Type           EntryType
	Size           int64
	Mode           string
	ModTime        time.Time
	OpaqueDigest   string
	SourceRevision string
}

type PageRequest struct {
	Limit     int
	AfterName string
	MaxItems  int
	MaxBytes  int64
}

func (request PageRequest) Normalize() (PageRequest, error) {
	if request.Limit <= 0 || request.MaxItems <= 0 || request.MaxBytes <= 0 || request.Limit > request.MaxItems {
		return PageRequest{}, fmt.Errorf("%w: bounded page limits are required", ErrResourceLimit)
	}
	return request, nil
}

type EntryPage struct {
	Items      []Entry
	HasMore    bool
	LastDigest string
}

type ContentStat struct {
	Size           int64
	ModTime        time.Time
	Mode           string
	SourceRevision string
}

type ByteRange struct {
	Offset int64
	Length int64
}

func (value ByteRange) Validate() error {
	if value.Offset < 0 || value.Length <= 0 {
		return fmt.Errorf("%w: invalid byte range", ErrResourceLimit)
	}
	return nil
}

type ReadHandle interface {
	io.Reader
	Close() error
}

type Tree interface {
	List(context.Context, Root, Locator, Policy, PageRequest) (EntryPage, error)
	Lstat(context.Context, Root, Locator, Policy) (Entry, error)
	OpenRegular(context.Context, Root, Locator, Policy) (ReadHandle, ContentStat, error)
	OpenRange(context.Context, Root, Locator, Policy, ByteRange) (ReadHandle, ContentStat, error)
}
