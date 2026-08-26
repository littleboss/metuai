package identity

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const CtxPrincipal = "principal"

func EmployeeAuth(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, err := ParseEmployeeToken(bearer(c), secret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Set(CtxPrincipal, principal)
		c.Next()
	}
}

func AnyMeetingAuth(employeeSecret, guestSecret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		rawToken := bearer(c)
		if principal, err := ParseEmployeeToken(rawToken, employeeSecret); err == nil {
			c.Set(CtxPrincipal, principal)
			c.Next()
			return
		}
		if principal, err := ParseGuestSession(rawToken, guestSecret); err == nil {
			c.Set(CtxPrincipal, principal)
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
	}
}

func WorkerOrEmployeeAuth(secret []byte, workerToken string) gin.HandlerFunc {
	employee := EmployeeAuth(secret)
	workerToken = strings.TrimSpace(workerToken)
	return func(c *gin.Context) {
		rawToken := bearer(c)
		if workerToken != "" && rawToken == workerToken {
			c.Set(CtxPrincipal, Principal{
				Kind:        KindEmployee,
				UserID:      "system:worker",
				DisplayName: "worker",
			})
			c.Next()
			return
		}
		employee(c)
	}
}

func MustPrincipal(c *gin.Context) Principal {
	return c.MustGet(CtxPrincipal).(Principal)
}

func bearer(c *gin.Context) string {
	header := c.GetHeader("Authorization")
	return strings.TrimPrefix(header, "Bearer ")
}
