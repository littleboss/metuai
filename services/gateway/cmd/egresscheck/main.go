// Command egresscheck：探测 LiveKit Egress → MinIO。
//
// 策略：进房并发布静音 Opus 轨，再开 **ParticipantEgress**（不依赖 RoomComposite 的 Chrome）。
// 刻意不用 livekit/media-sdk（需 libopus / pkg-config），只用 SDK 自带的 NullSampleProvider。
//
//	set -a && source infra/compose/.env.example && set +a
//	export EGRESS_ENABLED=true
//	export S3_ENDPOINT=http://minio:9000
//	cd services/gateway && go run ./cmd/egresscheck
package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"metuai/services/gateway/internal/config"
	"metuai/services/gateway/internal/egress"
)

const smokeIdentity = "egress-smoke-pub"

func main() {
	cfg := config.FromEnv()
	eg := cfg.EgressConfig()
	if !cfg.EgressEnabled {
		log.Fatal("set EGRESS_ENABLED=true before running egresscheck")
	}
	if !eg.Ready() {
		log.Fatal("egress config incomplete (need LiveKit + S3 env)")
	}
	if eg.UsesLoopbackS3() {
		log.Fatalf("S3_ENDPOINT=%s looks like loopback; use http://minio:9000", eg.S3Endpoint)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := egress.NewLiveKitClient(eg)
	roomName := fmt.Sprintf("egress-smoke-%d", time.Now().Unix())

	if err := ensureRoom(ctx, eg, roomName); err != nil {
		log.Fatalf("CreateRoom: %v", err)
	}

	room, err := joinAndPublishSilence(eg, roomName)
	if err != nil {
		log.Fatalf("join/publish: %v", err)
	}
	defer room.Disconnect()

	log.Printf("participant online; trying RoomComposite first (Chrome needs someone in-room)")
	if tryRoomComposite(ctx, client, roomName) {
		return
	}

	path := egress.ParticipantPath(roomName, smokeIdentity)
	log.Printf("fallback: ParticipantEgress filepath=%s (mp4)", path)
	handle, err := client.StartParticipant(ctx, roomName, smokeIdentity, egress.OutputSpec{
		AudioOnly: true,
		FileType:  egress.FileMP4,
		Filepath:  path,
	})
	if err != nil {
		log.Fatalf("StartParticipant failed: %v", err)
	}
	log.Printf("PIPELINE OK: egress_id=%s status=%s", handle.EgressID, handle.Status)

	if err := waitStatus(ctx, client, handle.EgressID, egress.StatusActive, 60*time.Second); err != nil {
		log.Fatalf("ParticipantEgress: %v", err)
	}

	log.Printf("ACTIVE — record 4s then stop")
	time.Sleep(4 * time.Second)
	_ = stopQuiet(ctx, client, handle.EgressID)

	final, err := waitTerminal(ctx, client, handle.EgressID, 60*time.Second)
	if err != nil {
		log.Fatalf("%v", err)
	}
	if !final.Succeeded() {
		log.Fatalf("egress not successful: status=%s error=%q", final.Status, final.Error)
	}

	prefix := strings.TrimSuffix(path, filepath.Base(path))
	if err := listMinIO(prefix); err != nil {
		log.Fatalf("COMPLETE but MinIO list failed: %v", err)
	}
	log.Printf("COMPLETE OK: MinIO has objects under %s", prefix)
}

func tryRoomComposite(ctx context.Context, client egress.Client, roomName string) bool {
	path := egress.RoomAudioPath(roomName)
	log.Printf("fallback: StartRoomComposite filepath=%s", path)
	handle, err := client.StartRoomComposite(ctx, roomName, egress.OutputSpec{
		AudioOnly: true,
		FileType:  egress.FileOGG,
		Filepath:  path,
	})
	if err != nil {
		log.Printf("RoomComposite start failed: %v", err)
		return false
	}
	if err := waitStatus(ctx, client, handle.EgressID, egress.StatusActive, 45*time.Second); err != nil {
		log.Printf("RoomComposite: %v", err)
		_ = stopQuiet(ctx, client, handle.EgressID)
		return false
	}
	time.Sleep(3 * time.Second)
	_ = stopQuiet(ctx, client, handle.EgressID)
	final, err := waitTerminal(ctx, client, handle.EgressID, 45*time.Second)
	if err != nil || !final.Succeeded() {
		log.Printf("RoomComposite finalize failed: %v status=%+v", err, final)
		return false
	}
	prefix := strings.TrimSuffix(path, filepath.Base(path))
	if err := listMinIO(prefix); err != nil {
		log.Printf("RoomComposite COMPLETE but MinIO list failed: %v", err)
		return false
	}
	log.Printf("COMPLETE OK via RoomComposite under %s", prefix)
	return true
}

func joinAndPublishSilence(eg egress.Config, roomName string) (*lksdk.Room, error) {
	room, err := lksdk.ConnectToRoom(eg.LiveKitURL, lksdk.ConnectInfo{
		APIKey:              eg.LiveKitAPIKey,
		APISecret:           eg.LiveKitAPISecret,
		RoomName:            roomName,
		ParticipantIdentity: smokeIdentity,
		ParticipantName:     "Egress Smoke",
	}, &lksdk.RoomCallback{})
	if err != nil {
		return nil, err
	}

	track, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus})
	if err != nil {
		room.Disconnect()
		return nil, err
	}
	// ~64kbps 静音填充；够让 SFU 看到一条活跃音轨。
	provider := lksdk.NewNullSampleProvider(64_000)
	if err := track.StartWrite(provider, func() {}); err != nil {
		room.Disconnect()
		return nil, err
	}
	if _, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:   "smoke-silence",
		Source: livekit.TrackSource_MICROPHONE,
	}); err != nil {
		room.Disconnect()
		return nil, err
	}

	time.Sleep(2 * time.Second)
	return room, nil
}

func waitStatus(ctx context.Context, client egress.Client, id, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		h, err := client.Describe(ctx, id)
		if err != nil {
			return err
		}
		log.Printf("wait %s: status=%s err=%q", want, h.Status, h.Error)
		if h.Status == want {
			return nil
		}
		if h.Terminal() {
			return fmt.Errorf("egress terminal before %s: status=%s error=%q", want, h.Status, h.Error)
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("timeout waiting for %s", want)
}

func waitTerminal(ctx context.Context, client egress.Client, id string, timeout time.Duration) (egress.Handle, error) {
	deadline := time.Now().Add(timeout)
	var last egress.Handle
	for time.Now().Before(deadline) {
		h, err := client.Describe(ctx, id)
		if err != nil {
			return egress.Handle{}, err
		}
		last = h
		log.Printf("wait terminal: status=%s files=%v err=%q", h.Status, h.Files, h.Error)
		if h.Terminal() {
			return h, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return last, fmt.Errorf("timeout waiting terminal (last=%s)", last.Status)
}

func stopQuiet(ctx context.Context, client egress.Client, id string) error {
	_, err := client.Stop(ctx, id)
	return err
}

func listMinIO(prefix string) error {
	alias := exec.Command(
		"docker", "exec", "compose-minio-1",
		"/usr/bin/mc", "alias", "set", "local", "http://127.0.0.1:9000", "metuai", "metuai-secret",
	)
	if out, err := alias.CombinedOutput(); err != nil {
		return fmt.Errorf("mc alias: %v (%s)", err, out)
	}
	list := exec.Command(
		"docker", "exec", "compose-minio-1",
		"/usr/bin/mc", "ls", "local/metuai-media/"+prefix,
	)
	out, err := list.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mc ls: %v (%s)", err, out)
	}
	log.Printf("minio ls:\n%s", out)
	if len(strings.TrimSpace(string(out))) == 0 {
		return fmt.Errorf("empty listing for prefix %s", prefix)
	}
	return nil
}

func ensureRoom(ctx context.Context, eg egress.Config, room string) error {
	host := strings.TrimSpace(eg.LiveKitURL)
	host = strings.Replace(host, "wss://", "https://", 1)
	host = strings.Replace(host, "ws://", "http://", 1)
	rooms := lksdk.NewRoomServiceClient(host, eg.LiveKitAPIKey, eg.LiveKitAPISecret)
	_, err := rooms.CreateRoom(ctx, &livekit.CreateRoomRequest{Name: room})
	return err
}
