package upload

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveUploadEndpoint(t *testing.T) {
	if got := ResolveUploadEndpoint("http://minio:9000", "http://127.0.0.1:19000"); got != "http://127.0.0.1:19000" {
		t.Fatalf("upload override: %s", got)
	}
	if got := ResolveUploadEndpoint("http://127.0.0.1:19000", ""); got != "http://127.0.0.1:19000" {
		t.Fatalf("fallback: %s", got)
	}
}

func TestS3ConfigReady(t *testing.T) {
	ok := S3Config{
		Endpoint: "http://127.0.0.1:19000", Bucket: "b", AccessKey: "a", SecretKey: "s",
	}
	if !ok.Ready() {
		t.Fatal("expected ready")
	}
	if (S3Config{Endpoint: "http://x", Bucket: "b"}).Ready() {
		t.Fatal("missing secret must not be ready")
	}
}

func TestNewS3BlobStoreNilWhenIncomplete(t *testing.T) {
	s, err := NewS3BlobStore(S3Config{})
	if err != nil || s != nil {
		t.Fatalf("want nil store, got %v %v", s, err)
	}
}

func TestS3BlobStorePutAgainstLiveMinIO(t *testing.T) {
	// 可选冒烟：本机 compose MinIO 在 19000 时跑；否则跳过。
	resp, err := http.Get("http://127.0.0.1:19000/minio/health/live")
	if err != nil || resp.StatusCode != 200 {
		t.Skip("minio not reachable on :19000")
	}
	_ = resp.Body.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "merged.bin")
	if err := os.WriteFile(path, []byte("hello-metuai-local-mic"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewS3BlobStore(S3Config{
		Endpoint: "http://127.0.0.1:19000", Region: "us-east-1", Bucket: "metuai-media",
		AccessKey: "metuai", SecretKey: "metuai-secret", ForcePathStyle: true,
	})
	if err != nil || store == nil {
		t.Fatalf("store: %v %v", store, err)
	}
	key := "local-recording/_smoke/merged.bin"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := store.PutFile(ctx, key, path, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
}
