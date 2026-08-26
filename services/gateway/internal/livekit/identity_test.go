package livekit

import "testing"

func TestDeviceIdentityAndUserKey(t *testing.T) {
	if got := DeviceIdentity("u-1", ""); got != "u-1" {
		t.Fatalf("empty device: %q", got)
	}
	if got := DeviceIdentity("u-1", "dev_ab!"); got != "u-1~dev_ab" {
		t.Fatalf("sanitized: %q", got)
	}
	if got := UserKey("u-1~phone"); got != "u-1" {
		t.Fatalf("user key: %q", got)
	}
	if got := UserKey("guest:abc~tab1"); got != "guest:abc" {
		t.Fatalf("guest key: %q", got)
	}
	if got := UserKey("u-1"); got != "u-1" {
		t.Fatalf("plain: %q", got)
	}
}
