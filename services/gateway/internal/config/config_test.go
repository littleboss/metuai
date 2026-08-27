package config

import (
	"testing"
)

func TestFromEnvDoesNotBakeJWTSecrets(t *testing.T) {
	t.Setenv("EMPLOYEE_JWT_SECRET", "")
	t.Setenv("GUEST_JWT_SECRET", "")
	cfg := FromEnv()
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
	cfg := FromEnv()
	if string(cfg.EmployeeJWTSecret) != "emp-only" {
		t.Fatalf("got %q", string(cfg.EmployeeJWTSecret))
	}
	if string(cfg.GuestJWTSecret) != "gst-only" {
		t.Fatalf("got %q", string(cfg.GuestJWTSecret))
	}
}

func TestLiveKitPublicURLDefaultsToLiveKitURL(t *testing.T) {
	t.Setenv("LIVEKIT_URL", "ws://livekit:7880")
	t.Setenv("LIVEKIT_PUBLIC_URL", "")
	cfg := FromEnv()
	if cfg.LiveKitURL != "ws://livekit:7880" {
		t.Fatalf("LiveKitURL=%q", cfg.LiveKitURL)
	}
	if cfg.LiveKitPublicURL != "ws://livekit:7880" {
		t.Fatalf("LiveKitPublicURL should default to LIVEKIT_URL, got %q", cfg.LiveKitPublicURL)
	}
}

func TestLiveKitPublicURLOverride(t *testing.T) {
	t.Setenv("LIVEKIT_URL", "ws://livekit:7880")
	t.Setenv("LIVEKIT_PUBLIC_URL", "ws://127.0.0.1:17880")
	cfg := FromEnv()
	if cfg.LiveKitURL != "ws://livekit:7880" {
		t.Fatalf("LiveKitURL=%q", cfg.LiveKitURL)
	}
	if cfg.LiveKitPublicURL != "ws://127.0.0.1:17880" {
		t.Fatalf("LiveKitPublicURL=%q", cfg.LiveKitPublicURL)
	}
}
