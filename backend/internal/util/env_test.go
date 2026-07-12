package util

import "testing"

func TestIsDevelopmentEnvIgnoresGinMode(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "debug")
	if IsDevelopmentEnv() {
		t.Fatal("GIN_MODE=debug must not make IsDevelopmentEnv true when APP_ENV=production")
	}
	if !IsProductionEnv() {
		t.Fatal("expected IsProductionEnv for APP_ENV=production")
	}
	if !IsGinDebug() {
		t.Fatal("expected IsGinDebug when GIN_MODE=debug")
	}
}

func TestIsDevelopmentEnvRequiresExplicitAppEnv(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "debug")
	if IsDevelopmentEnv() {
		t.Fatal("empty APP_ENV with GIN_MODE=debug must not be development")
	}

	t.Setenv("APP_ENV", "development")
	if !IsDevelopmentEnv() {
		t.Fatal("APP_ENV=development should be development")
	}
}

func TestIsDevelopmentEnvViaEnvironmentVar(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("GIN_MODE", "")
	if !IsDevelopmentEnv() {
		t.Fatal("ENVIRONMENT=development should be development when APP_ENV unset")
	}
}

func TestAppEnvTakesPrecedenceOverEnvironment(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("GIN_MODE", "")
	if IsDevelopmentEnv() {
		t.Fatal("APP_ENV=production must win over ENVIRONMENT=development")
	}
	if !IsProductionEnv() {
		t.Fatal("expected production when APP_ENV=production")
	}

	t.Setenv("APP_ENV", "development")
	t.Setenv("ENVIRONMENT", "production")
	if !IsDevelopmentEnv() {
		t.Fatal("APP_ENV=development must win over ENVIRONMENT=production")
	}
	if IsProductionEnv() {
		t.Fatal("APP_ENV=development must not report production")
	}
}

func TestGinModeReleaseIsProductionWhenAppEnvUnset(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "release")
	if !IsProductionEnv() {
		t.Fatal("legacy GIN_MODE=release with unset APP_ENV must be production")
	}
	if IsDevelopmentEnv() {
		t.Fatal("GIN_MODE=release must not be development")
	}
}

func TestAppEnvDevelopmentWinsOverGinRelease(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "release")
	if !IsDevelopmentEnv() {
		t.Fatal("APP_ENV=development must win over GIN_MODE=release")
	}
	if IsProductionEnv() {
		t.Fatal("APP_ENV=development must not be production even if GIN_MODE=release")
	}
}

func TestProductionAliasesAlwaysProduction(t *testing.T) {
	// prod/staging must harden even without GIN_MODE=release.
	for _, env := range []string{"prod", "staging", "production"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("APP_ENV", env)
			t.Setenv("ENVIRONMENT", "")
			t.Setenv("GIN_MODE", "")
			if !IsProductionEnv() {
				t.Fatalf("APP_ENV=%s must be production regardless of GIN_MODE", env)
			}
			if IsDevelopmentEnv() {
				t.Fatalf("APP_ENV=%s must not be development", env)
			}
		})
	}
}

func TestUnknownAppEnvFailsClosedAsProduction(t *testing.T) {
	t.Setenv("APP_ENV", "canary")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "debug")
	if !IsProductionEnv() {
		t.Fatal("unknown APP_ENV must fail closed as production so Swagger/METRICS/CORS stay hardened")
	}
	if IsDevelopmentEnv() {
		t.Fatal("unknown APP_ENV must not be development")
	}
}

func TestEmptyAppEnvFailsClosedAsProduction(t *testing.T) {
	// Undeclared APP_ENV must not skip METRICS_TOKEN / Swagger / CORS hardening
	// even when GIN_MODE is unset or debug.
	t.Setenv("APP_ENV", "")
	t.Setenv("ENVIRONMENT", "")
	t.Setenv("GIN_MODE", "")
	if !IsProductionEnv() {
		t.Fatal("unset APP_ENV must fail closed as production")
	}
	t.Setenv("GIN_MODE", "debug")
	if !IsProductionEnv() {
		t.Fatal("unset APP_ENV with GIN_MODE=debug must still be production")
	}
}
