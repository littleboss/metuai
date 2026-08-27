// Package ready 实现进程就绪检查（P0 /readyz）。
// /healthz 只表示进程存活；/readyz 要求员工 JWT 密钥已配置且数据库可达。
package ready

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ErrNotReady = "not_ready"
	MsgNotReady = "gateway is not ready to issue meeting credentials"
)

// Status 是一次就绪探测的结果。
type Status struct {
	Ready   bool     `json:"ready"`
	Error   string   `json:"error,omitempty"`
	Message string   `json:"message,omitempty"`
	Missing []string `json:"missing,omitempty"`
}

// Checker 检查 EMPLOYEE_JWT_SECRET 是否显式配置，以及 DATABASE_URL 是否可达。
type Checker struct {
	// EmployeeSecretSet 为 true 表示环境变量 EMPLOYEE_JWT_SECRET 非空。
	EmployeeSecretSet bool
	DatabaseURL       string
	// Ping 可选；为空时若 DatabaseURL 非空则用 pgx 直连探测。
	Ping func(ctx context.Context) error
}

// FromEnv 从环境变量构造 Checker（密钥必须显式设置，不用代码默认值冒充已配置）。
func FromEnv() *Checker {
	return &Checker{
		EmployeeSecretSet: strings.TrimSpace(os.Getenv("EMPLOYEE_JWT_SECRET")) != "",
		DatabaseURL:       strings.TrimSpace(os.Getenv("DATABASE_URL")),
	}
}

// Check 返回就绪状态与缺失项列表。
func (c *Checker) Check(ctx context.Context) Status {
	if c == nil {
		return Status{
			Ready:   false,
			Error:   ErrNotReady,
			Message: MsgNotReady,
			Missing: []string{"EMPLOYEE_JWT_SECRET", "DATABASE_URL"},
		}
	}
	missing := make([]string, 0, 2)
	if !c.EmployeeSecretSet {
		missing = append(missing, "EMPLOYEE_JWT_SECRET")
	}
	dbURL := strings.TrimSpace(c.DatabaseURL)
	if dbURL == "" {
		missing = append(missing, "DATABASE_URL")
	} else if err := c.pingDB(ctx, dbURL); err != nil {
		missing = append(missing, "DATABASE_URL")
	}
	if len(missing) > 0 {
		return Status{
			Ready:   false,
			Error:   ErrNotReady,
			Message: MsgNotReady,
			Missing: missing,
		}
	}
	return Status{Ready: true}
}

func (c *Checker) pingDB(ctx context.Context, dsn string) error {
	if c.Ping != nil {
		return c.Ping(ctx)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	pool, err := pgxpool.New(pingCtx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	return pool.Ping(pingCtx)
}

// HandleReadyz 注册 GET /readyz：仅当密钥已设且数据库可达时返回 200。
func HandleReadyz(checker *Checker) gin.HandlerFunc {
	return func(c *gin.Context) {
		st := checker.Check(c.Request.Context())
		if !st.Ready {
			c.JSON(http.StatusServiceUnavailable, st)
			return
		}
		c.JSON(http.StatusOK, gin.H{"ready": true})
	}
}

// Gate 在未就绪时对建会 / 嘉宾会话 / LiveKit 令牌失败关闭，不签发凭证。
func Gate(checker *Checker) gin.HandlerFunc {
	return func(c *gin.Context) {
		st := checker.Check(c.Request.Context())
		if st.Ready {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, st)
	}
}

// AlwaysReady 供单测使用：不拦签发路径。
func AlwaysReady() *Checker {
	return &Checker{
		EmployeeSecretSet: true,
		DatabaseURL:       "memory://test",
		Ping:              func(context.Context) error { return nil },
	}
}
