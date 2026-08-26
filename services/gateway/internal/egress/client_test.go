package egress

import (
	"context"
	"testing"
)

func TestHttpHost(t *testing.T) {
	cases := map[string]string{
		"ws://127.0.0.1:17880":  "http://127.0.0.1:17880",
		"wss://livekit.example": "https://livekit.example",
		"http://livekit:7880":   "http://livekit:7880",
	}
	for in, want := range cases {
		if got := httpHost(in); got != want {
			t.Fatalf("httpHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// NoopClient 必须对每个方法都报错，这样上层只会走降级分支，不会误判成功。
func TestNoopClientAlwaysFails(t *testing.T) {
	ctx := context.Background()
	var c Client = NoopClient{}
	if _, err := c.StartRoomComposite(ctx, "mtg_1", OutputSpec{}); err == nil {
		t.Fatal("StartRoomComposite should fail")
	}
	if _, err := c.StartParticipant(ctx, "mtg_1", "u-1", OutputSpec{}); err == nil {
		t.Fatal("StartParticipant should fail")
	}
	if _, err := c.ListParticipants(ctx, "mtg_1"); err == nil {
		t.Fatal("ListParticipants should fail")
	}
	if _, err := c.Stop(ctx, "eg_1"); err == nil {
		t.Fatal("Stop should fail")
	}
	handle, err := c.Describe(ctx, "eg_1")
	if err == nil {
		t.Fatal("Describe should fail")
	}
	if handle.Succeeded() {
		t.Fatalf("unknown handle must not look successful: %+v", handle)
	}
}

func TestHandleTerminalAndSucceeded(t *testing.T) {
	if !(Handle{Status: StatusComplete}).Succeeded() {
		t.Fatal("complete without error should succeed")
	}
	// LiveKit 会在 COMPLETE 上带 error 表示部分失败，这种不能算成功。
	if (Handle{Status: StatusComplete, Error: "upload failed"}).Succeeded() {
		t.Fatal("complete with error must not succeed")
	}
	if (Handle{Status: StatusActive}).Terminal() {
		t.Fatal("active is not terminal")
	}
	if !(Handle{Status: StatusFailed}).Terminal() {
		t.Fatal("failed is terminal")
	}
	if (Handle{Status: StatusUnknown}).Terminal() {
		t.Fatal("unknown must not be terminal, otherwise we would finalize on no evidence")
	}
}
