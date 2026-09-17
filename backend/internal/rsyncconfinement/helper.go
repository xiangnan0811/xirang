package rsyncconfinement

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type helperRequest struct {
	Binary string
	Args   []string

	ReadRoots  []string
	WriteRoots []string
	ExecRoots  []string

	ReadFDs        []int
	RuntimeReadFDs []int
	WriteFDs       []int
	ExecFDs        []int

	MountReadPath  string
	MountWritePath string
	MountExecPath  string
	MountReadFD    int
	MountWriteFD   int
	MountExecFD    int

	// PreserveDirectoryRoot requests the trusted directory-root rewrite used
	// for a no-trailing-slash source operand. It is emitted only by the
	// confinement command builder/remote command builder after validation.
	PreserveDirectoryRoot bool
}
type helperExitError struct {
	code int
}

func (err *helperExitError) Error() string {
	if err == nil {
		return "confined Rsync exited"
	}
	return fmt.Sprintf("confined Rsync exited with code %d", err.code)
}

func (err *helperExitError) ExitCode() int {
	if err == nil {
		return -1
	}
	return err.code
}

// RunHelper is the only entry point used by cmd/rsync-confined. It parses a
// closed argument grammar, installs the platform confinement, and runs the
// requested one-shot Rsync process. It never starts a daemon.
const helperCleanupCommand = "--cleanup-mounts"

func RunHelper(args []string) error {
	if len(args) > 0 && args[0] == helperCleanupCommand {
		return runHelperCleanup(args[1:])
	}
	request, err := parseHelperRequest(args)
	if err != nil {
		return err
	}
	return executeHelper(request)
}
func parseHelperRequest(args []string) (helperRequest, error) {
	var request helperRequest
	request.MountReadFD = -1
	request.MountWriteFD = -1
	request.MountExecFD = -1
	protocolSeen := false
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator == len(args)-1 {
		return request, fmt.Errorf("%w: helper command is empty", ErrInvalidRequest)
	}
	for _, arg := range args[:separator] {
		key, value, ok := strings.Cut(arg, "=")
		if !ok || value == "" || strings.ContainsRune(value, '\x00') {
			return request, fmt.Errorf("%w: malformed helper option", ErrInvalidRequest)
		}
		switch key {
		case "--protocol":
			if protocolSeen || value != strconv.Itoa(HelperProtocolVersion) {
				return request, fmt.Errorf("%w: unsupported helper protocol", ErrInvalidRequest)
			}
			protocolSeen = true
		case "--binary":
			if request.Binary != "" {
				return request, fmt.Errorf("%w: duplicate binary", ErrInvalidRequest)
			}
			request.Binary = value
		case "--read-root":
			request.ReadRoots = appendUnique(request.ReadRoots, value)
		case "--write-root":
			request.WriteRoots = appendUnique(request.WriteRoots, value)
		case "--exec-root":
			request.ExecRoots = appendUnique(request.ExecRoots, value)
		case "--read-fd":
			fd, err := parseFD(value)
			if err != nil {
				return request, err
			}
			request.ReadFDs = append(request.ReadFDs, fd)
		case "--runtime-read-fd":
			fd, err := parseFD(value)
			if err != nil {
				return request, err
			}
			request.RuntimeReadFDs = append(request.RuntimeReadFDs, fd)
		case "--write-fd":
			fd, err := parseFD(value)
			if err != nil {
				return request, err
			}
			request.WriteFDs = append(request.WriteFDs, fd)
		case "--exec-fd":
			fd, err := parseFD(value)
			if err != nil {
				return request, err
			}
			request.ExecFDs = append(request.ExecFDs, fd)
		case "--mount-read":
			if request.MountReadPath != "" {
				return request, fmt.Errorf("%w: duplicate read mount", ErrInvalidRequest)
			}
			request.MountReadPath = value
		case "--preserve-directory-root":
			if request.PreserveDirectoryRoot || value != "1" {
				return request, fmt.Errorf("%w: invalid directory-root preservation flag", ErrInvalidRequest)
			}
			request.PreserveDirectoryRoot = true
		case "--mount-write":
			if request.MountWritePath != "" {
				return request, fmt.Errorf("%w: duplicate write mount", ErrInvalidRequest)
			}
			request.MountWritePath = value
		case "--mount-exec":
			if request.MountExecPath != "" {
				return request, fmt.Errorf("%w: duplicate executable mount", ErrInvalidRequest)
			}
			request.MountExecPath = value
		case "--mount-read-fd":
			fd, err := parseFD(value)
			if err != nil {
				return request, err
			}
			request.MountReadFD = fd
		case "--mount-write-fd":
			fd, err := parseFD(value)
			if err != nil {
				return request, err
			}
			request.MountWriteFD = fd
		case "--mount-exec-fd":
			fd, err := parseFD(value)
			if err != nil {
				return request, err
			}
			request.MountExecFD = fd
		default:
			return request, fmt.Errorf("%w: unknown helper option %q", ErrInvalidRequest, key)
		}
	}
	if !protocolSeen {
		return request, fmt.Errorf("%w: helper protocol is required", ErrInvalidRequest)
	}
	if request.Binary == "" || strings.ContainsRune(request.Binary, '\x00') {
		return request, fmt.Errorf("%w: missing binary", ErrInvalidRequest)
	}
	request.Args = append([]string(nil), args[separator+1:]...)
	return request, nil

}
func parseFD(value string) (int, error) {
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 || fd > 1<<20 {
		return -1, fmt.Errorf("%w: invalid helper fd", ErrInvalidRequest)
	}
	return fd, nil
}

func appendUnique(values []string, value string) []string {
	clean := filepath.Clean(value)
	for _, existing := range values {
		if existing == clean {
			return values
		}
	}
	return append(values, clean)
}

func closeHelperFDs(request helperRequest) {
	closeHelperFDsExcept(request, nil)
}

func closeHelperFDsExcept(request helperRequest, keep map[int]struct{}) {
	seen := make(map[int]struct{})
	fds := make([]int, 0, len(request.ReadFDs)+len(request.RuntimeReadFDs)+len(request.WriteFDs)+len(request.ExecFDs)+3)
	fds = append(fds, request.ReadFDs...)
	fds = append(fds, request.RuntimeReadFDs...)
	fds = append(fds, request.WriteFDs...)
	fds = append(fds, request.ExecFDs...)
	fds = append(fds, request.MountReadFD, request.MountWriteFD, request.MountExecFD)
	for _, fd := range fds {
		if fd < 0 {
			continue
		}
		if _, ok := seen[fd]; ok {
			continue
		}
		seen[fd] = struct{}{}
		if _, ok := keep[fd]; ok {
			continue
		}
		_ = syscall.Close(fd)
	}
}
