package capabilities

import "testing"

func TestSecretClassificationIsOptionalAndFailClosed(t *testing.T) {
	if result := ClassifySecret([]byte("token=FAKE_TOKEN_FOR_TEST_ONLY"), false); result.Sensitivity != SensitivityUnknown {
		t.Fatalf("disabled classifier result=%+v", result)
	}
	if result := ClassifySecret([]byte("token=FAKE_TOKEN_FOR_TEST_ONLY"), true); result.Sensitivity != SensitivitySecret || len(result.Categories) != 1 {
		t.Fatalf("secret classifier result=%+v", result)
	}
	if result := ClassifySecret([]byte{0xff, 0xfe, 0xfd}, true); result.Sensitivity != SensitivityUnknown {
		t.Fatalf("binary classifier result=%+v", result)
	}
}
