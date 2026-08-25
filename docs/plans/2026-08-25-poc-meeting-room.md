# PoC：建会并进入 LiveKit 房间

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 开发者在本机用 Docker 起 LiveKit 与 PostgreSQL，启动 Go 网关和 Vite 前端后，能用测试员工 JWT 创建即时会议，两人进入同一房间看到音视频；嘉宾用链接密码进会，进会前必须确认服务端录音。

**Architecture:** 网关（Go + Gin）校验员工 JWT（PoC 用共享密钥 HMAC，契约与正式 IdP 相同），会议元数据进 PostgreSQL，用 LiveKit Server SDK 签发房间 token。前端一份 Vite SPA：员工带 Bearer 建会/入会，嘉宾走密码换短期会话。本计划不上 Tauri、Egress、Dapr、Vespa、本机录音。

**Tech Stack:** Go 1.22、Gin、golang-jwt、livekit-server-sdk-go、PostgreSQL 16、LiveKit Server（dev 模式）、Vite、React、TypeScript、livekit-client、@livekit/components-react

**Spec:** `docs/2026-08-25-tech-stack.md`、`docs/2026-08-25-private-video-meeting-ai-architecture.md`

## Global Constraints

- 模块路径：`metuai/services/gateway`；前端在 `apps/web`。
- 员工 JWT claim：`sub`（稳定用户 ID）、`kind` 必须为 `employee`、`email`、`display_name`、可选 `roles` 字符串数组。
- 嘉宾不走企业 JWT；密码正确后由网关签发 `guest_session`（HS256，15 分钟，claim 含 `kind=guest`、`meeting_id`、`guest_id`、`display_name`）。
- 进会前必须写 `recording_ack`；未确认不得签发 LiveKit token。
- 本计划默认 `DEV_ALLOW_EMPLOYEE_WEB=true`（尚无 Tauri）。正式行为是员工必须用 Tauri，由后续计划关掉该开关并返回 403。
- 房间密码创建时自动生成 8 位数字+字母，存 SHA-256 哈希（PoC 不加 salt 轮次；后续计划再升级）。
- 不实现等候室、日历、本地录音、知识库。
- 测试命令在仓库根目录或各模块目录执行；本机用 Docker 起依赖。

## 后续计划（本文件不实现）

| 计划 | 产出 |
|---|---|
| 2. 员工壳与会中控制 | Meetily 式侧栏、锁定/踢人、空闲结束 |
| 3. Tauri 与本机麦克风备份 | 员工禁止浏览器开会、分块上传 |
| 4. Egress 与会后落盘 | 独立音轨、混音、房间画面、MinIO |
| 5. Dapr 会后工作流 | 状态机 + Python Worker 桩 |
| 6. ASR 与纪要 | FunASR、结构化纪要、修订 |
| 7. Vespa 知识检索 | ACL 条件查询 |

---

## 文件地图

```text
infra/compose/docker-compose.yml
infra/compose/.env.example
services/gateway/go.mod
services/gateway/cmd/gateway/main.go
services/gateway/internal/config/config.go
services/gateway/internal/identity/principal.go
services/gateway/internal/identity/employee.go
services/gateway/internal/identity/employee_test.go
services/gateway/internal/identity/guest.go
services/gateway/internal/identity/guest_test.go
services/gateway/internal/identity/middleware.go
services/gateway/internal/meeting/model.go
services/gateway/internal/meeting/store.go
services/gateway/internal/meeting/store_test.go
services/gateway/internal/meeting/http.go
services/gateway/internal/meeting/http_test.go
services/gateway/internal/livekit/token.go
services/gateway/internal/livekit/token_test.go
apps/web/package.json
apps/web/vite.config.ts
apps/web/index.html
apps/web/src/main.tsx
apps/web/src/App.tsx
apps/web/src/lib/api.ts
apps/web/src/pages/HomePage.tsx
apps/web/src/pages/JoinGuestPage.tsx
apps/web/src/pages/RoomPage.tsx
apps/web/src/vite-env.d.ts
README.md
```

---

### Task 1: 员工 JWT 解析

**Files:**
- Create: `services/gateway/go.mod`
- Create: `services/gateway/internal/identity/principal.go`
- Create: `services/gateway/internal/identity/employee.go`
- Test: `services/gateway/internal/identity/employee_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `type Principal struct { UserID string; Kind string; Email string; DisplayName string; Roles []string }`；`func ParseEmployeeToken(tokenString string, hmacSecret []byte) (Principal, error)`；常量 `KindEmployee = "employee"`、`KindGuest = "guest"`

- [ ] **Step 1: 初始化 Go 模块并写失败测试**

```bash
mkdir -p services/gateway/internal/identity
cd services/gateway
go mod init metuai/services/gateway
go get github.com/golang-jwt/jwt/v5
```

`services/gateway/internal/identity/principal.go`:

```go
package identity

const (
	KindEmployee = "employee"
	KindGuest    = "guest"
)

type Principal struct {
	UserID      string
	Kind        string
	Email       string
	DisplayName string
	Roles       []string
	MeetingID   string
	GuestID     string
}
```

`services/gateway/internal/identity/employee_test.go`:

```go
package identity

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestParseEmployeeToken_OK(t *testing.T) {
	secret := []byte("test-secret")
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":           "u-1",
		"kind":          KindEmployee,
		"email":         "a@corp.local",
		"display_name":  "Alice",
		"roles":         []any{"organizer"},
		"exp":           time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseEmployeeToken(s, secret)
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != "u-1" || p.Kind != KindEmployee || p.Email != "a@corp.local" || p.DisplayName != "Alice" {
		t.Fatalf("got %+v", p)
	}
}

func TestParseEmployeeToken_RejectsGuestKind(t *testing.T) {
	secret := []byte("test-secret")
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  "g-1",
		"kind": KindGuest,
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	s, _ := tok.SignedString(secret)
	if _, err := ParseEmployeeToken(s, secret); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseEmployeeToken_RejectsWrongSecret(t *testing.T) {
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  "u-1",
		"kind": KindEmployee,
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	s, _ := tok.SignedString([]byte("a"))
	if _, err := ParseEmployeeToken(s, []byte("b")); err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd services/gateway && go test ./internal/identity/ -count=1`

Expected: FAIL，`ParseEmployeeToken` 未定义。

- [ ] **Step 3: 实现解析**

`services/gateway/internal/identity/employee.go`:

```go
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
```

- [ ] **Step 4: 再跑测试**

Run: `cd services/gateway && go test ./internal/identity/ -count=1`

Expected: PASS（此时只有 employee 测试；guest 文件下一步再加）。

- [ ] **Step 5: Commit**

```bash
git add services/gateway
git commit -m "$(cat <<'EOF'
feat: parse employee JWT identity contract

EOF
)"
```

---

### Task 2: 嘉宾会话 JWT

**Files:**
- Create: `services/gateway/internal/identity/guest.go`
- Test: `services/gateway/internal/identity/guest_test.go`

**Interfaces:**
- Consumes: `Principal`、`KindGuest`
- Produces: `func IssueGuestSession(p Principal, hmacSecret []byte, ttl time.Duration) (string, error)`；`func ParseGuestSession(tokenString string, hmacSecret []byte) (Principal, error)`

- [ ] **Step 1: 写失败测试**

`services/gateway/internal/identity/guest_test.go`:

```go
package identity

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func jwtEmployee(t *testing.T, secret []byte) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  "u-1",
		"kind": KindEmployee,
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGuestSession_RoundTrip(t *testing.T) {
	secret := []byte("guest-secret")
	in := Principal{
		Kind:        KindGuest,
		GuestID:     "gst_1",
		MeetingID:   "mtg_1",
		DisplayName: "Bob",
	}
	s, err := IssueGuestSession(in, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseGuestSession(s, secret)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != KindGuest || out.GuestID != "gst_1" || out.MeetingID != "mtg_1" || out.DisplayName != "Bob" {
		t.Fatalf("got %+v", out)
	}
}

func TestParseGuestSession_RejectsEmployeeToken(t *testing.T) {
	secret := []byte("guest-secret")
	emp := jwtEmployee(t, secret)
	if _, err := ParseGuestSession(emp, secret); err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd services/gateway && go test ./internal/identity/ -count=1`

Expected: FAIL，`IssueGuestSession` 未定义。

- [ ] **Step 3: 实现**

`services/gateway/internal/identity/guest.go`:

```go
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
	}, nil
}
```

- [ ] **Step 4: 再跑测试**

Run: `cd services/gateway && go test ./internal/identity/ -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add services/gateway/internal/identity
git commit -m "$(cat <<'EOF'
feat: issue and parse guest meeting sessions

EOF
)"
```

---

### Task 3: 会议存储（密码哈希、组织者）

**Files:**
- Create: `services/gateway/internal/meeting/model.go`
- Create: `services/gateway/internal/meeting/store.go`
- Test: `services/gateway/internal/meeting/store_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `type Meeting struct { ID string; Title string; PasswordHash string; OrganizerID string; Locked bool; CreatedAt time.Time }`；`type Store struct`；`func NewMemoryStore() *Store`；`func (s *Store) Create(title, organizerID, plainPassword string) (Meeting, string, error)` 返回 meeting 与明文密码；`func (s *Store) Get(id string) (Meeting, bool)`；`func (s *Store) CheckPassword(id, plain string) bool`；`func HashPassword(plain string) string`

PoC 先用内存 Store，接口保持为后续换 PostgreSQL 做准备。Task 4 再接 Postgres。本 Task 的 MemoryStore 必须通过同样的接口测试，Task 4 用相同测试跑 SQL 实现。

- [ ] **Step 1: 写失败测试**

`services/gateway/internal/meeting/store_test.go`:

```go
package meeting

import "testing"

func TestCreateAndCheckPassword(t *testing.T) {
	s := NewMemoryStore()
	m, plain, err := s.Create("standup", "u-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID == "" || m.OrganizerID != "u-1" || m.Locked {
		t.Fatalf("meeting %+v", m)
	}
	if len(plain) != 8 {
		t.Fatalf("password len %d", len(plain))
	}
	if !s.CheckPassword(m.ID, plain) {
		t.Fatal("password should match")
	}
	if s.CheckPassword(m.ID, "wrong") {
		t.Fatal("wrong password should fail")
	}
}

func TestGetMissing(t *testing.T) {
	s := NewMemoryStore()
	if _, ok := s.Get("nope"); ok {
		t.Fatal("expected missing")
	}
}
```

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd services/gateway && go test ./internal/meeting/ -count=1`

Expected: FAIL，`NewMemoryStore` 未定义。

- [ ] **Step 3: 实现内存 Store**

`services/gateway/internal/meeting/model.go`:

```go
package meeting

import "time"

type Meeting struct {
	ID           string
	Title        string
	PasswordHash string
	OrganizerID  string
	Locked       bool
	CreatedAt    time.Time
}
```

`services/gateway/internal/meeting/store.go`:

```go
package meeting

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

const passwordChars = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

type Store struct {
	mu       sync.Mutex
	meetings map[string]Meeting
}

func NewMemoryStore() *Store {
	return &Store{meetings: map[string]Meeting{}}
}

func HashPassword(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func randomPassword() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 8)
	for i := range out {
		out[i] = passwordChars[int(b[i])%len(passwordChars)]
	}
	return string(out), nil
}

func (s *Store) Create(title, organizerID, plainPassword string) (Meeting, string, error) {
	if title == "" {
		title = "即时会议"
	}
	plain := plainPassword
	var err error
	if plain == "" {
		plain, err = randomPassword()
		if err != nil {
			return Meeting{}, "", err
		}
	}
	id, err := randomPassword()
	if err != nil {
		return Meeting{}, "", err
	}
	id = "mtg_" + id
	m := Meeting{
		ID:           id,
		Title:        title,
		PasswordHash: HashPassword(plain),
		OrganizerID:  organizerID,
		Locked:       false,
		CreatedAt:    time.Now().UTC(),
	}
	s.mu.Lock()
	s.meetings[m.ID] = m
	s.mu.Unlock()
	return m, plain, nil
}

func (s *Store) Get(id string) (Meeting, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.meetings[id]
	return m, ok
}

func (s *Store) CheckPassword(id, plain string) bool {
	m, ok := s.Get(id)
	if !ok {
		return false
	}
	return m.PasswordHash == HashPassword(plain)
}

func (s *Store) SetLocked(id string, locked bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.meetings[id]
	if !ok {
		return fmt.Errorf("meeting not found")
	}
	m.Locked = locked
	s.meetings[id] = m
	return nil
}
```

- [ ] **Step 4: 再跑测试**

Run: `cd services/gateway && go test ./internal/meeting/ -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add services/gateway/internal/meeting
git commit -m "$(cat <<'EOF'
feat: in-memory meeting store with hashed room passwords

EOF
)"
```

---

### Task 4: PostgreSQL Store 与 Compose

**Files:**
- Create: `infra/compose/docker-compose.yml`
- Create: `infra/compose/.env.example`
- Create: `services/gateway/internal/meeting/pg_store.go`
- Create: `services/gateway/internal/meeting/pg_store_test.go`
- Modify: `services/gateway/internal/meeting/store.go` — 抽出 `type Repository interface`

**Interfaces:**
- Consumes: `Meeting`、`HashPassword`、`Create` 语义与 Memory 相同
- Produces: `type Repository interface { Create(title, organizerID, plainPassword string) (Meeting, string, error); Get(id string) (Meeting, bool); CheckPassword(id, plain string) bool; SetLocked(id string, locked bool) error }`；`func NewPGStore(ctx context.Context, dsn string) (*PGStore, error)`；`MemoryStore` 实现同一 interface

- [ ] **Step 1: 让 MemoryStore 实现 interface，并写 PG 测试（需 Docker）**

在 `store.go` 顶部增加：

```go
type Repository interface {
	Create(title, organizerID, plainPassword string) (Meeting, string, error)
	Get(id string) (Meeting, bool)
	CheckPassword(id, plain string) bool
	SetLocked(id string, locked bool) error
}
```

确认 `var _ Repository = (*Store)(nil)` 放在 `store.go` 末尾。

`infra/compose/docker-compose.yml`:

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: metuai
      POSTGRES_PASSWORD: metuai
      POSTGRES_DB: metuai
    ports:
      - "5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U metuai"]
      interval: 3s
      timeout: 3s
      retries: 20

  livekit:
    image: livekit/livekit-server:v1.8.4
    command: --dev --bind 0.0.0.0
    ports:
      - "7880:7880"
      - "7881:7881"
      - "7882:7882/udp"
```

`infra/compose/.env.example`:

```
DATABASE_URL=postgres://metuai:metuai@127.0.0.1:5432/metuai?sslmode=disable
EMPLOYEE_JWT_SECRET=dev-employee-secret
GUEST_JWT_SECRET=dev-guest-secret
LIVEKIT_URL=ws://127.0.0.1:7880
LIVEKIT_API_KEY=devkey
LIVEKIT_API_SECRET=secret
DEV_ALLOW_EMPLOYEE_WEB=true
HTTP_ADDR=:8080
```

`services/gateway/internal/meeting/pg_store_test.go`:

```go
package meeting

import (
	"context"
	"os"
	"testing"
)

func TestPGStore_CreateAndCheckPassword(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	s, err := NewPGStore(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	m, plain, err := s.Create("pg-meet", "u-9", "")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(m.ID)
	if !ok || got.OrganizerID != "u-9" {
		t.Fatalf("get %+v ok=%v", got, ok)
	}
	if !s.CheckPassword(m.ID, plain) || s.CheckPassword(m.ID, "nope") {
		t.Fatal("password check failed")
	}
}
```

- [ ] **Step 2: 起 Postgres，确认测试因缺实现而失败或 skip 后实现失败**

```bash
docker compose -f infra/compose/docker-compose.yml up -d postgres
```

Expected: postgres healthy。

Run: `cd services/gateway && DATABASE_URL='postgres://metuai:metuai@127.0.0.1:5432/metuai?sslmode=disable' go test ./internal/meeting/ -count=1`

Expected: FAIL，`NewPGStore` 未定义。

- [ ] **Step 3: 实现 PGStore**

```bash
cd services/gateway
go get github.com/jackc/pgx/v5
go get github.com/jackc/pgx/v5/pgxpool
```

`services/gateway/internal/meeting/pg_store.go`:

```go
package meeting

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(ctx context.Context, dsn string) (*PGStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	_, err = pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS meetings (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  organizer_id TEXT NOT NULL,
  locked BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS recording_acks (
  meeting_id TEXT NOT NULL,
  principal_key TEXT NOT NULL,
  acked_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (meeting_id, principal_key)
);
`)
	if err != nil {
		return nil, err
	}
	return &PGStore{pool: pool}, nil
}

func (s *PGStore) Create(title, organizerID, plainPassword string) (Meeting, string, error) {
	mem := NewMemoryStore()
	m, plain, err := mem.Create(title, organizerID, plainPassword)
	if err != nil {
		return Meeting{}, "", err
	}
	_, err = s.pool.Exec(context.Background(),
		`INSERT INTO meetings (id, title, password_hash, organizer_id, locked, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		m.ID, m.Title, m.PasswordHash, m.OrganizerID, m.Locked, m.CreatedAt,
	)
	if err != nil {
		return Meeting{}, "", err
	}
	return m, plain, nil
}

func (s *PGStore) Get(id string) (Meeting, bool) {
	var m Meeting
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, title, password_hash, organizer_id, locked, created_at FROM meetings WHERE id=$1`, id,
	).Scan(&m.ID, &m.Title, &m.PasswordHash, &m.OrganizerID, &m.Locked, &m.CreatedAt)
	if err != nil {
		return Meeting{}, false
	}
	return m, true
}

func (s *PGStore) CheckPassword(id, plain string) bool {
	m, ok := s.Get(id)
	if !ok {
		return false
	}
	return m.PasswordHash == HashPassword(plain)
}

func (s *PGStore) SetLocked(id string, locked bool) error {
	tag, err := s.pool.Exec(context.Background(), `UPDATE meetings SET locked=$2 WHERE id=$1`, id, locked)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("meeting not found")
	}
	return nil
}

func (s *PGStore) AckRecording(meetingID, principalKey string) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO recording_acks (meeting_id, principal_key, acked_at)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (meeting_id, principal_key) DO NOTHING`,
		meetingID, principalKey, time.Now().UTC(),
	)
	return err
}

func (s *PGStore) HasAck(meetingID, principalKey string) bool {
	var n int
	err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(1) FROM recording_acks WHERE meeting_id=$1 AND principal_key=$2`,
		meetingID, principalKey,
	).Scan(&n)
	return err == nil && n == 1
}

func principalKey(kind, userID, guestID string) string {
	if kind == "guest" {
		return "guest:" + guestID
	}
	return "employee:" + userID
}
```

把 `AckRecording` / `HasAck` / `principalKey` 也加到 MemoryStore，否则 HTTP 层无法对两种 Store 统一。在 `store.go` 的 `Store` 上增加：

```go
type ackKey struct{ meetingID, principal string }

// 在 Store 结构体增加: acks map[ackKey]struct{}
```

初始化 `acks: map[ackKey]struct{}{}`。

```go
func (s *Store) AckRecording(meetingID, principalKey string) error {
	if _, ok := s.Get(meetingID); !ok {
		return fmt.Errorf("meeting not found")
	}
	s.mu.Lock()
	s.acks[ackKey{meetingID, principalKey}] = struct{}{}
	s.mu.Unlock()
	return nil
}

func (s *Store) HasAck(meetingID, principalKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.acks[ackKey{meetingID, principalKey}]
	return ok
}
```

`Repository` 增加：

```go
AckRecording(meetingID, principalKey string) error
HasAck(meetingID, principalKey string) bool
```

导出函数供 HTTP 使用：

```go
func PrincipalKey(kind, userID, guestID string) string {
	if kind == "guest" {
		return "guest:" + guestID
	}
	return "employee:" + userID
}
```

删除 `pg_store.go` 里未导出的 `principalKey`，HTTP 一律调用 `meeting.PrincipalKey`。

- [ ] **Step 4: 再跑测试**

Run: `cd services/gateway && go test ./internal/meeting/ -count=1 && DATABASE_URL='postgres://metuai:metuai@127.0.0.1:5432/metuai?sslmode=disable' go test ./internal/meeting/ -count=1`

Expected: 两组都 PASS（无 DATABASE_URL 时 PG 测试 skip；有 URL 时通过）。

- [ ] **Step 5: Commit**

```bash
git add infra/compose services/gateway
git commit -m "$(cat <<'EOF'
feat: persist meetings in postgres and boot livekit compose

EOF
)"
```

---

### Task 5: LiveKit token 签发

**Files:**
- Create: `services/gateway/internal/livekit/token.go`
- Test: `services/gateway/internal/livekit/token_test.go`

**Interfaces:**
- Consumes: `identity.Principal`、会议 ID
- Produces: `func IssueRoomToken(apiKey, apiSecret, room, identity, name string) (string, error)`

- [ ] **Step 1: 写失败测试**

```bash
cd services/gateway
go get github.com/livekit/protocol@v1.38.0
go get github.com/livekit/server-sdk-go/v2
```

若 `server-sdk-go/v2` 的 import 路径以模块文档为准（`github.com/livekit/server-sdk-go/v2/auth` 或 `github.com/livekit/protocol/auth`）。本计划使用 `github.com/livekit/protocol/auth`：

`services/gateway/internal/livekit/token_test.go`:

```go
package livekit

import (
	"testing"

	"github.com/livekit/protocol/auth"
)

func TestIssueRoomToken_Decodable(t *testing.T) {
	s, err := IssueRoomToken("devkey", "secret", "mtg_1", "u-1", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if s == "" {
		t.Fatal("empty token")
	}
	v, err := auth.ParseAPIToken(s)
	if err != nil {
		t.Fatal(err)
	}
	id, err := v.Identity()
	if err != nil || id != "u-1" {
		t.Fatalf("identity %s err=%v", id, err)
	}
}
```

若 `ParseAPIToken` 在当前版本不存在，改为只断言 `err == nil && len(s) > 20`。

- [ ] **Step 2: 跑测试，确认失败**

Run: `cd services/gateway && go test ./internal/livekit/ -count=1`

Expected: FAIL，`IssueRoomToken` 未定义。

- [ ] **Step 3: 实现**

`services/gateway/internal/livekit/token.go`:

```go
package livekit

import (
	"time"

	"github.com/livekit/protocol/auth"
)

func IssueRoomToken(apiKey, apiSecret, room, identity, name string) (string, error) {
	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{RoomJoin: true, Room: room, CanPublish: true, CanSubscribe: true}
	at.AddGrant(grant).SetIdentity(identity).SetName(name).SetValidFor(2 * time.Hour)
	return at.ToJWT()
}
```

方法名若与 SDK 不符，以编译器为准改成该版本的 `SetVideoGrant` / `ToJWT`。

- [ ] **Step 4: 再跑测试**

Run: `cd services/gateway && go test ./internal/livekit/ -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add services/gateway
git commit -m "$(cat <<'EOF'
feat: issue LiveKit room join tokens

EOF
)"
```

---

### Task 6: Gin HTTP API

**Files:**
- Create: `services/gateway/internal/config/config.go`
- Create: `services/gateway/internal/identity/middleware.go`
- Create: `services/gateway/internal/meeting/http.go`
- Create: `services/gateway/internal/meeting/http_test.go`
- Create: `services/gateway/cmd/gateway/main.go`

**Interfaces:**
- Consumes: `ParseEmployeeToken`、`IssueGuestSession`、`ParseGuestSession`、`Repository`、`IssueRoomToken`、`PrincipalKey`
- Produces: HTTP 路由如下

| 方法 | 路径 | 鉴权 | 行为 |
|---|---|---|---|
| GET | `/healthz` | 无 | `{"ok":true}` |
| POST | `/v1/meetings` | Employee Bearer | 创建会议，响应含一次性 `password` |
| POST | `/v1/meetings/:id/guest-session` | 无 | body `{password, display_name}`，返回 `token` |
| POST | `/v1/meetings/:id/recording-ack` | Employee 或 Guest Bearer | 记录确认 |
| POST | `/v1/meetings/:id/livekit-token` | Employee 或 Guest Bearer | 未 ack 或会议锁定（非组织者）则 403；返回 `{token, livekit_url}` |

- [ ] **Step 1: 用 httptest 写失败测试**

`services/gateway/internal/config/config.go`:

```go
package config

import "os"

type Config struct {
	HTTPAddr             string
	DatabaseURL          string
	EmployeeJWTSecret    []byte
	GuestJWTSecret       []byte
	LiveKitURL           string
	LiveKitAPIKey        string
	LiveKitAPISecret     string
	DevAllowEmployeeWeb  bool
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

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
```

`services/gateway/internal/meeting/http_test.go` 使用 MemoryStore + 测试员工 JWT：

```go
package meeting

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"metuai/services/gateway/internal/identity"
)

func employeeJWT(t *testing.T, secret []byte) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":          "u-1",
		"kind":         identity.KindEmployee,
		"email":        "a@corp.local",
		"display_name": "Alice",
		"exp":          time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateMeetingAndGuestCannotGetLivekitBeforeAck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secretEmp := []byte("emp")
	secretGst := []byte("gst")
	store := NewMemoryStore()
	r := gin.New()
	RegisterRoutes(r, store, secretEmp, secretGst, "ws://localhost:7880", "devkey", "secret", true)

	body := bytes.NewBufferString(`{"title":"t1"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/meetings", body)
	req.Header.Set("Authorization", "Bearer "+employeeJWT(t, secretEmp))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	gbody := bytes.NewBufferString(`{"password":"` + created.Password + `","display_name":"Bob"}`)
	greq := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+created.ID+"/guest-session", gbody)
	gw := httptest.NewRecorder()
	r.ServeHTTP(gw, greq)
	if gw.Code != 200 {
		t.Fatalf("guest session %d %s", gw.Code, gw.Body.String())
	}
	var gs struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(gw.Body.Bytes(), &gs)

	lt := httptest.NewRequest(http.MethodPost, "/v1/meetings/"+created.ID+"/livekit-token", nil)
	lt.Header.Set("Authorization", "Bearer "+gs.Token)
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, lt)
	if lw.Code != http.StatusForbidden {
		t.Fatalf("expected 403 before ack, got %d %s", lw.Code, lw.Body.String())
	}
}
```

- [ ] **Step 2: 跑测试，确认失败**

```bash
cd services/gateway
go get github.com/gin-gonic/gin
go test ./internal/meeting/ -count=1
```

Expected: FAIL，`RegisterRoutes` 未定义。

- [ ] **Step 3: 实现中间件与路由**

`services/gateway/internal/identity/middleware.go`:

```go
package identity

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const CtxPrincipal = "principal"

func EmployeeAuth(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, err := ParseEmployeeToken(bearer(c), secret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Set(CtxPrincipal, p)
		c.Next()
	}
}

func AnyMeetingAuth(employeeSecret, guestSecret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := bearer(c)
		if p, err := ParseEmployeeToken(raw, employeeSecret); err == nil {
			c.Set(CtxPrincipal, p)
			c.Next()
			return
		}
		if p, err := ParseGuestSession(raw, guestSecret); err == nil {
			c.Set(CtxPrincipal, p)
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
	}
}

func MustPrincipal(c *gin.Context) Principal {
	return c.MustGet(CtxPrincipal).(Principal)
}

func bearer(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	return strings.TrimPrefix(h, "Bearer ")
}
```

`services/gateway/internal/meeting/http.go`:

```go
package meeting

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"metuai/services/gateway/internal/identity"
	lktoken "metuai/services/gateway/internal/livekit"
)

type createBody struct {
	Title string `json:"title"`
}

type guestBody struct {
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

func RegisterRoutes(
	r *gin.Engine,
	repo Repository,
	employeeSecret, guestSecret []byte,
	livekitURL, livekitKey, livekitSecret string,
	allowEmployeeWeb bool,
) {
	_ = allowEmployeeWeb

	r.POST("/v1/meetings", identity.EmployeeAuth(employeeSecret), func(c *gin.Context) {
		var body createBody
		_ = c.ShouldBindJSON(&body)
		p := identity.MustPrincipal(c)
		m, plain, err := repo.Create(body.Title, p.UserID, "")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"id":           m.ID,
			"title":        m.Title,
			"password":     plain,
			"organizer_id": m.OrganizerID,
		})
	})

	r.POST("/v1/meetings/:id/guest-session", func(c *gin.Context) {
		id := c.Param("id")
		m, ok := repo.Get(id)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		if m.Locked {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_locked"})
			return
		}
		var body guestBody
		if err := c.ShouldBindJSON(&body); err != nil || body.Password == "" || body.DisplayName == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password and display_name required"})
			return
		}
		if !repo.CheckPassword(id, body.Password) {
			c.JSON(http.StatusForbidden, gin.H{"error": "invalid_password"})
			return
		}
		guestID, err := RandomID("gst_")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		token, err := identity.IssueGuestSession(identity.Principal{
			Kind:        identity.KindGuest,
			GuestID:     guestID,
			MeetingID:   id,
			DisplayName: body.DisplayName,
		}, guestSecret, 15*time.Minute)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"token": token, "guest_id": guestID})
	})

	auth := identity.AnyMeetingAuth(employeeSecret, guestSecret)

	r.POST("/v1/meetings/:id/recording-ack", auth, func(c *gin.Context) {
		id := c.Param("id")
		if _, ok := repo.Get(id); !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		p := identity.MustPrincipal(c)
		if p.Kind == identity.KindGuest && p.MeetingID != id {
			c.JSON(http.StatusForbidden, gin.H{"error": "wrong_meeting"})
			return
		}
		if err := repo.AckRecording(id, PrincipalKey(p.Kind, p.UserID, p.GuestID)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	r.POST("/v1/meetings/:id/livekit-token", auth, func(c *gin.Context) {
		id := c.Param("id")
		m, ok := repo.Get(id)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
			return
		}
		p := identity.MustPrincipal(c)
		if p.Kind == identity.KindGuest && p.MeetingID != id {
			c.JSON(http.StatusForbidden, gin.H{"error": "wrong_meeting"})
			return
		}
		if m.Locked && p.UserID != m.OrganizerID {
			c.JSON(http.StatusForbidden, gin.H{"error": "meeting_locked"})
			return
		}
		key := PrincipalKey(p.Kind, p.UserID, p.GuestID)
		if !repo.HasAck(id, key) {
			c.JSON(http.StatusForbidden, gin.H{"error": "recording_ack_required"})
			return
		}
		roomID := id
		ident := p.UserID
		name := p.DisplayName
		if p.Kind == identity.KindGuest {
			ident = "guest:" + p.GuestID
		}
		tok, err := lktoken.IssueRoomToken(livekitKey, livekitSecret, roomID, ident, name)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"token": tok, "livekit_url": livekitURL})
	})
}
```

在 `store.go` 增加导出函数（把原来的 `randomPassword` 改名为可复用）：

```go
func RandomID(prefix string) (string, error) {
	s, err := randomPassword()
	if err != nil {
		return "", err
	}
	return prefix + s, nil
}
```

`Create` 里生成会议 ID 改为 `RandomID("mtg_")`。

`services/gateway/cmd/gateway/main.go`:

```go
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
		pg, err := meeting.NewPGStore(context.Background(), cfg.DatabaseURL)
		if err != nil {
			log.Fatal(err)
		}
		repo = pg
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
```

- [ ] **Step 4: 再跑测试**

Run: `cd services/gateway && go test ./... -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add services/gateway
git commit -m "$(cat <<'EOF'
feat: expose gin meeting and livekit token APIs

EOF
)"
```

---

### Task 7: Vite 进会页

**Files:**
- Create: `apps/web/package.json`
- Create: `apps/web/tsconfig.json`
- Create: `apps/web/tsconfig.app.json`
- Create: `apps/web/vite.config.ts`
- Create: `apps/web/index.html`
- Create: `apps/web/src/main.tsx`
- Create: `apps/web/src/App.tsx`
- Create: `apps/web/src/lib/api.ts`
- Create: `apps/web/src/pages/HomePage.tsx`
- Create: `apps/web/src/pages/JoinGuestPage.tsx`
- Create: `apps/web/src/pages/RoomPage.tsx`
- Create: `apps/web/src/vite-env.d.ts`
- Modify: `README.md`

**Interfaces:**
- Consumes: 上述 HTTP API
- Produces: 浏览器可创建会议、嘉宾输入密码、确认录音后进入 LiveKit 房间

- [ ] **Step 1: 脚手架**

```bash
cd apps/web
pnpm create vite . --template react-ts
pnpm install
pnpm add livekit-client @livekit/components-react @livekit/components-styles
```

`vite.config.ts` 增加代理：

```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/v1": "http://127.0.0.1:8080",
      "/healthz": "http://127.0.0.1:8080",
    },
  },
});
```

`src/lib/api.ts`:

```ts
export type CreatedMeeting = {
  id: string;
  title: string;
  password: string;
  organizer_id: string;
};

export async function createMeeting(employeeToken: string, title: string): Promise<CreatedMeeting> {
  const res = await fetch("/v1/meetings", {
    method: "POST",
    headers: {
      Authorization: `Bearer ${employeeToken}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ title }),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export async function guestSession(meetingId: string, password: string, displayName: string): Promise<{ token: string }> {
  const res = await fetch(`/v1/meetings/${meetingId}/guest-session`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password, display_name: displayName }),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export async function ackRecording(meetingId: string, token: string): Promise<void> {
  const res = await fetch(`/v1/meetings/${meetingId}/recording-ack`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) throw new Error(await res.text());
}

export async function livekitToken(meetingId: string, token: string): Promise<{ token: string; livekit_url: string }> {
  const res = await fetch(`/v1/meetings/${meetingId}/livekit-token`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}
```

`HomePage`：textarea 贴员工 JWT、会议标题、按钮创建；展示 `id` 与 `password`；按钮「确认录音并入会」走 ack + livekit-token，再 `navigate` 到房间页（用 `sessionStorage` 存 `lkToken`、`lkUrl`）。

`JoinGuestPage`：路径 `/join/:meetingId`，输入密码与显示名。

`RoomPage`：

```tsx
import { LiveKitRoom, VideoConference } from "@livekit/components-react";
import "@livekit/components-styles";

export function RoomPage(props: { url: string; token: string }) {
  return (
    <LiveKitRoom serverUrl={props.url} token={props.token} connect={true} audio={true} video={true}>
      <VideoConference />
    </LiveKitRoom>
  );
}
```

`App.tsx` 用简单 `window.location.pathname` 分支即可，不必先上 TanStack Router：`/` 员工首页，`/join/:id` 嘉宾，`/room` 房间。

- [ ] **Step 2: 手动验收（本计划前端不做组件单测）**

起依赖与网关：

```bash
docker compose -f infra/compose/docker-compose.yml up -d
cd services/gateway && go run ./cmd/gateway
```

另开终端：

```bash
cd apps/web && pnpm dev
```

生成员工 JWT（可放 `scripts/dev_employee_jwt.go` 或文档中的 `jwt.io` 说明）。提供 `services/gateway/cmd/devtoken/main.go`：

```go
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
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":          "u-dev",
		"kind":         "employee",
		"email":        "dev@corp.local",
		"display_name": "Dev User",
		"exp":          time.Now().Add(24 * time.Hour).Unix(),
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		panic(err)
	}
	fmt.Println(s)
}
```

Run: `cd services/gateway && go run ./cmd/devtoken`

Expected: 打印一行 JWT。

浏览器打开 `http://127.0.0.1:5173`，粘贴 token，创建会议，确认录音，应看到自己的摄像头。第二个浏览器窗口打开 `/join/<id>` 用密码进入，两人应互相看到。

若 LiveKit 连不上：确认 compose 中 `--dev` 的默认 key 为 `devkey`/`secret`，与网关环境变量一致；`LIVEKIT_URL` 对浏览器必须是 `ws://127.0.0.1:7880`。

- [ ] **Step 3: 更新根 README**

`README.md` 写：架构文档路径、如何 compose up、如何 `go run`、如何 `pnpm dev`、如何 `go run ./cmd/devtoken`。

- [ ] **Step 4: Commit**

```bash
git add apps/web README.md services/gateway/cmd/devtoken
git commit -m "$(cat <<'EOF'
feat: add web client to create meetings and join livekit rooms

EOF
)"
```

---

### Task 8: 计划自检与手工清单

- [ ] **Step 1: 对照规格**

确认本计划已覆盖：员工 token 契约、嘉宾密码会话、进会录音确认、LiveKit 进房、PostgreSQL 会议表。未覆盖项已列在「后续计划」，不要在本 PR 范围悄悄做 Vespa/Dapr。

- [ ] **Step 2: 跑全部网关测试**

Run: `cd services/gateway && go test ./... -count=1`

Expected: PASS

- [ ] **Step 3: 两人进房手工勾选**

- 员工创建会议并复制密码
- 未点确认时请求 livekit-token 返回 403 `recording_ack_required`
- 确认后音视频可用
- 错误密码无法拿 guest session

---

## 执行说明

实现时若 LiveKit Go SDK 的 token API 与上文符号不一致，以当前模块编译为准，测试「能 decode 且 identity 正确」这一行为不变。

本地默认 `DEV_ALLOW_EMPLOYEE_WEB=true`。Tauri 计划落地后改为 `false`，员工无 `X-Metuai-Client: tauri` 时创建会议与 livekit-token 返回 403。
