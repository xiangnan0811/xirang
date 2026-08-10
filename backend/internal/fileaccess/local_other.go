//go:build !linux

package fileaccess

import (
	"context"
	"os"
)

type strictRootHandle struct{}

func openStrictRoot(string) (*strictRootHandle, error) { return nil, ErrStrictUnavailable }
func OpenPinnedStrictTree(context.Context, Root, Locator) (PinnedStrictTree, error) {
	return nil, ErrStrictUnavailable
}
func (*strictRootHandle) Close() error                         { return nil }
func (*strictRootHandle) OpenRegular(string) (*os.File, error) { return nil, ErrStrictUnavailable }
func (*strictRootHandle) List(context.Context, string, PageRequest) (EntryPage, error) {
	return EntryPage{}, ErrStrictUnavailable
}
func (*strictRootHandle) Lstat(string) (Entry, error) { return Entry{}, ErrStrictUnavailable }
func isStrictSymlinkError(error) bool                 { return false }
