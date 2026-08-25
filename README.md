# metuai

私有化视频会议 AI 的概念验证项目。当前可通过网页创建会议、校验嘉宾密码、
确认录音告知并进入 LiveKit 房间。

## 文档

- [总体架构](docs/2026-08-25-private-video-meeting-ai-architecture.md)
- [技术栈](docs/2026-08-25-tech-stack.md)
- [POC 实施计划](docs/plans/2026-08-25-poc-meeting-room.md)

## 本地启动

需要 Docker、Go 和 pnpm。以下命令均从仓库根目录开始执行。

### 1. 启动 Postgres 与 LiveKit

```bash
docker compose -f infra/compose/docker-compose.yml up -d
```

LiveKit 的开发模式默认使用 `devkey` / `secret`，浏览器连接地址是
`ws://127.0.0.1:7880`。

### 2. 启动网关

```bash
cd services/gateway
go run ./cmd/gateway
```

网关默认监听 `http://127.0.0.1:8080`，不配置 `DATABASE_URL` 时使用内存存储。
如需连接 compose 中的 Postgres，可在启动前设置：

```bash
export DATABASE_URL='postgres://metuai:metuai@127.0.0.1:5432/metuai?sslmode=disable'
go run ./cmd/gateway
```

### 3. 生成开发员工 JWT

打开另一个终端：

```bash
cd services/gateway
go run ./cmd/devtoken
```

命令会打印一个有效期为 24 小时的本地开发令牌。若网关自定义了
`EMPLOYEE_JWT_SECRET`，生成令牌时必须使用相同的环境变量。

### 4. 启动网页

```bash
cd apps/web
pnpm install
pnpm dev
```

打开 `http://127.0.0.1:5173`，粘贴员工 JWT 并创建会议。创建成功后：

1. 复制页面展示的嘉宾链接和会议密码。
2. 员工点击“确认录音并入会”。
3. 在第二个浏览器窗口打开嘉宾链接，输入显示名和密码。
4. 勾选录音确认，再进入房间；两端应能互相看到和听到。

## 前端检查

```bash
cd apps/web
pnpm typecheck
pnpm lint
pnpm build
```

