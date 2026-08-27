package config

import (
	"os"
	"strconv"
	"strings"

	"metuai/services/gateway/internal/egress"
)

type Config struct {
	HTTPAddr            string
	DatabaseURL         string
	EmployeeJWTSecret   []byte
	GuestJWTSecret      []byte
	LiveKitURL          string
	LiveKitAPIKey       string
	LiveKitAPISecret    string
	DevAllowEmployeeWeb bool
	IdleEndMinutes      int
	S3Bucket            string
	S3Endpoint          string
	// S3UploadEndpoint 是网关 PutObject 用的地址（宿主机可达）。
	// 空则回退 S3Endpoint；EGRESS 开着且 S3_ENDPOINT=http://minio:9000 时应显式设 127.0.0.1:19000。
	S3UploadEndpoint string
	S3Region         string
	S3AccessKey      string
	S3SecretKey      string
	S3ForcePathStyle bool
	EgressEnabled    bool
	// EgressFinalizeSeconds 是结束会议时等待 Egress 汇报终态的预算。
	// 预算内拿不到终态就保持 started，不会猜成 ready。
	EgressFinalizeSeconds int
	SMTPHost              string
	SMTPPort              string
	SMTPUsername          string
	SMTPPassword          string
	SMTPFrom              string
	SMTPRequireTLS        bool
	AppBaseURL            string
}

func FromEnv() Config {
	return Config{
		HTTPAddr:    getenv("HTTP_ADDR", ":18080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		// JWT 密钥禁止代码内默认值：空/未设置必须保持空，由 /readyz 与签发门闸失败关闭。
		EmployeeJWTSecret:   []byte(strings.TrimSpace(os.Getenv("EMPLOYEE_JWT_SECRET"))),
		GuestJWTSecret:      []byte(strings.TrimSpace(os.Getenv("GUEST_JWT_SECRET"))),
		LiveKitURL:          getenv("LIVEKIT_URL", "ws://127.0.0.1:17880"),
		LiveKitAPIKey:       getenv("LIVEKIT_API_KEY", "devkey"),
		LiveKitAPISecret:    getenv("LIVEKIT_API_SECRET", "secret"),
		DevAllowEmployeeWeb: getenv("DEV_ALLOW_EMPLOYEE_WEB", "true") == "true",
		IdleEndMinutes:      getenvInt("IDLE_END_MINUTES", 10),
		S3Bucket:            getenv("S3_BUCKET", "metuai-media"),
		S3Endpoint:          getenv("S3_ENDPOINT", "http://127.0.0.1:19000"),
		S3UploadEndpoint:    os.Getenv("S3_UPLOAD_ENDPOINT"),
		S3Region:            getenv("S3_REGION", "us-east-1"),
		S3AccessKey:         getenv("S3_ACCESS_KEY", "metuai"),
		S3SecretKey:         getenv("S3_SECRET_KEY", "metuai-secret"),
		S3ForcePathStyle:    getenv("S3_FORCE_PATH_STYLE", "true") == "true",
		EgressEnabled:       getenv("EGRESS_ENABLED", "false") == "true",

		EgressFinalizeSeconds: getenvInt("EGRESS_FINALIZE_SECONDS", 8),
		SMTPHost:              os.Getenv("SMTP_HOST"),
		SMTPPort:              getenv("SMTP_PORT", "587"),
		SMTPUsername:          os.Getenv("SMTP_USERNAME"),
		SMTPPassword:          os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:              os.Getenv("SMTP_FROM"),
		SMTPRequireTLS:        getenv("SMTP_REQUIRE_TLS", "true") == "true",
		AppBaseURL:            getenv("APP_BASE_URL", "http://127.0.0.1:5173"),
	}
}

func (c Config) EgressConfig() egress.Config {
	return egress.Config{
		LiveKitURL:       c.LiveKitURL,
		LiveKitAPIKey:    c.LiveKitAPIKey,
		LiveKitAPISecret: c.LiveKitAPISecret,
		S3Endpoint:       c.S3Endpoint,
		S3Region:         c.S3Region,
		S3Bucket:         c.S3Bucket,
		S3AccessKey:      c.S3AccessKey,
		S3SecretKey:      c.S3SecretKey,
		S3ForcePathStyle: c.S3ForcePathStyle,
	}
}

func getenv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getenvInt(key string, defaultValue int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return defaultValue
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultValue
	}
	return n
}
