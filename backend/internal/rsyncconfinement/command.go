package rsyncconfinement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// HelperProtocolVersion is the closed command grammar shared by the local
	// launcher and the deployed one-shot remote helper.
	HelperProtocolVersion = 2

	HelperPathEnv       = "RSYNC_CONFINEMENT_HELPER"
	RemoteHelperPathEnv = "RSYNC_CONFINEMENT_REMOTE_HELPER"
	DefaultHelperPath   = "xirang-rsync-confined"
)

var (
	ErrCapabilityUnavailable = errors.New("rsync confinement capability unavailable")
	ErrInvalidRequest        = errors.New("invalid rsync confinement request")
)

// CommandRequest describes one fixed Rsync process. LocalSource and
// LocalTarget are the local transfer operands. A remote operand is left to
// Rsync's SSH transport and must carry a guarded remote helper command.
type CommandRequest struct {
	Binary string
	Args   []string

	LocalSource string
	LocalTarget string

	// LocalSourceTargetRole selects TargetRoots instead of SourceRoots for a
	// local read whose semantic role is a restored target (for example,
	// restore verification). The descriptor remains pinned before execution.
	LocalSourceTargetRole bool
	// RuntimeReadPaths are local, non-content files required by the Rsync
	// process itself (for example an SSH identity or known_hosts file). Each
	// path is pinned before the helper starts and granted read access without
	// widening the source or target boundary.
	RuntimeReadPaths []string

	// TrustedLocalSource identifies a server-created staging source. It is
	// still pinned and granted only through the source descriptor, but is not
	// required to lie below the user-configured source roots.
	TrustedLocalSource bool

	// TrustedLocalTarget identifies a server-created temporary capture target.
	// It is pinned and granted only through the target descriptor, without
	// widening the configured target roots.
	TrustedLocalTarget bool

	// LocalBasis is a server-created private directory passed to Rsync as
	// --copy-dest (a snapshot of the previous backup). Like the trusted
	// staging operands it is not required to lie below the configured roots;
	// it is pinned and granted read-only access so the receiver can reuse
	// unchanged content and delta-transfer changed files.
	LocalBasis string
}

// NewCommand creates the one-shot confined helper process when either
// allowlist is configured. With no configured allowlist it returns a regular
// command, preserving the historical unrestricted behavior.
func NewCommand(ctx context.Context, request CommandRequest) (*exec.Cmd, func(), error) {
	if request.Binary == "" || strings.ContainsRune(request.Binary, '\x00') {
		return nil, func() {}, fmt.Errorf("%w: binary", ErrInvalidRequest)
	}
	for _, arg := range request.Args {
		if strings.ContainsRune(arg, '\x00') {
			return nil, func() {}, fmt.Errorf("%w: argument", ErrInvalidRequest)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	policy, err := LoadPolicyFromEnv()
	if err != nil {
		return nil, func() {}, err
	}
	if !policy.Configured() {
		return exec.CommandContext(ctx, request.Binary, request.Args...), func() {}, nil
	}
	if err := validateConfinedBasisArgs(request.Args, request.LocalBasis); err != nil {
		return nil, func() {}, err
	}
	if request.LocalSource == "" && request.LocalTarget == "" {
		return nil, func() {}, fmt.Errorf("%w: configured Rsync policy has no local operand", ErrCapabilityUnavailable)
	}

	binary := request.Binary
	if !filepath.IsAbs(binary) {
		binary, err = exec.LookPath(binary)
		if err != nil {
			return nil, func() {}, fmt.Errorf("%w: Rsync binary unavailable: %v", ErrCapabilityUnavailable, err)
		}
	} else if _, err := os.Stat(binary); err != nil {
		return nil, func() {}, fmt.Errorf("%w: Rsync binary unavailable: %v", ErrCapabilityUnavailable, err)
	}
	if !filepath.IsAbs(binary) {
		return nil, func() {}, fmt.Errorf("%w: Rsync binary is not absolute", ErrCapabilityUnavailable)
	}
	helper := strings.TrimSpace(os.Getenv(HelperPathEnv))
	if helper == "" {
		helper = DefaultHelperPath
	}
	if strings.ContainsRune(helper, '\x00') {
		return nil, func() {}, fmt.Errorf("%w: helper path", ErrInvalidRequest)
	}
	if !filepath.IsAbs(helper) {
		resolved, lookErr := exec.LookPath(helper)
		if lookErr != nil {
			return nil, func() {}, fmt.Errorf("%w: helper %q is not installed", ErrCapabilityUnavailable, helper)
		}
		helper = resolved
	}
	helperInfo, helperErr := os.Stat(helper)
	if helperErr != nil || helperInfo.IsDir() || helperInfo.Mode().Perm()&0o111 == 0 {
		if helperErr == nil {
			helperErr = fmt.Errorf("helper is not executable")
		}
		return nil, func() {}, fmt.Errorf("%w: helper %q is not installed: %v", ErrCapabilityUnavailable, helper, helperErr)
	}
	request.Binary = binary
	return newConfinedCommand(ctx, helper, binary, request)
}

// validateConfinedRsyncArgs accepts only the option profile emitted by the
// task executor and managed-tree provider. Values are consumed according to
// the option grammar so a value such as "-L" remains data rather than a
// forwarded switch.
func validateConfinedRsyncArgs(args []string) error {
	_, err := scanConfinedRsyncArgs(args)
	return err
}

// A --copy-dest value must be exactly the pinned basis. Any other path would
// be read through an unpinned lookup outside the granted descriptors. The
// grammar-aware scan keeps option values such as an --exclude pattern named
// "--copy-dest=x" or "--" from being mistaken for switches.
func validateConfinedBasisArgs(args []string, basis string) error {
	copyDests, err := scanConfinedRsyncArgs(args)
	if err != nil {
		return err
	}
	if basis == "" {
		if len(copyDests) > 0 {
			return fmt.Errorf("%w: --copy-dest requires a pinned local basis", ErrInvalidRequest)
		}
		return nil
	}
	if len(copyDests) != 1 || !filepath.IsAbs(copyDests[0]) || filepath.Clean(copyDests[0]) != filepath.Clean(basis) {
		return fmt.Errorf("%w: --copy-dest must name the pinned local basis exactly once", ErrInvalidRequest)
	}
	return nil
}

// scanConfinedRsyncArgs validates args and returns every --copy-dest value.
func scanConfinedRsyncArgs(args []string) ([]string, error) {
	var copyDests []string
	separator := -1
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			separator = index
			break
		}
		if strings.HasPrefix(argument, "--") {
			option, value, hasValue := strings.Cut(argument, "=")
			takesValue, ok := confinedLongOptions[option]
			if !ok {
				return nil, fmt.Errorf("%w: Rsync option %s is not allowed under filesystem confinement", ErrInvalidRequest, option)
			}
			if hasValue {
				if !takesValue {
					return nil, fmt.Errorf("%w: Rsync option %s does not take a value", ErrInvalidRequest, option)
				}
			} else if takesValue {
				if index+1 >= len(args) {
					return nil, fmt.Errorf("%w: Rsync option %s is missing its value", ErrInvalidRequest, option)
				}
				index++
				value = args[index]
			}
			if option == "--copy-dest" {
				copyDests = append(copyDests, value)
			}
			continue
		}
		if strings.HasPrefix(argument, "-") && argument != "-" {
			shortOptions := argument[1:]
			for position := 0; position < len(shortOptions); position++ {
				option := shortOptions[position]
				if option == 'e' {
					if position == len(shortOptions)-1 {
						if index+1 >= len(args) {
							return nil, fmt.Errorf("%w: Rsync option -e is missing its value", ErrInvalidRequest)
						}
						index++
					}
					break
				}
				if !strings.ContainsRune("avz", rune(option)) {
					return nil, fmt.Errorf("%w: Rsync option -%c is not allowed under filesystem confinement", ErrInvalidRequest, option)
				}
			}
			continue
		}
		return nil, fmt.Errorf("%w: unexpected Rsync operand before --", ErrInvalidRequest)
	}
	if separator < 0 {
		return nil, fmt.Errorf("%w: confined Rsync command requires -- before operands", ErrInvalidRequest)
	}
	if len(args)-separator-1 != 2 {
		return nil, fmt.Errorf("%w: confined Rsync command requires exactly two operands", ErrInvalidRequest)
	}
	return copyDests, nil
}

var confinedLongOptions = map[string]bool{
	"--archive":      false,
	"--checksum":     false,
	"--delete":       false,
	"--exclude":      true,
	"--hard-links":   false,
	"--numeric-ids":  false,
	"--fsync":        false,
	"--protect-args": false,
	"--info":         true,
	"--no-devices":   false,
	"--no-specials":  false,
	"--acls":         false,
	"--xattrs":       false,
	"--8-bit-output": false,
	"--dry-run":      false,
	"--out-format":   true,
	"--rsh":          true,
	"--rsync-path":   true,
	"--bwlimit":      true,
	"--copy-dest":    true,
	// Receiver-side only; rsync does not forward it to the server.
	"--itemize-changes": false,
}

// BuildRemoteRsyncPath returns a fixed shell command for Rsync's
// --rsync-path. Every value is independently shell quoted and the helper
// accepts no arbitrary options. role must be "read" or "write".
func BuildRemoteRsyncPath(role, helper, binary string, roots []string, path string, useSudo bool) (string, error) {
	if role != "read" && role != "write" {
		return "", fmt.Errorf("%w: unknown remote role", ErrInvalidRequest)
	}
	if strings.TrimSpace(helper) == "" {
		helper = strings.TrimSpace(os.Getenv(RemoteHelperPathEnv))
	}
	if helper == "" {
		helper = DefaultHelperPath
	}
	if binary == "" {
		binary = "rsync"
	}
	for _, value := range []string{helper, binary, path} {
		if err := validatePathSyntax(value); err != nil {
			return "", fmt.Errorf("%w: remote helper operand: %v", ErrInvalidRequest, err)
		}
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%w: remote operand path must not be empty", ErrInvalidRequest)
	}
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return "", fmt.Errorf("%w: remote operand path must be absolute", ErrInvalidRequest)
	}
	cleanRoots := make([]string, 0, len(roots))
	seenRoots := make(map[string]struct{}, len(roots))
	for _, rawRoot := range roots {
		root := strings.TrimSpace(rawRoot)
		if root == "" {
			continue
		}
		if err := validatePathSyntax(root); err != nil {
			return "", fmt.Errorf("%w: remote helper root: %v", ErrInvalidRequest, err)
		}
		root = filepath.Clean(root)
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("%w: remote helper root must be absolute", ErrInvalidRequest)
		}
		if _, exists := seenRoots[root]; exists {
			continue
		}
		seenRoots[root] = struct{}{}
		cleanRoots = append(cleanRoots, root)
	}
	if len(cleanRoots) == 0 {
		return "", fmt.Errorf("%w: remote helper requires configured roots", ErrCapabilityUnavailable)
	}
	if err := ValidatePath(cleanPath, cleanRoots, "remote rsync operand"); err != nil {
		return "", err
	}
	parts := make([]string, 0, 5+len(cleanRoots)*2)
	if useSudo {
		parts = append(parts, "sudo", "--")
	}
	parts = append(parts, helper, "--protocol="+strconv.Itoa(HelperProtocolVersion), "--binary="+binary, "--mount-"+role+"="+cleanPath)
	if role == "read" {
		parts = append(parts, "--preserve-directory-root=1")
	}
	for _, root := range cleanRoots {
		parts = append(parts, "--"+role+"-root="+root)
	}
	parts = append(parts, "--")
	for i := range parts {
		parts[i] = shellQuote(parts[i])
	}
	return strings.Join(parts, " "), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func helperFD(value int) string { return strconv.Itoa(value) }
