package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
)

const (
	resticRepositoryAccessKind   = "restic_repository_access"
	resticRepositoryAccessSource = "task_executor_settings"
)

var ErrInvalidResticRepositoryAccess = errors.New("invalid restic repository access settings")

type ResticRepositoryAccess struct {
	password string
	Provider string
	Kind     string
	Source   string
}

func ResolveResticRepositoryAccess(raw string) (ResticRepositoryAccess, error) {
	cfg, err := parseResticConfig(raw)
	if err != nil {
		return ResticRepositoryAccess{}, ErrInvalidResticRepositoryAccess
	}
	return NewResticRepositoryAccess(cfg.RepositoryPassword), nil
}

func ResolveResticRepositoryAccessOrEmpty(raw string) ResticRepositoryAccess {
	access, err := ResolveResticRepositoryAccess(raw)
	if err != nil {
		return NewResticRepositoryAccess("")
	}
	return access
}

func NewResticRepositoryAccess(password string) ResticRepositoryAccess {
	return ResticRepositoryAccess{
		password: password,
		Provider: sshutil.CredentialProviderLocal,
		Kind:     resticRepositoryAccessKind,
		Source:   resticRepositoryAccessSource,
	}
}

func (access ResticRepositoryAccess) SafeMetadata() map[string]string {
	return map[string]string{
		"provider": access.Provider,
		"kind":     access.Kind,
		"source":   access.Source,
	}
}

func (access ResticRepositoryAccess) Password() string {
	return access.password
}

// BuildResticPasswordFilePath 生成一个唯一的 restic 密码临时文件路径。
//
// The password lives below a freshly-created, mode-0700 directory rather than
// directly below /tmp.  Callers must pass this path to the create and cleanup
// command helpers unchanged.
func BuildResticPasswordFilePath() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("/tmp/xirang_restic_pw_%s/password", hex.EncodeToString(b))
}

// BuildResticPasswordFileArg 返回 restic 命令使用的 --password-file 参数。
func BuildResticPasswordFileArg(passwordFilePath string) string {
	return "--password-file " + ShellEscape(passwordFilePath)
}

// BuildResticCommandPrefix returns a binary-first restic command prefix with
// the password-file option. It deliberately does not add sudo: compatibility
// callers must preserve their existing privilege policy.
func BuildResticCommandPrefix(binary, passwordFilePath string) string {
	return binary + " " + BuildResticPasswordFileArg(passwordFilePath)
}

// BuildCreateResticPasswordFileCmd 返回在远程节点上创建 restic 密码文件的命令。
//
// The directory is created without -p, so an existing directory or symlink
// (including a dangling symlink) is a hard failure.  mktemp creates a private
// regular file, and ln publishes it at the requested name without following
// or replacing an existing destination.  The trap removes only files that
// this command successfully published, and never removes a colliding path.
func BuildCreateResticPasswordFileCmd(passwordFilePath string, access ResticRepositoryAccess) string {
	pwEscaped := ShellEscape(access.Password())
	fileEscaped := ShellEscape(passwordFilePath)
	dirEscaped := ShellEscape(path.Dir(passwordFilePath))
	markerPath := path.Join(path.Dir(passwordFilePath), ".xirang_restic_pw_owner")
	markerEscaped := ShellEscape(markerPath)
	ownerMarker := ShellEscape("xirang-restic-password-v1:" + passwordFilePath)

	return fmt.Sprintf(`umask 077
dir=%s
file=%s
marker=%s
dir_created=0
created=0
marker_created=0
tmp=
marker_tmp=
cleanup() {
	if [ "$created" = 1 ] && [ ! -L "$file" ]; then
		rm -f "$file"
	fi
	if [ "$marker_created" = 1 ] && [ ! -L "$marker" ]; then
		rm -f "$marker"
	fi
	if [ -n "$tmp" ] && [ ! -L "$tmp" ]; then
		rm -f "$tmp"
	fi
	if [ -n "$marker_tmp" ] && [ ! -L "$marker_tmp" ]; then
		rm -f "$marker_tmp"
	fi
	if [ "$dir_created" = 1 ]; then
		rmdir "$dir" 2>/dev/null
	fi
}
trap cleanup 0
trap 'exit 1' 1 2 3 15
if mkdir -m 700 "$dir"; then
	dir_created=1
	marker_tmp=$(mktemp "$dir/.owner.XXXXXX") &&
		printf '%%s' %s > "$marker_tmp" &&
		[ ! -e "$marker" ] &&
		[ ! -L "$marker" ] &&
		ln "$marker_tmp" "$marker" &&
		marker_created=1 &&
		rm -f "$marker_tmp" &&
		marker_tmp= &&
		tmp=$(mktemp "$dir/.pw.XXXXXX") &&
		printf '%%s' %s > "$tmp" &&
		[ ! -e "$file" ] &&
		[ ! -L "$file" ] &&
		ln "$tmp" "$file" &&
		created=1 &&
		rm -f "$tmp" &&
		tmp= &&
		chmod 600 "$file" "$marker" &&
		trap - 0 1 2 3 15
else
	exit 1
fi
`, dirEscaped, fileEscaped, markerEscaped, ownerMarker, pwEscaped)
}

// BuildCleanupResticPasswordFileCmd 返回删除远程节点上 restic 密码临时文件的命令。
//
// Cleanup is deliberately conditional on the private owner marker.  This
// makes it safe to arm cleanup before creation: a pre-existing file, regular
// or symlink, is left untouched when directory creation failed.
func BuildCleanupResticPasswordFileCmd(passwordFilePath string) string {
	dir := path.Dir(passwordFilePath)
	marker := path.Join(dir, ".xirang_restic_pw_owner")
	return fmt.Sprintf(`dir=%s
file=%s
marker=%s
if [ -d "$dir" ] && [ ! -L "$dir" ] &&
	[ -f "$marker" ] && [ ! -L "$marker" ] &&
	[ "$(cat "$marker" 2>/dev/null)" = %s ] &&
	[ ! -L "$file" ] &&
	{ [ ! -e "$file" ] || [ -f "$file" ]; }; then
	rm -f "$file" "$marker" && rmdir "$dir" 2>/dev/null
fi
`, ShellEscape(dir), ShellEscape(passwordFilePath), ShellEscape(marker),
		ShellEscape("xirang-restic-password-v1:"+passwordFilePath))
}

// resticPasswordCleanupTimeout bounds the independent post-operation cleanup
// connection. It intentionally does not inherit a canceled operation context.
const resticPasswordCleanupTimeout = 5 * time.Second

// CleanupResticPasswordFile removes an attempt-owned Restic password file over
// a fresh SSH connection. Operation streams may own (and force-close) their
// original transport, so cleanup must redial the same node for the exact
// cleanup purpose instead of reusing that client. A dial or command failure is
// returned to the caller; an unreachable node cannot be promised to be clean.
func CleanupResticPasswordFile(node model.Node, passwordFilePath, purpose string) (returnErr error) {
	defer func() {
		if returnErr != nil {
			logger.Module("executor").Warn().
				Uint("node_id", node.ID).
				Str("purpose", sshutil.NormalizePurpose(purpose)).
				Err(returnErr).
				Msg("restic password cleanup failed; remote residue may remain")
		}
	}()
	if strings.TrimSpace(passwordFilePath) == "" {
		return fmt.Errorf("restic password cleanup path is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), resticPasswordCleanupTimeout)
	defer cancel()

	client, err := DialSSHForNodePurpose(ctx, node, purpose)
	if err != nil {
		return fmt.Errorf("restic password cleanup SSH connection failed: %w", err)
	}
	defer client.Close() //nolint:errcheck // cleanup transport close is best effort

	runner := sshutil.NewSSHCommandRunnerWithTransportClose(client, 1)
	stream, err := runner.OpenRawExecution(ctx, sshutil.RawCommandSpec{
		Command:        BuildCleanupResticPasswordFileCmd(passwordFilePath),
		Timeout:        resticPasswordCleanupTimeout,
		MaxStdoutBytes: 4 << 10,
		MaxStderrBytes: 4 << 10,
		MaxRecordBytes: 4 << 10,
	})
	if err != nil {
		return fmt.Errorf("restic password cleanup command start failed: %w", err)
	}
	if _, err := io.Copy(io.Discard, stream); err != nil {
		cancelErr := stream.Cancel()
		if cancelErr != nil && !errors.Is(cancelErr, sshutil.ErrCommandFailed) {
			err = errors.Join(err, cancelErr)
		}
		return fmt.Errorf("restic password cleanup command failed: %w", err)
	}
	completion, err := stream.Join()
	if err != nil {
		return fmt.Errorf("restic password cleanup command join failed: %w", err)
	}
	if !completion.ExitCodeKnown {
		return fmt.Errorf("restic password cleanup command completed without an exit status")
	}
	if completion.ExitCode != 0 {
		return fmt.Errorf("restic password cleanup command exited with code %d", completion.ExitCode)
	}
	return nil
}

func parseResticConfigWithRepositoryAccess(raw string) (ResticConfig, ResticRepositoryAccess, error) {
	cfg, access, err := resolveResticConfigWithRepositoryAccess(raw)
	if err != nil {
		return ResticConfig{}, ResticRepositoryAccess{}, err
	}
	return cfg, access, nil
}

func resolveResticConfigWithRepositoryAccess(raw string) (ResticConfig, ResticRepositoryAccess, error) {
	cfg, err := parseResticConfig(raw)
	if err != nil {
		return ResticConfig{}, ResticRepositoryAccess{}, ErrInvalidResticRepositoryAccess
	}
	return cfg, NewResticRepositoryAccess(cfg.RepositoryPassword), nil
}
