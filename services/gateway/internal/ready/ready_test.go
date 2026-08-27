package ready_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/ready"
)

func TestCheckerMissingSecret(t *testing.T) {
	c := &ready.Checker{
		EmployeeSecretSet: false,
		DatabaseURL:       "postgres://x",
		Ping:              func(context.Context) error { return nil },
	}
	st := c.Check(context.Background())
	if st.Ready || len(st.Missing) != 1 || st.Missing[0] != "EMPLOYEE_JWT_SECRET" {
		t.Fatalf("%+v", st)
	}
}

func TestCheckerMissingDatabase(t *testing.T) {
	c := &ready.Checker{EmployeeSecretSet: true, DatabaseURL: ""}
	st := c.Check(context.Background())
	if st.Ready || len(st.Missing) != 1 || st.Missing[0] != "DATABASE_URL" {
		t.Fatalf("%+v", st)
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
