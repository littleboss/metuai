package identity

import (
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

func ParseEmployeeToken(tokenString string, hmacSecret []byte) (Principal, error) {
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
