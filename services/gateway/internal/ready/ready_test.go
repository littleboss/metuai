package ready_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/ready"
)

func TestCheckerMissingSecret(t *testing.T) {
	c := &ready.Checker{
		EmployeeSecretSet: false,
		GuestSecretSet:    true,
		DatabaseURL:       "postgres://x",
		Ping:              func(context.Context) error { return nil },
	}
	st := c.Check(context.Background())
	if st.Ready || len(st.Missing) != 1 || st.Missing[0] != "EMPLOYEE_JWT_SECRET" {
		t.Fatalf("%+v", st)
	}
}

func TestCheckerMissingGuestSecret(t *testing.T) {
	c := &ready.Checker{
		EmployeeSecretSet: true,
		GuestSecretSet:    false,
		DatabaseURL:       "postgres://x",
		Ping:              func(context.Context) error { return nil },
	}
	st := c.Check(context.Background())
	if st.Ready || !strings.Contains(strings.Join(st.Missing, ","), "GUEST_JWT_SECRET") {
		t.Fatalf("%+v", st)
	}
}

func TestCheckerMissingDatabase(t *testing.T) {
	c := &ready.Checker{EmployeeSecretSet: true, GuestSecretSet: true, DatabaseURL: ""}
	st := c.Check(context.Background())
	if st.Ready || len(st.Missing) != 1 || st.Missing[0] != "DATABASE_URL" {
		t.Fatalf("%+v", st)
	}
}

func TestFromEnvReflectsUnsetSecrets(t *testing.T) {
	t.Setenv("EMPLOYEE_JWT_SECRET", "")
	t.Setenv("GUEST_JWT_SECRET", "")
	t.Setenv("DATABASE_URL", "")
	st := ready.FromEnv().Check(context.Background())
	if st.Ready {
		t.Fatal("expected not ready")
	}
	joined := strings.Join(st.Missing, ",")
	if !strings.Contains(joined, "EMPLOYEE_JWT_SECRET") || !strings.Contains(joined, "GUEST_JWT_SECRET") {
		t.Fatalf("missing=%v", st.Missing)
	}
}

func TestHandleReadyzJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/readyz", ready.HandleReadyz(&ready.Checker{}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["error"] != "not_ready" {
		t.Fatalf("%v", body)
	}
}
