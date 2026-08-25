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

func TestCreateMeetingAndGuestCannotGetLivekitBeforeAck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secretEmp := []byte("emp")
	secretGst := []byte("gst")
	store := NewMemoryStore()
	r := gin.New()
	RegisterRoutes(r, store, secretEmp, secretGst, "ws://localhost:7880", "devkey", "secret", true)

	body := bytes.NewBufferString(`{"title":"t1"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/meetings", body)
	req.Header.Set("Authorization", "Bearer "+employeeJWT(t, secretEmp))
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

	gbody := bytes.NewBufferString(`{"password":"` + created.Password + `","display_name":"Bob"}`)
	greq := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+created.ID+"/guest-session", gbody)
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

	lt := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+created.ID+"/livekit-token", nil)
	lt.Header.Set("Authorization", "Bearer "+gs.Token)
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, lt)
	if lw.Code != http.StatusForbidden {
		t.Fatalf("expected 403 before ack, got %d %s", lw.Code, lw.Body.String())
	}
}
