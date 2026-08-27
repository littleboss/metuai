package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/auth"
	"metuai/services/gateway/internal/ready"
)

// AC9: 坏 DSN 时 openStores 不得 Fatal；进程可挂路由，healthz=200，readyz/auth=503。
func TestOpenStoresBadDSNDoesNotFatalAndAuthFailClosed(t *testing.T) {
	badDSN := "postgres://metuai:metuai@127.0.0.1:1/metuai?sslmode=disable"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	var repo interface{}
	var users auth.UserStore
	var ping func(context.Context) error
	go func() {
		defer close(done)
		repo, users, ping = openStores(ctx, badDSN)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("openStores hung or blocked forever on bad DSN")
	}
	if repo == nil || users == nil {
		t.Fatal("openStores must return fallback stores, not nil")
	}
	if ping != nil {
		t.Fatal("failed startup should not expose a live pool ping")
	}

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
	auth.RegisterRoutes(r, users, []byte("emp-secret"), checker)

	hw := httptest.NewRecorder()
	r.ServeHTTP(hw, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if hw.Code != http.StatusOK {
		t.Fatalf("healthz want 200 got %d", hw.Code)
	}

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rw.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz want 503 got %d %s", rw.Code, rw.Body.String())
	}
	var st ready.Status
	_ = json.Unmarshal(rw.Body.Bytes(), &st)
	if !strings.Contains(strings.Join(st.Missing, ","), "DATABASE_URL") {
		t.Fatalf("missing=%v body=%s", st.Missing, rw.Body.String())
	}

	for _, path := range []string{"/v1/auth/register", "/v1/auth/login"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(
			`{"email":"a@corp.local","password":"password1"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s want 503 got %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestOpenStoresEmptyDatabaseURLFailClosed(t *testing.T) {
	_, users, ping := openStores(context.Background(), "")
	if users == nil {
		t.Fatal("users store nil")
	}
	if ping != nil {
		t.Fatal("empty DATABASE_URL should not set ping")
	}

	t.Setenv("EMPLOYEE_JWT_SECRET", "emp-secret")
	t.Setenv("GUEST_JWT_SECRET", "gst-secret")
	t.Setenv("DATABASE_URL", "")
	checker := ready.FromEnv()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/readyz", ready.HandleReadyz(checker))
	auth.RegisterRoutes(r, users, []byte("emp-secret"), checker)

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rw.Code != http.StatusServiceUnavailable || !strings.Contains(rw.Body.String(), "DATABASE_URL") {
		t.Fatalf("readyz %d %s", rw.Code, rw.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register", strings.NewReader(
		`{"email":"a@corp.local","password":"password1"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("register want 503 got %d %s", w.Code, w.Body.String())
	}
}
