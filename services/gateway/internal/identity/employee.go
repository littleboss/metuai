package identity

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// IssueEmployeeToken 签发与 ParseEmployeeToken 兼容的员工 JWT。
// hmacSecret 为空时返回错误（调用方应先做 503 fail-closed）。
func IssueEmployeeToken(p Principal, hmacSecret []byte, ttl time.Duration) (string, error) {
	if len(hmacSecret) == 0 {
		return "", fmt.Errorf("employee jwt secret not configured")
	}
	if p.UserID == "" {
		return "", fmt.Errorf("missing sub")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	claims := jwt.MapClaims{
		"sub":          p.UserID,
		"kind":         KindEmployee,
		"email":        p.Email,
		"display_name": p.DisplayName,
		"exp":          time.Now().Add(ttl).Unix(),
	}
	if len(p.Roles) > 0 {
		roles := make([]any, 0, len(p.Roles))
		for _, r := range p.Roles {
			roles = append(roles, r)
		}
		claims["roles"] = roles
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(hmacSecret)
}

func ParseEmployeeToken(tokenString string, hmacSecret []byte) (Principal, error) {
	if len(hmacSecret) == 0 {
		return Principal{}, fmt.Errorf("employee jwt secret not configured")
	}
	parsed, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return hmacSecret, nil
	})
	if err != nil || !parsed.Valid {
		return Principal{}, fmt.Errorf("invalid employee token")
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return Principal{}, fmt.Errorf("invalid claims")
	}
	kind, _ := claims["kind"].(string)
	if kind != KindEmployee {
		return Principal{}, fmt.Errorf("not an employee token")
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return Principal{}, fmt.Errorf("missing sub")
	}
	p := Principal{
		UserID:      sub,
		Kind:        KindEmployee,
		Email:       stringClaim(claims, "email"),
		DisplayName: stringClaim(claims, "display_name"),
	}
	if raw, ok := claims["roles"].([]any); ok {
		for _, r := range raw {
			if s, ok := r.(string); ok {
				p.Roles = append(p.Roles, s)
			}
		}
	}
	return p, nil
}

func stringClaim(c jwt.MapClaims, key string) string {
	s, _ := c[key].(string)
	return s
}
