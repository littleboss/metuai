package meeting

import "testing"

func TestBrowserLiveKitURL(t *testing.T) {
	t.Setenv("LIVEKIT_PUBLIC_URL", "")
	if got := browserLiveKitURL("ws://livekit:7880"); got != "ws://livekit:7880" {
		t.Fatalf("unset public URL: got %q", got)
	}

	t.Setenv("LIVEKIT_PUBLIC_URL", "ws://127.0.0.1:17880")
	if got := browserLiveKitURL("ws://livekit:7880"); got != "ws://127.0.0.1:17880" {
		t.Fatalf("public URL override: got %q", got)
	}
}
