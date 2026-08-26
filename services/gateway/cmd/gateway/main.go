package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/config"
	"metuai/services/gateway/internal/egress"
	"metuai/services/gateway/internal/identity"
	"metuai/services/gateway/internal/knowledge"
	"metuai/services/gateway/internal/meeting"
	"metuai/services/gateway/internal/upload"
)

func main() {
	cfg := config.FromEnv()

	var repo meeting.Repository
	if cfg.DatabaseURL != "" {
		pgStore, err := meeting.NewPGStore(context.Background(), cfg.DatabaseURL)
		if err != nil {
			log.Fatal(err)
		}
		repo = pgStore
	} else {
		repo = meeting.NewMemoryStore()
	}

	uploadRoot := os.Getenv("UPLOAD_SPOOL_DIR")
	if uploadRoot == "" {
		uploadRoot = filepath.Join(os.TempDir(), "metuai-uploads")
	}
	uploadStore, err := upload.NewStore(uploadRoot)
	if err != nil {
		log.Fatal(err)
	}

	// Egress 未启用时 orchestrator 为 nil：媒体一路保持 pending，不会假装录成了。
	egCfg := cfg.EgressConfig()
	var orchestrator meeting.EgressOrchestrator
	if cfg.EgressEnabled && egCfg.Ready() {
		orchestrator = egress.NewManager(
			egCfg,
			egress.NewLiveKitClient(egCfg),
			time.Duration(cfg.EgressFinalizeSeconds)*time.Second,
		)
		log.Printf("livekit egress enabled: bucket=%s endpoint=%s", egCfg.S3Bucket, egCfg.S3Endpoint)
		if egCfg.UsesLoopbackS3() {
			log.Printf("WARNING: S3_ENDPOINT=%s is loopback; livekit-egress runs in Docker and will not reach host MinIO. Use http://minio:9000", egCfg.S3Endpoint)
		}
	} else {
		log.Printf("livekit egress disabled (EGRESS_ENABLED=%v config_ready=%v)", cfg.EgressEnabled, egCfg.Ready())
	}
	egressRT := meeting.NewEgressRuntime(orchestrator, cfg.S3Bucket)

	uploadEndpoint := upload.ResolveUploadEndpoint(cfg.S3Endpoint, cfg.S3UploadEndpoint)
	blobs, err := upload.NewS3BlobStore(upload.S3Config{
		Endpoint:       uploadEndpoint,
		Region:         cfg.S3Region,
		Bucket:         cfg.S3Bucket,
		AccessKey:      cfg.S3AccessKey,
		SecretKey:      cfg.S3SecretKey,
		ForcePathStyle: cfg.S3ForcePathStyle,
	})
	if err != nil {
		log.Fatal(err)
	}
	if blobs != nil && blobs.Enabled() {
		log.Printf("local-recording S3 upload enabled: bucket=%s endpoint=%s", cfg.S3Bucket, uploadEndpoint)
	} else {
		log.Printf("local-recording S3 upload disabled (incomplete S3 config); spool stays local only")
	}

	stop := make(chan struct{})
	meeting.StartIdleReaper(repo, time.Duration(cfg.IdleEndMinutes)*time.Minute, 30*time.Second, egressRT, stop)

	knowledgeIdx := knowledge.NewFromEnv()
	breakGlass := meeting.BreakGlassForRepo(repo)
	guestMailSender, err := meeting.NewSMTPVerificationSender(meeting.SMTPConfig{
		Host:       cfg.SMTPHost,
		Port:       cfg.SMTPPort,
		Username:   cfg.SMTPUsername,
		Password:   cfg.SMTPPassword,
		From:       cfg.SMTPFrom,
		RequireTLS: cfg.SMTPRequireTLS,
	})
	if err != nil {
		log.Fatal(err)
	}
	guestVerifier := meeting.NewGuestEmailVerifier(repo, guestMailSender)
	log.Printf("knowledge backend: %s", knowledge.BackendName(knowledgeIdx))
	if guestMailSender == nil {
		log.Printf("guest email verification disabled (configure SMTP_HOST, SMTP_PORT, SMTP_FROM)")
	}
	if _, ok := breakGlass.(*meeting.PGBreakGlass); ok {
		log.Printf("break-glass backend: postgres")
	} else {
		log.Printf("break-glass backend: memory")
	}

	r := gin.Default()
	r.GET("/healthz", func(c *gin.Context) {
		bgBackend := "memory"
		if _, ok := breakGlass.(*meeting.PGBreakGlass); ok {
			bgBackend = "postgres"
		}
		c.JSON(http.StatusOK, gin.H{
			"ok":                 true,
			"egress_enabled":     cfg.EgressEnabled,
			"egress_ready":       egCfg.Ready() && cfg.EgressEnabled,
			"s3_endpoint":        egCfg.S3Endpoint,
			"s3_upload_endpoint": uploadEndpoint,
			"s3_upload_ready":    blobs != nil && blobs.Enabled(),
			"s3_loopback_warn":   cfg.EgressEnabled && egCfg.UsesLoopbackS3(),
			"knowledge_backend":  knowledge.BackendName(knowledgeIdx),
			"break_glass":        bgBackend,
		})
	})
	meeting.RegisterRoutes(
		r,
		repo,
		cfg.EmployeeJWTSecret,
		cfg.GuestJWTSecret,
		cfg.LiveKitURL,
		cfg.LiveKitAPIKey,
		cfg.LiveKitAPISecret,
		cfg.DevAllowEmployeeWeb,
		cfg.S3Bucket,
		egressRT,
		knowledgeIdx,
		breakGlass,
		guestVerifier,
	)
	// 企业下发桌面端 spool 密钥（PoC：读环境变量；生产应走设备注册 + 轮换）。
	r.GET("/v1/device/spool-key", identity.EmployeeAuth(cfg.EmployeeJWTSecret), func(c *gin.Context) {
		key := strings.TrimSpace(os.Getenv("METUAI_SPOOL_KEY"))
		if key == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "spool_key_not_configured"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"spool_key_hex": key,
			"algorithm":     "aes-256-gcm",
			"note":          "PoC: rotate via METUAI_SPOOL_KEY; not a full device enrollment flow",
		})
	})
	knowledge.RegisterRoutes(
		r,
		knowledgeIdx,
		cfg.EmployeeJWTSecret,
		cfg.GuestJWTSecret,
		breakGlass.ElevatedMeetingIDs,
		func(meetingIDs []string, actorKey, query string, hitCount int) {
			for _, mid := range meetingIDs {
				_ = repo.AppendAudit(meeting.AuditEvent{
					MeetingID: mid,
					ActorKey:  actorKey,
					Action:    "knowledge_search",
					Detail:    query,
				})
			}
			_ = hitCount
		},
	)
	upload.RegisterRoutes(r, uploadStore, repo, cfg.EmployeeJWTSecret, blobs, cfg.S3Bucket)
	if err := r.Run(cfg.HTTPAddr); err != nil {
		close(stop)
		log.Fatal(err)
	}
}
