package backupasset

import "testing"

func TestClassifyDisasterRecoveryFactClosedSet(t *testing.T) {
	cases := map[string]DisasterRecoveryFactClass{
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
	for fact, want := range cases {
		got, err := ClassifyDisasterRecoveryFact(fact)
		if err != nil || got != want {
			t.Fatalf("fact %q class=%q err=%v want=%q", fact, got, err, want)
		}
	}
	if _, err := ClassifyDisasterRecoveryFact("provider_locator"); err == nil {
		t.Fatal("unknown disaster-recovery fact was admitted")
	}
	if _, err := ClassifyDisasterRecoveryFact(""); err == nil {
		t.Fatal("empty disaster-recovery fact was admitted")
	}
}
