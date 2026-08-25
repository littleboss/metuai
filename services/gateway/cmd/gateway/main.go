package main

import (
	"context"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/config"
	"metuai/services/gateway/internal/meeting"
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

	r := gin.Default()
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
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
	)
	if err := r.Run(cfg.HTTPAddr); err != nil {
		log.Fatal(err)
	}
}
