package meeting

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/egress"
	"metuai/services/gateway/internal/knowledge"
	"metuai/services/gateway/internal/ready"
)

type stubMediaSigner struct{}

func (stubMediaSigner) SignGetURL(_ context.Context, objectKey string, _ time.Duration) (string, error) {
	return "https://minio.example/" + objectKey + "?sig=test", nil
}

func testRouterWithMediaSigner(t *testing.T) (*gin.Engine, *Store, []byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	stubPrivateLLM(t, "")
	secretEmp := []byte("emp")
	secretGst := []byte("gst")
	store := NewMemoryStore()
	r := gin.New()
	RegisterRoutes(r, store, secretEmp, secretGst, "ws://127.0.0.1:17880", "devkey", "secret", true, "metuai-media", nil, knowledge.NewMemoryIndex(), NewBreakGlassStore(), NewGuestEmailVerifier(store, nil), stubMediaSigner{}, ready.AlwaysReady())
	return r, store, secretEmp
}

func TestMediaGet_Unauthorized(t *testing.T) {
	r, _, secretEmp := testRouterWithMediaSigner(t)
	emp := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, emp)

	got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/media", "", "")
	if got.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned media want 401 got %d %s", got.Code, got.Body.String())
	}
}

func TestMediaGet_Forbidden(t *testing.T) {
	r, store, secretEmp := testRouterWithMediaSigner(t)
	organizer := employeeJWT(t, secretEmp)
	stranger := employeeJWTFor(t, secretEmp, "u-stranger", "Stranger")
	id, _ := createMeeting(t, r, organizer)
	_ = store.AckRecording(id, PrincipalKey("employee", "u-1", ""))

	got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/media", stranger, "")
	if got.Code != http.StatusForbidden {
		t.Fatalf("stranger media want 403 got %d %s", got.Code, got.Body.String())
	}
}

func TestMediaGet_NotFound(t *testing.T) {
	r, _, secretEmp := testRouterWithMediaSigner(t)
	emp := employeeJWT(t, secretEmp)

	got := doJSON(t, r, http.MethodGet, "/v1/meetings/mtg_nonexistent/media", emp, "")
	if got.Code != http.StatusNotFound {
		t.Fatalf("unknown meeting media want 404 got %d %s", got.Code, got.Body.String())
	}
}

func TestMediaGet_EmptyArtifacts200(t *testing.T) {
	r, store, secretEmp := testRouterWithMediaSigner(t)
	organizer := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, organizer)
	_ = store.AckRecording(id, PrincipalKey("employee", "u-1", ""))
	if end := doJSON(t, r, http.MethodPost, "/v1/meetings/"+id+"/end", organizer, ""); end.Code != http.StatusOK {
		t.Fatalf("end %d %s", end.Code, end.Body.String())
	}

	got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/media", organizer, "")
	if got.Code != http.StatusOK {
		t.Fatalf("empty media want 200 got %d %s", got.Code, got.Body.String())
	}
	var body struct {
		Artifacts []MediaArtifact `json:"artifacts"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, a := range body.Artifacts {
		if a.Status == "ready" {
			t.Fatalf("must not invent ready artifacts: %+v", a)
		}
		if a.DownloadURL != "" {
			t.Fatalf("non-ready must not have download_url: %+v", a)
		}
	}
}

func TestMediaGet_NonReadyArtifacts200(t *testing.T) {
	r, store, secretEmp := testRouterWithMediaSigner(t)
	organizer := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, organizer)
	_ = store.AckRecording(id, PrincipalKey("employee", "u-1", ""))
	if _, err := store.AddMediaArtifact(MediaArtifact{
		MeetingID: id, Kind: egress.KindRoomVideo, Status: "pending", ObjectKey: "metuai-media/" + id + "/room.mp4",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMediaArtifact(MediaArtifact{
		MeetingID: id, Kind: egress.KindRoomAudio, Status: "started", ObjectKey: "metuai-media/" + id + "/room.ogg",
	}); err != nil {
		t.Fatal(err)
	}

	got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/media", organizer, "")
	if got.Code != http.StatusOK {
		t.Fatalf("non-ready media want 200 got %d %s", got.Code, got.Body.String())
	}
	var body struct {
		Artifacts []MediaArtifact `json:"artifacts"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Artifacts) != 2 {
		t.Fatalf("expected 2 artifacts, got %+v", body.Artifacts)
	}
	for _, a := range body.Artifacts {
		if a.DownloadURL != "" {
			t.Fatalf("non-ready must not sign download_url: %+v", a)
		}
	}
}

func TestMediaGet_ReadyIncludesDownloadURL(t *testing.T) {
	r, store, secretEmp := testRouterWithMediaSigner(t)
	organizer := employeeJWT(t, secretEmp)
	id, _ := createMeeting(t, r, organizer)
	_ = store.AckRecording(id, PrincipalKey("employee", "u-1", ""))
	key := "metuai-media/" + id + "/room.mp4"
	if _, err := store.AddMediaArtifact(MediaArtifact{
		MeetingID: id, Kind: KindRoomVideo, Status: "ready", ObjectKey: key,
	}); err != nil {
		t.Fatal(err)
	}

	got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/media", organizer, "")
	if got.Code != http.StatusOK {
		t.Fatalf("ready media want 200 got %d %s", got.Code, got.Body.String())
	}
	var body struct {
		Artifacts []MediaArtifact `json:"artifacts"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %+v", body.Artifacts)
	}
	a := body.Artifacts[0]
	if a.Status != "ready" {
		t.Fatalf("artifact status = %q", a.Status)
	}
	if a.DownloadURL == "" {
		t.Fatal("ready artifact must include presigned download_url")
	}
	if a.ObjectKey != key {
		t.Fatalf("object_key = %q", a.ObjectKey)
	}
}

func TestMediaGet_ParticipantCanRead(t *testing.T) {
	r, store, secretEmp := testRouterWithMediaSigner(t)
	organizer := employeeJWT(t, secretEmp)
	participant := employeeJWTFor(t, secretEmp, "u-2", "Bob")
	id, _ := createMeeting(t, r, organizer)
	_ = store.AckRecording(id, PrincipalKey("employee", "u-1", ""))
	_ = store.AckRecording(id, PrincipalKey("employee", "u-2", ""))

	got := doJSON(t, r, http.MethodGet, "/v1/meetings/"+id+"/media", participant, "")
	if got.Code != http.StatusOK {
		t.Fatalf("participant media want 200 got %d %s", got.Code, got.Body.String())
	}
}
