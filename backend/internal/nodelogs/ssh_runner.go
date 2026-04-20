package nodelogs

import (
	"context"
	"fmt"
	"io"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"gorm.io/gorm"
)

// sshRunner is the production Runner. It dials the node each call.
type sshRunner struct {
	db *gorm.DB
}

func NewSSHRunner(db *gorm.DB) Runner { return &sshRunner{db: db} }

func (r *sshRunner) Run(ctx context.Context, node model.Node, cmd string, timeout time.Duration, maxBytes int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	auth, err := sshutil.BuildSSHAuth(node, r.db)
	if err != nil {
		return "", fmt.Errorf("build auth: %w", err)
	}
	hostKey, err := sshutil.ResolveSSHHostKeyCallback()
	if err != nil {
		return "", fmt.Errorf("host key: %w", err)
	}
	addr := fmt.Sprintf("%s:%d", node.Host, node.Port)
	client, err := sshutil.DialSSH(ctx, addr, node.Username, auth, hostKey)
	if err != nil {
		return "", fmt.Errorf("dial: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("session: %w", err)
	}
	defer session.Close()

	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout: %w", err)
	}
	if err := session.Start(cmd); err != nil {
		return "", fmt.Errorf("start: %w", err)
	}

	limited := io.LimitReader(stdout, int64(maxBytes))
	buf, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	if err := session.Wait(); err != nil {
		// Partial output is useful; some shells return nonzero even on success.
		return string(buf), nil
	}
	return string(buf), nil
}
