// Command devtoken 生成一个仅供本地开发使用的员工 JWT。
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	secret := os.Getenv("EMPLOYEE_JWT_SECRET")
	if secret == "" {
		secret = "dev-employee-secret"
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":          "u-dev",
		"kind":         "employee",
		"email":        "dev@corp.local",
		"display_name": "Dev User",
		"exp":          time.Now().Add(24 * time.Hour).Unix(),
	})

	signedToken, err := token.SignedString([]byte(secret))
	if err != nil {
		panic(err)
	}

	fmt.Println(signedToken)
}
