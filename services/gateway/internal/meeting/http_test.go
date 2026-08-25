package meeting

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"metuai/services/gateway/internal/identity"
)

func employeeJWT(t *testing.T, secret []byte) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":          "u-1",
		"kind":         identity.KindEmployee,
		"email":        "a@corp.local",
		"display_name": "Alice",
		"exp":          time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testRouter(t *testing.T) (*gin.Engine, *Store, []byte, []byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	secretEmp := []byte("emp")
	secretGst := []byte("gst")
	store := NewMemoryStore()
	r := gin.New()
	RegisterRoutes(r, store, secretEmp, secretGst, "ws://127.0.0.1:17880", "devkey", "secret", true)
	return r, store, secretEmp, secretGst
}

func createMeeting(t *testing.T, r *gin.Engine, empToken string) (id, password string) {
	t.Helper()
	body := bytes.NewBufferString(`{"title":"t1"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/meetings", body)
	req.Header.Set("Authorization", "Bearer "+empToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created.ID, created.Password
}

func TestCreateMeetingAndGuestCannotGetLivekitBeforeAck(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	id, password := createMeeting(t, r, employeeJWT(t, secretEmp))

	gbody := bytes.NewBufferString(`{"password":"` + password + `","display_name":"Bob"}`)
	greq := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/guest-session", gbody)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, greq)
	if gw.Code != http.StatusOK {
		t.Fatalf("guest session %d %s", gw.Code, gw.Body.String())
	}
	var gs struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(gw.Body.Bytes(), &gs); err != nil {
		t.Fatal(err)
	}

	lt := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/livekit-token", nil)
	lt.Header.Set("Authorization", "Bearer "+gs.Token)
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, lt)
	if lw.Code != http.StatusForbidden {
		t.Fatalf("expected 403 before ack, got %d %s", lw.Code, lw.Body.String())
	}
	var errBody struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(lw.Body.Bytes(), &errBody)
	if errBody.Error != "recording_ack_required" {
		t.Fatalf("error want recording_ack_required got %q body=%s", errBody.Error, lw.Body.String())
	}
}

func TestWrongPasswordRejected(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	id, _ := createMeeting(t, r, employeeJWT(t, secretEmp))

	gbody := bytes.NewBufferString(`{"password":"wrongpass","display_name":"Bob"}`)
	greq := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/guest-session", gbody)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, greq)
	if gw.Code != http.StatusForbidden {
		t.Fatalf("expected 403 invalid password, got %d %s", gw.Code, gw.Body.String())
	}
}

func TestAckThenLivekitTokenOK(t *testing.T) {
	r, _, secretEmp, _ := testRouter(t)
	emp := employeeJWT(t, secretEmp)
	id, password := createMeeting(t, r, emp)

	ack := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/recording-ack", nil)
	ack.Header.Set("Authorization", "Bearer "+emp)
	aw := httptest.NewRecorder()
	r.ServeHTTP(aw, ack)
	if aw.Code != http.StatusOK {
		t.Fatalf("ack %d %s", aw.Code, aw.Body.String())
	}

	lt := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/livekit-token", nil)
	lt.Header.Set("Authorization", "Bearer "+emp)
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, lt)
	if lw.Code != http.StatusOK {
		t.Fatalf("livekit token %d %s", lw.Code, lw.Body.String())
	}
	var tok struct {
		Token      string `json:"token"`
		LivekitURL string `json:"livekit_url"`
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &tok); err != nil {
		t.Fatal(err)
	}
	if tok.Token == "" || tok.LivekitURL == "" {
		t.Fatalf("empty token response %+v", tok)
	}

	// guest path after ack
	gbody := bytes.NewBufferString(`{"password":"` + password + `","display_name":"Bob"}`)
	greq := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/guest-session", gbody)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, greq)
	var gs struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(gw.Body.Bytes(), &gs)
	gack := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/recording-ack", nil)
	gack.Header.Set("Authorization", "Bearer "+gs.Token)
	r.ServeHTTP(httptest.NewRecorder(), gack)
	glt := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+id+"/livekit-token", nil)
	glt.Header.Set("Authorization", "Bearer "+gs.Token)
	glw := httptest.NewRecorder()
	r.ServeHTTP(glw, glt)
	if glw.Code != http.StatusOK {
		t.Fatalf("guest livekit %d %s", glw.Code, glw.Body.String())
	}
}

func TestUnauthorizedCreate(t *testing.T) {
	r, _, _, _ := testRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/meetings", bytes.NewBufferString(`{"title":"x"}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 got %d", w.Code)
	}
}
