package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/auth"
	"metuai/services/gateway/internal/identity"
	"metuai/services/gateway/internal/knowledge"
	"metuai/services/gateway/internal/meeting"
	"metuai/services/gateway/internal/ready"
)

func testAuthRouter(t *testing.T, secret []byte) (*gin.Engine, auth.UserStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	users := auth.NewMemoryStore()
	r := gin.New()
	auth.RegisterRoutes(r, users, secret)
	store := meeting.NewMemoryStore()
	meeting.RegisterRoutes(r, store, secret, []byte("gst"), "ws://127.0.0.1:17880", "devkey", "secret", true, "metuai-media", nil, knowledge.NewMemoryIndex(), meeting.NewBreakGlassStore(), meeting.NewGuestEmailVerifier(store, nil), nil, ready.AlwaysReady())
	return r, users
}

func doJSON(t *testing.T, r *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRegisterLoginHappyPathAndCreateMeeting(t *testing.T) {
	secret := []byte("emp-secret-for-auth")
	r, _ := testAuthRouter(t, secret)

	reg := doJSON(t, r, http.MethodPost, "/v1/auth/register", "",
		`{"email":"alice@corp.local","password":"password1","name":"Alice"}`)
	if reg.Code != http.StatusCreated {
		t.Fatalf("register %d %s", reg.Code, reg.Body.String())
	}
	var regBody struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(reg.Body.Bytes(), &regBody)
	if regBody.AccessToken == "" {
		t.Fatal("missing access_token")
	}
	p, err := identity.ParseEmployeeToken(regBody.AccessToken, secret)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != identity.KindEmployee || p.Email != "alice@corp.local" || p.DisplayName != "Alice" || p.UserID == "" {
		t.Fatalf("claims %+v", p)
	}

	login := doJSON(t, r, http.MethodPost, "/v1/auth/login", "",
		`{"email":"Alice@corp.local","password":"password1"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login %d %s", login.Code, login.Body.String())
	}
	var loginBody struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(login.Body.Bytes(), &loginBody)
	if loginBody.AccessToken == "" {
		t.Fatal("login missing token")
	}

	create := doJSON(t, r, http.MethodPost, "/v1/meetings", loginBody.AccessToken, `{"title":"from-login"}`)
	if create.Code != http.StatusOK {
		t.Fatalf("create meeting with login token %d %s", create.Code, create.Body.String())
	}
}

func TestRegisterDuplicateEmail409(t *testing.T) {
	r, _ := testAuthRouter(t, []byte("emp"))
	first := doJSON(t, r, http.MethodPost, "/v1/auth/register", "",
		`{"email":"dup@corp.local","password":"password1"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first %d %s", first.Code, first.Body.String())
	}
	second := doJSON(t, r, http.MethodPost, "/v1/auth/register", "",
		`{"email":"DUP@corp.local","password":"password1"}`)
	if second.Code != http.StatusConflict {
		t.Fatalf("dup want 409 got %d %s", second.Code, second.Body.String())
	}
}

func TestRegisterPasswordTooShort400(t *testing.T) {
	r, _ := testAuthRouter(t, []byte("emp"))
	got := doJSON(t, r, http.MethodPost, "/v1/auth/register", "",
		`{"email":"short@corp.local","password":"short"}`)
	if got.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d %s", got.Code, got.Body.String())
	}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(got.Body.Bytes(), &body)
	if body.Error == "" || body.Message == "" {
		t.Fatalf("body %+v", body)
	}
}

func TestRegisterInvalidEmail400(t *testing.T) {
	r, _ := testAuthRouter(t, []byte("emp"))
	got := doJSON(t, r, http.MethodPost, "/v1/auth/register", "",
		`{"email":"not-an-email","password":"password1"}`)
	if got.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d %s", got.Code, got.Body.String())
	}
}

func TestLoginBadCredentials401(t *testing.T) {
	r, _ := testAuthRouter(t, []byte("emp"))
	_ = doJSON(t, r, http.MethodPost, "/v1/auth/register", "",
		`{"email":"bob@corp.local","password":"password1"}`)
	unknown := doJSON(t, r, http.MethodPost, "/v1/auth/login", "",
		`{"email":"nobody@corp.local","password":"password1"}`)
	if unknown.Code != http.StatusUnauthorized {
		t.Fatalf("unknown want 401 got %d", unknown.Code)
	}
	wrong := doJSON(t, r, http.MethodPost, "/v1/auth/login", "",
		`{"email":"bob@corp.local","password":"password9"}`)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong want 401 got %d", wrong.Code)
	}
	// 响应不得区分「邮箱不存在」与「密码错误」。
	if !strings.Contains(unknown.Body.String(), "invalid_credentials") ||
		!strings.Contains(wrong.Body.String(), "invalid_credentials") {
		t.Fatalf("leak? unknown=%s wrong=%s", unknown.Body.String(), wrong.Body.String())
	}
}

func TestLoginPasswordTooShort400(t *testing.T) {
	r, _ := testAuthRouter(t, []byte("emp"))
	got := doJSON(t, r, http.MethodPost, "/v1/auth/login", "",
		`{"email":"x@corp.local","password":"short"}`)
	if got.Code != http.StatusBadRequest {
		t.Fatalf("want 400 got %d %s", got.Code, got.Body.String())
	}
}

func TestAuthEmptySecret503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth.RegisterRoutes(r, auth.NewMemoryStore(), nil)

	reg := doJSON(t, r, http.MethodPost, "/v1/auth/register", "",
		`{"email":"a@corp.local","password":"password1"}`)
	if reg.Code != http.StatusServiceUnavailable {
		t.Fatalf("register want 503 got %d %s", reg.Code, reg.Body.String())
	}
	login := doJSON(t, r, http.MethodPost, "/v1/auth/login", "",
		`{"email":"a@corp.local","password":"password1"}`)
	if login.Code != http.StatusServiceUnavailable {
		t.Fatalf("login want 503 got %d %s", login.Code, login.Body.String())
	}
	for _, body := range []string{reg.Body.String(), login.Body.String()} {
		if !strings.Contains(body, "EMPLOYEE_JWT_SECRET") {
			t.Fatalf("503 body should mention missing secret: %s", body)
		}
	}
}
