package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xirang/backend/internal/model"
	"xirang/backend/internal/util"
)

var ErrUnsafePrivateRuntimeRoot = errors.New("unsafe private runtime root")

// ValidatePrivateRuntimeRoot proves that an internal ciphertext/cache root is
// a non-symlink local boundary disjoint from every known backup source. Errors
// are deliberately closed and never include private locators.
func (service *Service) ValidatePrivateRuntimeRoot(ctx context.Context, candidate string) error {
	if service == nil || service.db == nil || !validPrivateRuntimeCandidate(candidate) || unsafePrivateRuntimeFilesystem(candidate) {
		return ErrUnsafePrivateRuntimeRoot
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var tasks []struct {
		RsyncSource string
		RsyncTarget string
	}
	if err := service.db.WithContext(ctx).Model(&model.Task{}).Select("rsync_source", "rsync_target").Find(&tasks).Error; err != nil {
		return fmt.Errorf("%w: task source proof unavailable", ErrUnsafePrivateRuntimeRoot)
	}
	for _, task := range tasks {
		if privateRuntimePathOverlaps(candidate, task.RsyncSource) || privateRuntimePathOverlaps(candidate, task.RsyncTarget) {
			return fmt.Errorf("%w: backup source overlap", ErrUnsafePrivateRuntimeRoot)
		}
	}
	var links []model.TaskRepositoryLink
	if err := service.db.WithContext(ctx).Select("id", "encrypted_legacy_locator").Find(&links).Error; err != nil {
		return fmt.Errorf("%w: repository source proof unavailable", ErrUnsafePrivateRuntimeRoot)
	}
	for _, link := range links {
		if privateRuntimePathOverlaps(candidate, link.EncryptedLegacyLocator) {
			return fmt.Errorf("%w: backup source overlap", ErrUnsafePrivateRuntimeRoot)
		}
	}
	var bindings []model.RepositoryAccessBinding
	if err := service.db.WithContext(ctx).Where("status = ?", bindingStatusActive).Find(&bindings).Error; err != nil {
		return fmt.Errorf("%w: repository binding proof unavailable", ErrUnsafePrivateRuntimeRoot)
	}
	for _, binding := range bindings {
		document, err := decodeStoredBindingDocument(binding.EncryptedConfig)
		if err != nil {
			return fmt.Errorf("%w: repository binding proof unavailable", ErrUnsafePrivateRuntimeRoot)
		}
		switch {
		case document.V1 != nil && privateRuntimePathOverlaps(candidate, document.V1.Locator):
			return fmt.Errorf("%w: backup source overlap", ErrUnsafePrivateRuntimeRoot)
		case document.ManagedRsyncV2 != nil && privateRuntimePathOverlaps(candidate, document.ManagedRsyncV2.ManagedRootLocator):
			return fmt.Errorf("%w: backup source overlap", ErrUnsafePrivateRuntimeRoot)
		case document.ManagedRcloneV3 != nil && document.ManagedRcloneV3.Portable != nil &&
			document.ManagedRcloneV3.Portable.Backend == "local":
			return fmt.Errorf("%w: local remote source proof unavailable", ErrUnsafePrivateRuntimeRoot)
		}
	}
	return nil
}

func validPrivateRuntimeCandidate(candidate string) bool {
	if strings.TrimSpace(candidate) != candidate || candidate == "" || !filepath.IsAbs(candidate) ||
		filepath.Clean(candidate) != candidate || candidate == string(filepath.Separator) {
		return false
	}
	for _, forbidden := range []string{"/data", "/backup", "/logs"} {
		if privateRuntimePathsRelated(candidate, forbidden) {
			return false
		}
	}
	return true
}

func unsafePrivateRuntimeFilesystem(candidate string) bool {
	current := candidate
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return true
			}
			resolved, resolveErr := filepath.EvalSymlinks(current)
			return resolveErr != nil || filepath.Clean(resolved) != filepath.Clean(current)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return true
		}
		current = parent
	}
}

func privateRuntimePathOverlaps(candidate, source string) bool {
	source = strings.TrimSpace(source)
	if source == "" || util.IsRemotePathSpec(source) || !filepath.IsAbs(source) {
		return false
	}
	return privateRuntimePathsRelated(candidate, filepath.Clean(source))
}

func privateRuntimePathsRelated(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	separator := string(filepath.Separator)
	return left == right || strings.HasPrefix(left, right+separator) || strings.HasPrefix(right, left+separator)
}
