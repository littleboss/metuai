# metuai

私有化视频会议 AI 的概念验证项目。当前可通过网页创建会议、校验嘉宾密码、
确认录音告知并进入 LiveKit 房间。

## 文档

- [总体架构](docs/2026-08-25-private-video-meeting-ai-architecture.md)
- [技术栈](docs/2026-08-25-tech-stack.md)

## 本地启动

需要 Docker、Go 和 pnpm。Go toolchain 使用 Go 1.22+（当前 module 可能解析到更高版本；离线安装请固定匹配的 toolchain）。以下命令均从仓库根目录执行。

默认本机端口（避开常见的 5432 / 8080 / 7880 冲突）：

| 服务 | 地址 |
|---|---|
| Postgres | `127.0.0.1:55432` |
| LiveKit | `ws://127.0.0.1:17880` |
| Gateway | `http://127.0.0.1:18080` |
| Web | `http://127.0.0.1:5173` |

### 1. 启动 Postgres 与 LiveKit

```bash
docker compose -f infra/compose/docker-compose.yml up -d
```

LiveKit 开发模式默认 `devkey` / `secret`。

### 2. 启动网关

```bash
set -a
source infra/compose/.env.example
set +a
cd services/gateway
go run ./cmd/gateway
```

不设置 `DATABASE_URL` 时使用内存存储；按上面 source 后会连接 compose 中的 Postgres。

### 3. 生成开发员工 JWT

```bash
cd services/gateway
go run ./cmd/devtoken
```

打印 24 小时有效的开发令牌。若自定义了 `EMPLOYEE_JWT_SECRET`，生成时必须一致。

### 4. 启动网页

```bash
cd apps/web
pnpm install
pnpm dev
```

打开 `http://127.0.0.1:5173`，粘贴员工 JWT 并创建会议。创建成功后：

1. 复制嘉宾链接和会议密码。
2. 员工点击「确认录音并入会」。
3. 第二个浏览器打开嘉宾链接，输入显示名和密码。
4. 勾选录音确认后再进房；两端应能互相看到和听到。

未确认录音时，`/v1/meetings/:id/livekit-token` 返回 `403` 且 `error=recording_ack_required`。

## 检查

```bash
cd services/gateway && go test ./... -count=1
cd apps/web && pnpm typecheck && pnpm lint && pnpm build
```
