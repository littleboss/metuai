package identity

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func IssueGuestSession(p Principal, hmacSecret []byte, ttl time.Duration) (string, error) {
	if p.GuestID == "" || p.MeetingID == "" {
		return "", fmt.Errorf("guest_id and meeting_id required")
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"kind":         KindGuest,
		"guest_id":     p.GuestID,
		"meeting_id":   p.MeetingID,
		"display_name": p.DisplayName,
		"email":        p.Email,
		"exp":          time.Now().Add(ttl).Unix(),
	})
	return tok.SignedString(hmacSecret)
}

func ParseGuestSession(tokenString string, hmacSecret []byte) (Principal, error) {
	parsed, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return hmacSecret, nil
	})
	if err != nil || !parsed.Valid {
		return Principal{}, fmt.Errorf("invalid guest session")
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return Principal{}, fmt.Errorf("invalid claims")
	}
	if stringClaim(claims, "kind") != KindGuest {
		return Principal{}, fmt.Errorf("not a guest session")
	}
	return Principal{
		Kind:        KindGuest,
		GuestID:     stringClaim(claims, "guest_id"),
		MeetingID:   stringClaim(claims, "meeting_id"),
		DisplayName: stringClaim(claims, "display_name"),
		Email:       stringClaim(claims, "email"),
	}, nil
}
