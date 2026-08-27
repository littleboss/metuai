package config

import (
	"testing"
)

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
