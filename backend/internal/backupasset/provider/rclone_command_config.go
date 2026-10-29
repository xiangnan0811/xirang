package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/sshutil"
)

const rcloneCommandConfigRoot = ".xirang/rclone-command-config"
const rcloneCommandConfigScanLimit = 128

// Each reference belongs to one invocation and is never serialized or accepted
// as a caller-supplied filename. Writable config ownership is independent of
// immutable publication payload leases and content digests.
type rcloneCommandConfigRef struct {
	root        stagedPayloadRoot
	path        string
	operationID string
	marker      []byte
	nodeID      uint
	operation   CommandOperation
}

type rcloneCommandConfigSessionFactory func(context.Context, RemoteCommandAccess, string) (stagedPayloadSession, error)
type rcloneCommandConfigStore struct {
	factory rcloneCommandConfigSessionFactory
	random  io.Reader
}

func configSafeError(err error) error {
	if err == nil {
		return nil
	}
	return stagedPayloadSafeError{message: "rclone command config unavailable", cause: errors.Join(backupasset.ErrProviderUnavailable, err)}
}

func (store *rcloneCommandConfigStore) prepare(ctx context.Context, invocation CommandInvocation) (result *rcloneCommandConfigRef, resultErr error) {
	if store == nil || store.factory == nil || invocation.Runtime == nil || invocation.Runtime.Node.ID == 0 || len(invocation.SecretStdin) == 0 || len(invocation.SecretStdin) > 64<<10 {
		return nil, configSafeError(ErrUnsafeInvocation)
	}
	purpose, ok := sshPurpose(invocation.Purpose)
	if !ok {
		return nil, configSafeError(ErrUnsafeInvocation)
	}
	if err := ctx.Err(); err != nil {
		return nil, configSafeError(err)
	}
	random := store.random
	if random == nil {
		random = rand.Reader
	}
	var entropy [48]byte
	if _, err := io.ReadFull(random, entropy[:]); err != nil {
		return nil, configSafeError(err)
	}
	ref := &rcloneCommandConfigRef{operationID: hex.EncodeToString(entropy[:16]), nodeID: invocation.Runtime.Node.ID, operation: invocation.Operation}
	session, err := store.factory(ctx, *invocation.Runtime, purpose)
	if err != nil {
		return nil, configSafeError(err)
	}
	markerCreated := false
	defer func() {
		resultErr = errors.Join(resultErr, session.Close(), ctx.Err())
		if resultErr != nil {
			result = nil
			if markerCreated {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sshutil.CommandExecutionJoinTimeout)
				defer cancel()
				resultErr = errors.Join(resultErr, store.cleanup(cleanupCtx, invocation, ref))
			}
		}
		resultErr = configSafeError(resultErr)
	}()
	ref.root, err = ensurePrivateSFTPRoot(ctx, session, rcloneCommandConfigRoot)
	if err != nil {
		return nil, err
	}
	directory := path.Join(ref.root.root, ref.operationID)
	// A collision grants no ownership, including no right to chmod or remove it.
	if err := session.Mkdir(directory); err != nil {
		return nil, err
	}
	if err := ensureStrictStagedDirectory(session, directory); err != nil {
		return nil, err
	}
	ref.path = path.Join(directory, "rclone.conf")
	token := hex.EncodeToString(entropy[16:])
	ref.marker = []byte("xirang-rclone-command-config-v1\n" + token + "\n" + ref.root.home + "\n" + ref.operationID + "\n")
	markerPath := path.Join(directory, ".owner-v1")
	markerCreated, err = writeExclusiveStagedFile(session, markerPath, ref.marker)
	if err != nil {
		return nil, err
	}
	if err := verifyStagedPayload(ctx, session, markerPath, int64(len(ref.marker)), sha256Hex(ref.marker), int64(len(ref.marker))); err != nil {
		return nil, err
	}
	if _, err := writeExclusiveStagedFile(session, ref.path, invocation.SecretStdin); err != nil {
		return nil, err
	}
	if err := verifyStagedPayload(ctx, session, ref.path, int64(len(invocation.SecretStdin)), sha256Hex(invocation.SecretStdin), 64<<10); err != nil {
		return nil, err
	}
	if err := verifyConfigDirectories(session, ref); err != nil {
		return nil, err
	}
	return ref, nil
}

func (ref *rcloneCommandConfigRef) arguments(invocation CommandInvocation, arguments []string) ([]string, error) {
	if ref == nil || invocation.Runtime == nil || ref.nodeID != invocation.Runtime.Node.ID || ref.operation != invocation.Operation || ref.path != path.Join(ref.root.root, ref.operationID, "rclone.conf") || len(ref.marker) == 0 {
		return nil, configSafeError(ErrUnsafeInvocation)
	}
	result := make([]string, 0, len(arguments)+2)
	result = append(result, "--config", ref.path)
	return append(result, arguments...), nil
}

func verifyConfigDirectories(session stagedPayloadSession, ref *rcloneCommandConfigRef) error {
	if err := verifyStableStagedRoot(session, ref.root); err != nil {
		return err
	}
	working, err := session.Getwd()
	if err != nil {
		return err
	}
	canonical, err := session.RealPath(working)
	if err != nil || canonical != ref.root.home {
		return backupasset.ErrInvalidState
	}
	for _, directory := range []string{ref.root.home, path.Dir(ref.root.root), ref.root.root, path.Dir(ref.path)} {
		info, err := session.Lstat(directory)
		if err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return backupasset.ErrInvalidState
		}
		if directory != ref.root.home && info.Mode().Perm() != 0700 {
			return backupasset.ErrInvalidState
		}
		canonical, err := session.RealPath(directory)
		if err != nil || canonical != directory {
			return backupasset.ErrInvalidState
		}
	}
	return nil
}

func validRcloneConfigFileName(name string) bool {
	if name == ".owner-v1" || name == "rclone.conf" {
		return true
	}
	suffix, ok := strings.CutPrefix(name, "rclone.conf.old")
	if !ok {
		suffix, ok = strings.CutPrefix(name, "rclone.conf")
	}
	if !ok || len(suffix) < 1 || len(suffix) > 20 {
		return false
	}
	for _, ch := range suffix {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

type boundedConfigDirectorySession interface {
	readConfigDirectory(string) ([]os.FileInfo, error)
}

func (store *rcloneCommandConfigStore) cleanup(ctx context.Context, invocation CommandInvocation, ref *rcloneCommandConfigRef) (resultErr error) {
	defer func() { resultErr = configSafeError(resultErr) }()
	if _, err := ref.arguments(invocation, nil); err != nil {
		return err
	}
	purpose, ok := sshPurpose(invocation.Purpose)
	if !ok {
		return ErrUnsafeInvocation
	}
	session, err := store.factory(ctx, *invocation.Runtime, purpose)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, session.Close(), ctx.Err()) }()
	if err := verifyConfigDirectories(session, ref); err != nil {
		return err
	}
	markerPath := path.Join(path.Dir(ref.path), ".owner-v1")
	if err := verifyStagedPayload(ctx, session, markerPath, int64(len(ref.marker)), sha256Hex(ref.marker), int64(len(ref.marker))); err != nil {
		return err
	}
	bounded, ok := session.(boundedConfigDirectorySession)
	if !ok {
		return backupasset.ErrProviderUnavailable
	}
	entries, err := bounded.readConfigDirectory(path.Dir(ref.path))
	if err != nil {
		return err
	}
	if len(entries) > rcloneCommandConfigScanLimit {
		return ErrRcloneStagedPayloadScanLimitExceeded
	}
	markerSeen := false
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !validRcloneConfigFileName(name) || !entry.Mode().IsRegular() || entry.Mode()&os.ModeSymlink != 0 || entry.Mode().Perm() != 0600 {
			return backupasset.ErrInvalidState
		}
		// Recheck with Lstat; directory listings must not turn symlinks into files.
		info, err := session.Lstat(path.Join(path.Dir(ref.path), name))
		if err != nil || info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0600 {
			return backupasset.ErrInvalidState
		}
		if name == ".owner-v1" {
			markerSeen = true
		} else {
			names = append(names, name)
		}
	}
	if !markerSeen {
		return backupasset.ErrInvalidState
	}
	// No deletion may precede complete enumeration and validation.
	if err := verifyConfigDirectories(session, ref); err != nil {
		return err
	}
	if err := verifyStagedPayload(ctx, session, markerPath, int64(len(ref.marker)), sha256Hex(ref.marker), int64(len(ref.marker))); err != nil {
		return err
	}
	for _, name := range names {
		if err := session.Remove(path.Join(path.Dir(ref.path), name)); err != nil {
			return err
		}
	}
	if err := session.Remove(markerPath); err != nil {
		return err
	}
	return session.RemoveDirectory(path.Dir(ref.path))
}

// The config-only session opens a separate bounded directory subsystem on its
// owned SSH connection. Cancellation closes that connection and joins its owner.
type rcloneConfigSession struct {
	stagedPayloadSession
	openDirectory func() (io.ReadWriteCloser, error)
}

func (session *rcloneConfigSession) readConfigDirectory(directory string) (result []os.FileInfo, resultErr error) {
	wire, err := session.openDirectory()
	if err != nil {
		return nil, err
	}
	bounded, err := sshutil.NewBoundedSFTPDirectorySession(wire, rcloneCommandConfigScanLimit+1, 32)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, bounded.Close()) }()
	reader, err := bounded.OpenDirectory(directory)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
	for {
		entries, err := reader.ReadDir(32)
		if len(entries) > rcloneCommandConfigScanLimit-len(result) {
			return nil, ErrRcloneStagedPayloadScanLimitExceeded
		}
		result = append(result, entries...)
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			return nil, io.ErrNoProgress
		}
	}
}

type configDirectoryChannel struct {
	ssh.Channel
	done <-chan struct{}
	once sync.Once
	err  error
}

func (channel *configDirectoryChannel) Close() error {
	channel.once.Do(func() { channel.err = channel.Channel.Close(); <-channel.done })
	return channel.err
}

func newRcloneConfigSessionFactory(dialer *sshutil.NodeDialer) rcloneCommandConfigSessionFactory {
	return func(ctx context.Context, access RemoteCommandAccess, purpose string) (stagedPayloadSession, error) {
		client, err := dialer.Dial(ctx, access.Node, purpose, access.Audit)
		if err != nil {
			return nil, err
		}
		return newRcloneConfigSession(ctx, client)
	}
}

func newRcloneConfigSession(ctx context.Context, client *ssh.Client) (stagedPayloadSession, error) {
	session, err := newSFTPStagedPayloadSession(ctx, client, func() (*sftp.Client, error) { return sftp.NewClient(client) })
	if err != nil {
		return nil, err
	}
	return &rcloneConfigSession{stagedPayloadSession: session, openDirectory: func() (io.ReadWriteCloser, error) {
		channel, requests, err := client.OpenChannel("session", nil)
		if err != nil {
			return nil, err
		}
		done := make(chan struct{})
		go func() { defer close(done); ssh.DiscardRequests(requests) }()
		owned := &configDirectoryChannel{Channel: channel, done: done}
		accepted, err := channel.SendRequest("subsystem", true, ssh.Marshal(struct{ Name string }{Name: "sftp"}))
		if err != nil || !accepted {
			return nil, errors.Join(fmt.Errorf("SFTP directory subsystem unavailable"), err, owned.Close())
		}
		return owned, nil
	}}, nil
}
