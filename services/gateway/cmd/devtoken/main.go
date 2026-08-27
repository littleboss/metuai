// Command devtoken 生成一个仅供本地开发使用的员工 JWT。
// 必须显式设置 EMPLOYEE_JWT_SECRET（与网关一致）；禁止内置默认密钥。
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	secret := strings.TrimSpace(os.Getenv("EMPLOYEE_JWT_SECRET"))
	if secret == "" {
		fmt.Fprintln(os.Stderr, "EMPLOYEE_JWT_SECRET is required (no baked default). Example: source infra/compose/.env.example")
		os.Exit(1)
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
