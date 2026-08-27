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
	auth.RegisterRoutes(r, users, secret, ready.AlwaysReady())
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
		`{"email":"alice@corp.local","password":"password1","display_name":"Alice"}`)
	if reg.Code != http.StatusCreated {
		t.Fatalf("register %d %s", reg.Code, reg.Body.String())
	}
	var regBody struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID          string `json:"id"`
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
		} `json:"user"`
	}
	_ = json.Unmarshal(reg.Body.Bytes(), &regBody)
	if regBody.AccessToken == "" {
		t.Fatal("missing access_token")
	}
	if regBody.User.ID == "" || regBody.User.Email != "alice@corp.local" || regBody.User.DisplayName != "Alice" {
		t.Fatalf("register user payload %+v", regBody.User)
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
		User        struct {
			ID          string `json:"id"`
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
		} `json:"user"`
	}
	_ = json.Unmarshal(login.Body.Bytes(), &loginBody)
	if loginBody.AccessToken == "" {
		t.Fatal("login missing token")
	}
	if loginBody.User.DisplayName != "Alice" || loginBody.User.Email != "alice@corp.local" || loginBody.User.ID == "" {
		t.Fatalf("login user payload %+v", loginBody.User)
	}
	if strings.Contains(login.Body.String(), `"token"`) && !strings.Contains(login.Body.String(), `"access_token"`) {
		t.Fatal("must use access_token field, not token")
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
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(second.Body.Bytes(), &body)
	if body.Error != "email_taken" {
		t.Fatalf("want email_taken got %q", body.Error)
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
	if body.Error != "password_too_short" || body.Message == "" {
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
	// AlwaysReady 只绕过 DB；空密钥仍由 handler 失败关闭。
	auth.RegisterRoutes(r, auth.NewMemoryStore(), nil, ready.AlwaysReady())

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

// AC9: DATABASE_URL 未设置时 readyz 与 register/login 均为 503（禁止内存用户库冒充就绪）。
func TestAC9_UnsetDatabaseURLAuthAndReadyz503(t *testing.T) {
	t.Setenv("EMPLOYEE_JWT_SECRET", "emp-secret")
	t.Setenv("GUEST_JWT_SECRET", "gst-secret")
	t.Setenv("DATABASE_URL", "")

	checker := ready.FromEnv()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/readyz", ready.HandleReadyz(checker))
	auth.RegisterRoutes(r, auth.NewMemoryStore(), []byte("emp-secret"), checker)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz want 503 got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "DATABASE_URL") {
		t.Fatalf("readyz missing DATABASE_URL: %s", w.Body.String())
	}

	reg := doJSON(t, r, http.MethodPost, "/v1/auth/register", "",
		`{"email":"alice@corp.local","password":"password1","display_name":"Alice"}`)
	if reg.Code != http.StatusServiceUnavailable {
		t.Fatalf("register want 503 got %d %s (memory fallback must not succeed)", reg.Code, reg.Body.String())
	}
	if !strings.Contains(reg.Body.String(), "DATABASE_URL") {
		t.Fatalf("register 503 should list DATABASE_URL: %s", reg.Body.String())
	}
	login := doJSON(t, r, http.MethodPost, "/v1/auth/login", "",
		`{"email":"alice@corp.local","password":"password1"}`)
	if login.Code != http.StatusServiceUnavailable {
		t.Fatalf("login want 503 got %d %s", login.Code, login.Body.String())
	}
	if !strings.Contains(login.Body.String(), "DATABASE_URL") {
		t.Fatalf("login 503 should list DATABASE_URL: %s", login.Body.String())
	}
}

// AC9: 坏 DSN 时进程仍可响应；readyz/auth 503（missing 含 DATABASE_URL）。
func TestAC9_BadDSNReadyzAndAuth503(t *testing.T) {
	badDSN := "postgres://metuai:metuai@127.0.0.1:1/metuai?sslmode=disable"
	t.Setenv("EMPLOYEE_JWT_SECRET", "emp-secret")
	t.Setenv("GUEST_JWT_SECRET", "gst-secret")
	t.Setenv("DATABASE_URL", badDSN)

	checker := ready.FromEnv()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	r.GET("/readyz", ready.HandleReadyz(checker))
	auth.RegisterRoutes(r, auth.NewMemoryStore(), []byte("emp-secret"), checker)

	hw := httptest.NewRecorder()
	r.ServeHTTP(hw, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if hw.Code != http.StatusOK {
		t.Fatalf("healthz want 200 got %d", hw.Code)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz want 503 got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "DATABASE_URL") {
		t.Fatalf("readyz missing DATABASE_URL: %s", w.Body.String())
	}

	reg := doJSON(t, r, http.MethodPost, "/v1/auth/register", "",
		`{"email":"alice@corp.local","password":"password1"}`)
	if reg.Code != http.StatusServiceUnavailable {
		t.Fatalf("register want 503 got %d %s", reg.Code, reg.Body.String())
	}
	login := doJSON(t, r, http.MethodPost, "/v1/auth/login", "",
		`{"email":"alice@corp.local","password":"password1"}`)
	if login.Code != http.StatusServiceUnavailable {
		t.Fatalf("login want 503 got %d %s", login.Code, login.Body.String())
	}
}
