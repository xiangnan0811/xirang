package provider

import "errors"

// RclonePublicationError carries facts about one complete publication attempt.
// Its text never contains provider output or private locators.
type RclonePublicationError struct {
	MutationAttempted bool
	CommitAttempted   bool
	WritersQuiesced   bool
	PositiveExitCode  int
	Cause             error
}

func (*RclonePublicationError) Error() string     { return "rclone publication failed" }
func (err *RclonePublicationError) Unwrap() error { return err.Cause }

// RclonePublicationFailure describes only facts that have been positively
// established. An unmarked or contradictory error always has unknown outcome.
type RclonePublicationFailure struct {
	CompletionUnproven bool
	Unattempted        bool
	KnownNonzero       bool
	WriterQuiesced     *bool
	ExitCode           int
}

func ClassifyRclonePublicationFailure(err error) RclonePublicationFailure {
	unknown := RclonePublicationFailure{ExitCode: UnknownProviderExitCode}
	var fact *RclonePublicationError
	if err == nil || !errors.As(err, &fact) || fact == nil || fact.Cause == nil || fact.PositiveExitCode < 0 ||
		fact.CommitAttempted && !fact.MutationAttempted ||
		fact.PositiveExitCode > 0 && (!fact.MutationAttempted || !fact.WritersQuiesced) ||
		!fact.MutationAttempted && !fact.WritersQuiesced {
		return unknown
	}
	unknown.WriterQuiesced = new(fact.WritersQuiesced)
	if !fact.MutationAttempted {
		unknown.Unattempted = true
	} else if !fact.CommitAttempted && fact.WritersQuiesced && fact.PositiveExitCode > 0 {
		unknown.KnownNonzero = true
		unknown.ExitCode = fact.PositiveExitCode
	} else if !fact.CommitAttempted && fact.WritersQuiesced {
		unknown.CompletionUnproven = true
	}
	return unknown
}

// RcloneUnattemptedPublicationError is for exits that occur before the first
// publication mutation can be invoked, including read-only preparation.
func RcloneUnattemptedPublicationError(err error) error {
	if err == nil {
		return nil
	}
	return &RclonePublicationError{WritersQuiesced: true, Cause: err}
}

// rcloneMutationCommandError records the actual command boundary and the
// independent remote exit evidence even if joining or cleanup also failed.
type rcloneMutationCommandError struct {
	attempted    bool
	quiesced     bool
	positiveExit int
	cause        error
}

func (*rcloneMutationCommandError) Error() string     { return "rclone mutation command failed" }
func (err *rcloneMutationCommandError) Unwrap() error { return err.cause }

func rcloneCommandNotAttempted(err error) error {
	if err == nil {
		return nil
	}
	return &rcloneMutationCommandError{quiesced: true, cause: err}
}

type rclonePublicationTracker struct {
	attempted    bool
	commit       bool
	quiesced     bool
	positiveExit int
}

func newRclonePublicationTracker() *rclonePublicationTracker {
	return &rclonePublicationTracker{quiesced: true}
}

func (tracker *rclonePublicationTracker) mutation(err error, commit bool) {
	attempted, quiesced, exit := true, err == nil, 0
	if err != nil {
		var command *rcloneMutationCommandError
		if errors.As(err, &command) && command != nil {
			attempted, quiesced, exit = command.attempted, command.quiesced, command.positiveExit
		}
	}
	if attempted {
		tracker.attempted = true
		tracker.commit = tracker.commit || commit
		tracker.quiesced = tracker.quiesced && quiesced
	}
	tracker.positiveExit = exit
}

func (tracker *rclonePublicationTracker) failure(err error) error {
	if err == nil {
		return nil
	}
	return &RclonePublicationError{MutationAttempted: tracker.attempted, CommitAttempted: tracker.commit,
		WritersQuiesced: tracker.quiesced, PositiveExitCode: tracker.positiveExit, Cause: err}
}
