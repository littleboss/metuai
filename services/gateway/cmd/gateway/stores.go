package main

import (
	"context"
	"log"
	"strings"
	"time"

	"metuai/services/gateway/internal/auth"
	"metuai/services/gateway/internal/meeting"
)

// openStores 打开会议库与用户库。
// DATABASE_URL 为空或拨号/迁移失败时不 Fatal：回退内存仓库，由 /readyz 与 auth Gate 失败关闭。
// 返回的 ping 仅在 Postgres 池成功建立时非 nil（供 /readyz 复用）。
func openStores(ctx context.Context, databaseURL string) (meeting.Repository, auth.UserStore, func(context.Context) error) {
	dsn := strings.TrimSpace(databaseURL)
	if dsn == "" {
		log.Printf("DATABASE_URL unset: using in-memory stores; /readyz and auth stay fail-closed")
		return meeting.NewMemoryStore(), auth.NewMemoryStore(), nil
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}

	pgStore, err := meeting.NewPGStore(ctx, dsn)
	if err != nil {
		log.Printf("database unavailable at startup: %v; listening with in-memory fallback (/readyz will report DATABASE_URL)", err)
		return meeting.NewMemoryStore(), auth.NewMemoryStore(), nil
	}

	userStore, err := auth.NewPGStoreFromPool(ctx, pgStore.Pool())
	if err != nil {
		log.Printf("auth schema unavailable at startup: %v; auth stays fail-closed via /readyz", err)
		// 会议池仍可用时复用 Ping；用户库用内存占位（Gate 未就绪前不会写入成功路径）。
		return pgStore, auth.NewMemoryStore(), pgStore.Ping
	}

	log.Printf("auth user store: postgres")
	return pgStore, userStore, pgStore.Ping
}
