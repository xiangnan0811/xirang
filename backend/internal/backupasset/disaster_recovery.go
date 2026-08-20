package backupasset

import "fmt"

// DisasterRecoveryFactClass is the closed disaster-recovery rebuild class for a
// control-plane or Provider-derived fact. Provider-derived facts can be rebuilt
// only after valid reconnect/import authority. Control-plane and key-dependent
// facts require the original database plus the matching DATA_ENCRYPTION_KEY.
type DisasterRecoveryFactClass string

const (
	DisasterRecoveryProviderRebuildable DisasterRecoveryFactClass = "provider_rebuildable"
	DisasterRecoveryControlPlane        DisasterRecoveryFactClass = "control_plane"
	DisasterRecoveryKeyDependent        DisasterRecoveryFactClass = "key_dependent"
)

var disasterRecoveryFactClasses = map[string]DisasterRecoveryFactClass{
	"recovery_point":    DisasterRecoveryProviderRebuildable,
	"catalog":           DisasterRecoveryProviderRebuildable,
	"overlay":           DisasterRecoveryControlPlane,
	"audit":             DisasterRecoveryControlPlane,
	"policy":            DisasterRecoveryControlPlane,
	"hold":              DisasterRecoveryControlPlane,
	"task_relationship": DisasterRecoveryControlPlane,
	"binding":           DisasterRecoveryKeyDependent,
	"wrapped_key":       DisasterRecoveryKeyDependent,
	"encrypted_reason":  DisasterRecoveryKeyDependent,
}

func ClassifyDisasterRecoveryFact(fact string) (DisasterRecoveryFactClass, error) {
	class, ok := disasterRecoveryFactClasses[fact]
	if !ok {
		return "", fmt.Errorf("%w: disaster-recovery fact %q", ErrInvalidState, fact)
	}
	return class, nil
}
