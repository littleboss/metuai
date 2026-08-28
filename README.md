# metuai

私有化视频会议 AI 的概念验证项目。可网页建会、嘉宾密码入会、录音确认、LiveKit
开会；组织者可锁/踢/重置密码/结束；聊天落库；会后可跑假流水线看转写与纪要；
员工 Tauri 壳支持本机麦克风备份（cpal → 分块上传），会中页可操作。

## 文档

- [总体架构](docs/2026-08-25-private-video-meeting-ai-architecture.md)
- [技术栈](docs/2026-08-25-tech-stack.md)
- [实现进度](docs/PROGRESS.md)
- [员工桌面端](apps/desktop/README.md)
- [会后 Worker](services/worker/README.md)

## 一键 Compose（推荐 PoC）

需要 Docker。一条命令拉起 Postgres、Redis、LiveKit、Egress、MinIO、**网关 api** 与 **Web**：

```bash
cp infra/compose/.env.example infra/compose/.env   # 首次：写入 JWT 等（可改）
docker compose -f infra/compose/docker-compose.yml up --build
```

打开 `http://127.0.0.1:5173`：注册/登录 → 会议列表 → 大厅 → 入会门 → 会场 → 纪要。
嘉宾仍用链接+密码。

ASR/LLM 为可选的宿主机私有端点，经 compose 传入 `api`；本栈不附带 ASR GPU worker。

### JWT 密钥（运行时注入，镜像不含默认值）

`EMPLOYEE_JWT_SECRET` 与 `GUEST_JWT_SECRET` **不会**写进 Dockerfile `ARG`/`ENV`，
也**没有** Go 代码内默认值（空密钥时 `GET /readyz` 返回 503）。
Compose 使用必填插值 `${EMPLOYEE_JWT_SECRET:?must be set}`：未设置时 `up` 直接失败。

本地 PoC 可直接用 `.env.example` 里的 `dev-employee-secret` / `dev-guest-secret`
（复制为 `infra/compose/.env` 即可）。生产或共享环境请换成自己的随机值：

```bash
export EMPLOYEE_JWT_SECRET="$(openssl rand -hex 32)"
export GUEST_JWT_SECRET="$(openssl rand -hex 32)"
docker compose -f infra/compose/docker-compose.yml up --build
```

员工身份走 **register / login**（`POST /v1/auth/register`、`POST /v1/auth/login`），
返回 `{access_token,user}`。不要粘贴 JWT，也不再提供 `devtoken`。

### 端口与 LiveKit URL

| 服务 | 地址 |
|---|---|
| Postgres | `127.0.0.1:55432` |
| Redis | `127.0.0.1:16379` |
| LiveKit（宿主机直连，host-dev） | `ws://127.0.0.1:17880` |
| LiveKit（compose 浏览器，同源） | `ws://127.0.0.1:5173`（nginx `/rtc`） |
| MinIO API / Console | `http://127.0.0.1:19000` / `http://127.0.0.1:19001` |
| Gateway (api) | `http://127.0.0.1:18080` |
| Web | `http://127.0.0.1:5173` |

容器内网关用 `LIVEKIT_URL=ws://livekit:7880` 调 SDK；`livekit-token` 返回的
`livekit_url` 使用 `LIVEKIT_PUBLIC_URL`（compose 默认 `ws://127.0.0.1:5173`），
供浏览器经同源 nginx `/rtc` 连接 LiveKit（令牌里绝不能出现 `ws://livekit:7880`）。

Web 镜像由 nginx 提供静态资源，并把 `/v1`、`/healthz`、`/readyz` 反代到 `api:18080`，
`/rtc` 反代到 `livekit:7880`（WebSocket 信令）。
`api` 健康检查只探针 `GET /readyz`。

## 本地启动（宿主机 go run / pnpm，可选）

需要 Docker（仅依赖）、Go 和 pnpm。Go toolchain 使用 Go 1.22+（当前 module 可能解析到更高版本；构建镜像时用 `GOTOOLCHAIN=auto`）。以下命令均从仓库根目录执行。

### 1. 仅启动依赖（不含 api/web）

若只想自己跑网关和前端：

```bash
docker compose -f infra/compose/docker-compose.yml up -d postgres redis livekit livekit-egress minio minio-init
```

LiveKit 使用 `infra/compose/livekit.yaml`，密钥仍是 `devkey` / `secret`。这里不用
`--dev`：dev 模式不配置 Redis，而 `livekit-server` 正是通过 Redis 把录制任务派给
`livekit-egress` 的。MinIO 默认用户 `metuai` / `metuai-secret`，桶 `metuai-media`。

### 1b. 开启服务端录制（可选）

默认 `EGRESS_ENABLED=false`，网关不会调用 Egress，媒体元数据停在 `pending`。要真开录：

```bash
docker compose -f infra/compose/docker-compose.yml up -d
docker compose -f infra/compose/docker-compose.yml up -d --force-recreate livekit

bash scripts/check-egress-stack.sh

set -a
source infra/compose/.env.example
set +a
export EGRESS_ENABLED=true
# 关键：S3_ENDPOINT 是给 egress 容器用的，必须是 compose 网络内地址
export S3_ENDPOINT=http://minio:9000

cd services/gateway && go run ./cmd/egresscheck
```

开录发生在**参会人拿 LiveKit 令牌或会中心跳**时：若当时房还是空的，只记
`egress_deferred` 并稍后重试，**不会**对空房拉起 RoomComposite。结束会议时停止并回写
产物状态（`ready` / `failed`）。拿不到 LiveKit 终态时媒体保持 `started`，不会伪造成 `ready`。

员工本机麦克风上传：分块合并后网关会 **PutObject 到 MinIO**（`S3_UPLOAD_ENDPOINT`）。
断点续传基础：`GET /v1/meetings/:id/local-recording/:uploadId/status` 返回已收分块；
桌面端上传前会跳过已有块。

一键 PoC 验收（栈 + 建会 + 本机上传→MinIO + 假流水线）：

```bash
# 先启动网关，再：
bash scripts/poc-smoke.sh
```

### 2. 启动网关（宿主机）

```bash
set -a
source infra/compose/.env.example
set +a
cd services/gateway
go run ./cmd/gateway
```

不设置 `DATABASE_URL` 时使用内存存储；按上面 source 后会连接 compose 中的 Postgres。
`.env.example` 里的 `127.0.0.1` 地址面向宿主机；compose 的 `api` 服务会覆盖为容器内网主机名。

`EMPLOYEE_JWT_SECRET` / `GUEST_JWT_SECRET` **没有代码内默认值**。未设置时 `GET /readyz` 返回 503，建会/嘉宾会话/LiveKit 令牌失败关闭。本地请先 `source infra/compose/.env.example`。

### 3. 启动网页（宿主机）

```bash
cd apps/web
pnpm install
pnpm dev
```

打开 `http://127.0.0.1:5173`：注册/登录 → 会议列表 → 大厅 → 入会门 → 会场 → 纪要。嘉宾仍用链接+密码。

宿主机 `pnpm dev` 时，Vite 会把 `/rtc` 代理到 `127.0.0.1:17880`（与 compose nginx 行为一致）。
可将 `LIVEKIT_PUBLIC_URL=ws://127.0.0.1:5173` 走同源代理，或继续用 `ws://127.0.0.1:17880` 直连 LiveKit。

未确认录音时，`/v1/meetings/:id/livekit-token` 返回 `403` 且 `error=recording_ack_required`。  
将 `DEV_ALLOW_EMPLOYEE_WEB=false` 后，员工浏览器入会会被拒绝（需 `X-Metuai-Client: tauri`）。

## 检查

```bash
cd services/gateway && go test ./... -count=1
cd apps/web && pnpm typecheck && pnpm lint && pnpm build
```
