package config_test

import (
	"testing"

	"metuai/services/gateway/internal/config"
)

func TestFromEnvDoesNotBakeJWTSecrets(t *testing.T) {
	t.Setenv("EMPLOYEE_JWT_SECRET", "")
	t.Setenv("GUEST_JWT_SECRET", "")
	cfg := config.FromEnv()
	if len(cfg.EmployeeJWTSecret) != 0 {
		t.Fatalf("EMPLOYEE_JWT_SECRET must stay empty when unset, got %q", string(cfg.EmployeeJWTSecret))
	}
	if len(cfg.GuestJWTSecret) != 0 {
		t.Fatalf("GUEST_JWT_SECRET must stay empty when unset, got %q", string(cfg.GuestJWTSecret))
	}
}

func TestFromEnvReadsExplicitJWTSecrets(t *testing.T) {
	t.Setenv("EMPLOYEE_JWT_SECRET", "emp-only")
	t.Setenv("GUEST_JWT_SECRET", "gst-only")
	cfg := config.FromEnv()
	if string(cfg.EmployeeJWTSecret) != "emp-only" {
		t.Fatalf("got %q", string(cfg.EmployeeJWTSecret))
	}
	if string(cfg.GuestJWTSecret) != "gst-only" {
		t.Fatalf("got %q", string(cfg.GuestJWTSecret))
	}
}
