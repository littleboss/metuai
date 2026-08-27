package auth

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/identity"
)

const minPasswordLen = 8

type registerBody struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type loginBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// RegisterRoutes 挂载 POST /v1/auth/register 与 /v1/auth/login。
// employeeSecret 为空时两个接口均 503（与 /readyz fail-closed 一致）。
func RegisterRoutes(r *gin.Engine, users UserStore, employeeSecret []byte) {
	r.POST("/v1/auth/register", func(c *gin.Context) {
		if len(employeeSecret) == 0 {
			writeSecretUnavailable(c)
			return
		}
		var body registerBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": "invalid request body"})
			return
		}
		email := NormalizeEmail(body.Email)
		if !validEmail(email) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_email", "message": "invalid email"})
			return
		}
		if utf8.RuneCountInString(body.Password) < minPasswordLen {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "password_too_short",
				"message": "password must be at least 8 characters",
			})
			return
		}
		hash, err := HashPassword(body.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "hash_failed", "message": "could not hash password"})
			return
		}
		user, err := users.CreateUser(email, hash, body.DisplayName)
		if errors.Is(err, ErrDuplicateEmail) {
			c.JSON(http.StatusConflict, gin.H{"error": "email_taken", "message": "email already registered"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "register_failed", "message": "registration failed"})
			return
		}
		token, err := issueToken(user, employeeSecret)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "not_ready", "message": "employee jwt secret not configured"})
			return
		}
		c.JSON(http.StatusCreated, authSuccess(token, user))
	})

	r.POST("/v1/auth/login", func(c *gin.Context) {
		if len(employeeSecret) == 0 {
			writeSecretUnavailable(c)
			return
		}
		var body loginBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": "invalid request body"})
			return
		}
		if utf8.RuneCountInString(body.Password) < minPasswordLen {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "password_too_short",
				"message": "password must be at least 8 characters",
			})
			return
		}
		email := NormalizeEmail(body.Email)
		user, err := users.FindByEmail(email)
		if err != nil || !CheckPassword(user.PasswordHash, body.Password) {
			// 不泄露邮箱是否存在。
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_credentials", "message": "invalid email or password"})
			return
		}
		token, err := issueToken(user, employeeSecret)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "not_ready", "message": "employee jwt secret not configured"})
			return
		}
		c.JSON(http.StatusOK, authSuccess(token, user))
	})
}

func authSuccess(token string, user User) gin.H {
	return gin.H{
		"access_token": token,
		"user": gin.H{
			"id":           user.ID,
			"email":        user.Email,
			"display_name": user.DisplayName,
		},
	}
}

func writeSecretUnavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error":   "not_ready",
		"message": "EMPLOYEE_JWT_SECRET is not configured",
		"missing": []string{"EMPLOYEE_JWT_SECRET"},
	})
}

func issueToken(user User, secret []byte) (string, error) {
	return identity.IssueEmployeeToken(identity.Principal{
		UserID:      user.ID,
		Kind:        identity.KindEmployee,
		Email:       user.Email,
		DisplayName: user.DisplayName,
	}, secret, 24*time.Hour)
}

func validEmail(email string) bool {
	if email == "" || !strings.Contains(email, "@") {
		return false
	}
	_, err := mail.ParseAddress(email)
	return err == nil
}
