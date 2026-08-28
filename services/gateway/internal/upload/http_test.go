package upload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"metuai/services/gateway/internal/identity"
	"metuai/services/gateway/internal/meeting"
)

func TestCompleteWritesLocalMicArtifact(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := []byte("emp-secret")
	repo := meeting.NewMemoryStore()
	m, _, err := repo.Create("demo", "u-1", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	RegisterRoutes(r, store, repo, secret, nil, "metuai-media")

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":          "u-1",
		"kind":         identity.KindEmployee,
		"email":        "u-1@corp.local",
		"display_name": "Alice",
		"exp":          time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}

	part := []byte("pcm-bytes")
	sum := sha256.Sum256(part)
	put := httptest.NewRequest(
		http.MethodPut,
		"/v1/meetings/"+m.ID+"/local-recording/up_9/chunks/0",
		bytes.NewReader(part),
	)
	put.Header.Set("Authorization", "Bearer "+signed)
	put.Header.Set("X-Checksum-Sha256", hex.EncodeToString(sum[:]))
	pw := httptest.NewRecorder()
	r.ServeHTTP(pw, put)
	if pw.Code != http.StatusOK {
		t.Fatalf("put chunk %d %s", pw.Code, pw.Body.String())
	}

	complete := httptest.NewRequest(
		http.MethodPost,
		"/v1/meetings/"+m.ID+"/local-recording/up_9/complete",
		bytes.NewReader([]byte(`{"parts":1}`)),
	)
	complete.Header.Set("Authorization", "Bearer "+signed)
	complete.Header.Set("Content-Type", "application/json")
	cw := httptest.NewRecorder()
	r.ServeHTTP(cw, complete)
	if cw.Code != http.StatusOK {
		t.Fatalf("complete %d %s", cw.Code, cw.Body.String())
	}

	var body struct {
		Checksum  string                `json:"checksum"`
		ObjectKey string                `json:"object_key"`
		Artifact  meeting.MediaArtifact `json:"artifact"`
	}
	if err := json.Unmarshal(cw.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Checksum == "" || body.Artifact.Kind != meeting.KindLocalMic {
		t.Fatalf("unexpected complete body: %+v", body)
	}
	if body.Artifact.Status != "ready" {
		t.Fatalf("local mic must be ready immediately, got %+v", body.Artifact)
	}
	if body.ObjectKey != "metuai-media/local-recording/"+m.ID+"/u-1/up_9/merged.bin" {
		t.Fatalf("want bucket-qualified object key, got %q", body.ObjectKey)
	}

	list, err := repo.ListMediaArtifacts(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Kind != meeting.KindLocalMic {
		t.Fatalf("want one local_mic artifact, got %+v", list)
	}
}

type recordingBlob struct {
	puts []string
	err  error
}

func (r *recordingBlob) Enabled() bool { return true }

func (r *recordingBlob) PutFile(_ context.Context, objectKey, localPath, _ string) error {
	r.puts = append(r.puts, objectKey+"|"+localPath)
	return r.err
}

func TestCompleteUploadsToBlobStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := []byte("emp-secret")
	repo := meeting.NewMemoryStore()
	m, _, err := repo.Create("demo", "u-1", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blobs := &recordingBlob{}
	r := gin.New()
	RegisterRoutes(r, store, repo, secret, blobs, "metuai-media")

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":          "u-1",
		"kind":         identity.KindEmployee,
		"email":        "u-1@corp.local",
		"display_name": "Alice",
		"exp":          time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}

	part := []byte("pcm-bytes")
	sum := sha256.Sum256(part)
	put := httptest.NewRequest(
		http.MethodPut,
		"/v1/meetings/"+m.ID+"/local-recording/up_s3/chunks/0",
		bytes.NewReader(part),
	)
	put.Header.Set("Authorization", "Bearer "+signed)
	put.Header.Set("X-Checksum-Sha256", hex.EncodeToString(sum[:]))
	pw := httptest.NewRecorder()
	r.ServeHTTP(pw, put)
	if pw.Code != http.StatusOK {
		t.Fatalf("put chunk %d %s", pw.Code, pw.Body.String())
	}

	complete := httptest.NewRequest(
		http.MethodPost,
		"/v1/meetings/"+m.ID+"/local-recording/up_s3/complete",
		bytes.NewReader([]byte(`{"parts":1}`)),
	)
	complete.Header.Set("Authorization", "Bearer "+signed)
	complete.Header.Set("Content-Type", "application/json")
	cw := httptest.NewRecorder()
	r.ServeHTTP(cw, complete)
	if cw.Code != http.StatusOK {
		t.Fatalf("complete %d %s", cw.Code, cw.Body.String())
	}
	if len(blobs.puts) != 1 {
		t.Fatalf("want 1 put, got %+v", blobs.puts)
	}
	wantKey := "local-recording/" + m.ID + "/u-1/up_s3/merged.bin"
	if !strings.HasPrefix(blobs.puts[0], wantKey+"|") {
		t.Fatalf("put key wrong: %s", blobs.puts[0])
	}

	var body struct {
		StoredIn string                `json:"stored_in"`
		Artifact meeting.MediaArtifact `json:"artifact"`
	}
	if err := json.Unmarshal(cw.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.StoredIn != "s3" || body.Artifact.Status != "ready" {
		t.Fatalf("want s3 ready, got %+v", body)
	}
	audits, _ := repo.ListAudit(m.ID)
	var saw bool
	for _, e := range audits {
		if e.Action == "local_recording_s3_uploaded" {
			saw = true
		}
	}
	if !saw {
		t.Fatal("expected local_recording_s3_uploaded audit")
	}
}

func TestCompleteMarksFailedWhenBlobPutFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := []byte("emp-secret")
	repo := meeting.NewMemoryStore()
	m, _, err := repo.Create("demo", "u-1", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blobs := &recordingBlob{err: errS3Boom}
	r := gin.New()
	RegisterRoutes(r, store, repo, secret, blobs, "metuai-media")

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u-1", "kind": identity.KindEmployee, "email": "u-1@corp.local",
		"display_name": "Alice", "exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, _ := token.SignedString(secret)
	part := []byte("x")
	sum := sha256.Sum256(part)
	put := httptest.NewRequest(http.MethodPut, "/v1/meetings/"+m.ID+"/local-recording/up_fail/chunks/0", bytes.NewReader(part))
	put.Header.Set("Authorization", "Bearer "+signed)
	put.Header.Set("X-Checksum-Sha256", hex.EncodeToString(sum[:]))
	r.ServeHTTP(httptest.NewRecorder(), put)

	complete := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+m.ID+"/local-recording/up_fail/complete", bytes.NewReader([]byte(`{"parts":1}`)))
	complete.Header.Set("Authorization", "Bearer "+signed)
	complete.Header.Set("Content-Type", "application/json")
	cw := httptest.NewRecorder()
	r.ServeHTTP(cw, complete)
	if cw.Code != http.StatusOK {
		t.Fatalf("complete %d %s", cw.Code, cw.Body.String())
	}
	var body struct {
		StoredIn string                `json:"stored_in"`
		Artifact meeting.MediaArtifact `json:"artifact"`
	}
	_ = json.Unmarshal(cw.Body.Bytes(), &body)
	if body.StoredIn != "local_spool_only" || body.Artifact.Status != "failed" {
		t.Fatalf("want failed local_spool_only, got %+v", body)
	}
}

func TestNonParticipantCannotUploadToMeeting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := []byte("emp-secret")
	repo := meeting.NewMemoryStore()
	m, _, err := repo.Create("private", "u-1", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	RegisterRoutes(r, store, repo, secret, nil, "metuai-media")

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u-2", "kind": identity.KindEmployee, "exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/meetings/"+m.ID+"/local-recording/up_forbidden/status", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-participant upload status must be forbidden, got %d %s", w.Code, w.Body.String())
	}
}

var errS3Boom = errString("boom")

type errString string

func (e errString) Error() string { return string(e) }
