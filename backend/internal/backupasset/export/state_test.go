package export

import (
	"errors"
	"testing"
	"time"
)

func TestExecutionTransitionsAreClosedAndMonotonic(t *testing.T) {
	allowed := [][2]ExecutionState{
		{ExecutionQueued, ExecutionRunning}, {ExecutionQueued, ExecutionCancelRequested},
		{ExecutionRunning, ExecutionRetryWait}, {ExecutionRunning, ExecutionSealing},
		{ExecutionRetryWait, ExecutionRunning}, {ExecutionSealing, ExecutionRetryWait},
		{ExecutionSealing, ExecutionReady},
		{ExecutionReady, ExecutionCancelRequested}, {ExecutionReady, ExecutionExpiring},
		{ExecutionCancelRequested, ExecutionCanceled}, {ExecutionExpiring, ExecutionExpired},
	}
	for _, transition := range allowed {
		if err := ValidateExecutionTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("allowed %s -> %s: %v", transition[0], transition[1], err)
		}
	}
	for _, transition := range [][2]ExecutionState{
		{ExecutionReady, ExecutionRunning}, {ExecutionFailed, ExecutionQueued},
		{ExecutionCanceled, ExecutionReady}, {ExecutionExpired, ExecutionReady},
	} {
		if err := ValidateExecutionTransition(transition[0], transition[1]); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("invalid %s -> %s error=%v", transition[0], transition[1], err)
		}
	}
}

func TestCleanupAndItemTransitionsRemainOrthogonal(t *testing.T) {
	for _, transition := range [][2]CleanupState{
		{CleanupNone, CleanupRevoking}, {CleanupRevoking, CleanupPurging},
		{CleanupPurging, CleanupPurged}, {CleanupPurging, CleanupPurgeFailed},
		{CleanupPurgeFailed, CleanupPurging},
	} {
		if err := ValidateCleanupTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("cleanup %s -> %s: %v", transition[0], transition[1], err)
		}
	}
	if err := ValidateItemTransition(ItemPacked, ItemPending); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ordinary packed reset error=%v", err)
	}
	if err := ValidateItemReset(ItemPacked, true); err != nil {
		t.Fatalf("fenced byte-zero reset: %v", err)
	}
	if err := ValidateItemReset(ItemPacked, false); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unfenced reset error=%v", err)
	}
}

func TestDeadlineCapsUseExactReturnedLeaseAndRetention(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	leaseEarly := now.Add(90 * time.Minute)
	leaseLate := now.Add(2 * time.Hour)
	retention := now.Add(80 * time.Minute)
	cap, err := ComputeExecutionDeadline(now, 3*time.Hour, 10*time.Minute, []SourceDeadline{
		{AbsoluteDeadline: leaseLate},
		{AbsoluteDeadline: leaseEarly, RetentionUntil: &retention},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cap.Equal(retention) {
		t.Fatalf("execution cap=%s want retention=%s", cap, retention)
	}
	readyAt := now.Add(30 * time.Minute)
	expiresAt, err := ComputeReadyExpiry(readyAt, 24*time.Hour, []SourceDeadline{{AbsoluteDeadline: leaseEarly}})
	if err != nil || !expiresAt.Equal(leaseEarly) {
		t.Fatalf("ready expiry=%s err=%v want=%s", expiresAt, err, leaseEarly)
	}
}

func TestDeadlineCapsRejectReachedOrUnsafeStart(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, deadline := range []time.Time{now, now.Add(4 * time.Minute)} {
		if _, err := ComputeExecutionDeadline(now, time.Hour, 5*time.Minute, []SourceDeadline{{AbsoluteDeadline: deadline}}); !errors.Is(err, ErrDeadlineUnsafe) {
			t.Fatalf("deadline=%s error=%v want ErrDeadlineUnsafe", deadline, err)
		}
	}
}
