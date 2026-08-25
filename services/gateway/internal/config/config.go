package config

import "os"

type Config struct {
	HTTPAddr            string
	DatabaseURL         string
	EmployeeJWTSecret   []byte
	GuestJWTSecret      []byte
	LiveKitURL          string
	LiveKitAPIKey       string
	LiveKitAPISecret    string
	DevAllowEmployeeWeb bool
}

func FromEnv() Config {
	return Config{
		HTTPAddr:            getenv("HTTP_ADDR", ":8080"),
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		EmployeeJWTSecret:   []byte(getenv("EMPLOYEE_JWT_SECRET", "dev-employee-secret")),
		GuestJWTSecret:      []byte(getenv("GUEST_JWT_SECRET", "dev-guest-secret")),
		LiveKitURL:          getenv("LIVEKIT_URL", "ws://127.0.0.1:7880"),
		LiveKitAPIKey:       getenv("LIVEKIT_API_KEY", "devkey"),
		LiveKitAPISecret:    getenv("LIVEKIT_API_SECRET", "secret"),
		DevAllowEmployeeWeb: getenv("DEV_ALLOW_EMPLOYEE_WEB", "true") == "true",
	}
}

func getenv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
