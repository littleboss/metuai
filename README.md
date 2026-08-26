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

## 本地启动

需要 Docker、Go 和 pnpm。Go toolchain 使用 Go 1.22+（当前 module 可能解析到更高版本；离线安装请固定匹配的 toolchain）。以下命令均从仓库根目录执行。

默认本机端口（避开常见的 5432 / 8080 / 7880 冲突）：

| 服务 | 地址 |
|---|---|
| Postgres | `127.0.0.1:55432` |
| Redis | `127.0.0.1:16379` |
| LiveKit | `ws://127.0.0.1:17880` |
| MinIO API / Console | `http://127.0.0.1:19000` / `http://127.0.0.1:19001` |
| Gateway | `http://127.0.0.1:18080` |
| Web | `http://127.0.0.1:5173` |

### 1. 启动 Postgres、Redis、LiveKit、Egress 与 MinIO

```bash
docker compose -f infra/compose/docker-compose.yml up -d
```

LiveKit 使用 `infra/compose/livekit.yaml`，密钥仍是 `devkey` / `secret`。这里不用
`--dev`：dev 模式不配置 Redis，而 `livekit-server` 正是通过 Redis 把录制任务派给
`livekit-egress` 的。MinIO 默认用户 `metuai` / `metuai-secret`，桶 `metuai-media`。

### 1b. 开启服务端录制（可选）

默认 `EGRESS_ENABLED=false`，网关不会调用 Egress，媒体元数据停在 `pending`。要真开录：

```bash
# 确认 compose 里 redis / livekit（带 livekit.yaml）/ livekit-egress / minio 都在跑
docker compose -f infra/compose/docker-compose.yml up -d
# 若 livekit 仍是旧的 --dev，强制重建：
docker compose -f infra/compose/docker-compose.yml up -d --force-recreate livekit

bash scripts/check-egress-stack.sh

set -a
source infra/compose/.env.example
set +a
export EGRESS_ENABLED=true
# 关键：S3_ENDPOINT 是给 egress 容器用的，必须是 compose 网络内地址
export S3_ENDPOINT=http://minio:9000

# 可选：探测 Redis→egress→MinIO（会进房发静音轨；空房间 alone 常卡 STARTING）
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
2. 员工勾选录音告知后点击「确认录音并入会」。
3. 第二个浏览器打开嘉宾链接，输入显示名和密码。
4. 勾选录音确认后再进房；两端应能互相看到和听到。
5. 组织者侧栏可锁定/解锁、重置密码、踢人、全员结束；右侧「会中留言」会写入数据库。

未确认录音时，`/v1/meetings/:id/livekit-token` 返回 `403` 且 `error=recording_ack_required`。  
将 `DEV_ALLOW_EMPLOYEE_WEB=false` 后，员工浏览器入会会被拒绝（需 `X-Metuai-Client: tauri`）。

## 检查

```bash
cd services/gateway && go test ./... -count=1
cd apps/web && pnpm typecheck && pnpm lint && pnpm build
```
