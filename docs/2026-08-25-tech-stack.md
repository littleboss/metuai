# 前后端技术栈

**日期**：2026-08-25  
**状态**：已确认（现场已有 Kubernetes、Dapr、Vespa）  
**依据**：`docs/2026-08-25-private-video-meeting-ai-architecture.md`  
**UI 参考**：[Meetily](https://github.com/Zackriya-Solutions/meetily) 的会前/会后壳（侧栏、会议列表、转写/纪要页），不是它的本机转写架构

本文只定实现技术，不改产品规则。身份登录由企业现有系统负责；本仓库实现会议、媒体、会后 AI 与知识检索。

## 1. 总表

| 层 | 选型 | 职责边界 |
|---|---|---|
| 会中/会后 UI | Vite + React + TypeScript + Tailwind + shadcn/ui | 一份 SPA：员工 Tauri 与嘉宾浏览器共用 |
| 会中媒体组件 | `livekit-client` + `@livekit/components-react` | 画面、静音、共享、聊天；皮肤与 shadcn 壳对齐 |
| 员工桌面 | Tauri 2 + Rust | WebView 加载同一 SPA；仅员工端本机麦克风备份与续传 |
| 企业网关 | Go + Gin | 会议 API、RBAC、LiveKit Token、上传、审计；**只校验企业身份 token** |
| 身份 | 企业现有 IdP（调用方自备） | 认证、发 token、账号目录；本系统不自建 OIDC/LDAP 连接器 |
| 编排 / 队列 | Dapr Workflow + Dapr Pub/Sub | 会后状态机与 Worker 抢任务；broker 用现场已有组件 |
| 业务数据 | PostgreSQL | 会议、参会人、转写原文、纪要、ACL、审计、任务状态的**唯一权威** |
| 知识检索 | Vespa | 带 ACL 字段的检索副本；混合关键词 + 向量 |
| 对象存储 | MinIO 或企业 S3 | 音视频、本地备份、导出文件 |
| 会议媒体 | LiveKit Server + Egress | 实时转发、独立音轨、混音、房间画面 |
| AI Worker | Python 3.12 | ASR、纪要 LLM、embedding、写 Vespa |
| LLM | 私有 Qwen + vLLM/Ollama | 会后生成；CPU 允许排队 |
| 部署 | 现有 Kubernetes + Dapr | 网关与 Worker 带 sidecar；不把 Dapr 当业务库 |

## 2. 身份边界

企业负责「这人是谁」。会议网关负责「他在这场会能做什么」。

企业身份系统提供：

- 员工登录与 token（OIDC、LDAP 或其它，由现场决定）
- 员工目录（创建会议时勾选内部参会人）
- 可选：企业 SMTP（没有则用会中验证码绑嘉宾邮箱）

会议网关约定一份 **员工身份契约**（header 或 JWT claim），至少包含：

```text
subject          稳定用户 ID
kind             employee
email            建议有；用于与嘉宾邮箱去重
display_name     进会快照用
roles            如 system_admin / audit_admin；会议内组织者角色另存在会议服务
```

嘉宾**不走企业 IdP**。密码进会后由会议网关签发短期嘉宾会话；会后验证邮箱再升为稳定嘉宾身份。

网关：

- 校验 token 签名/内网内省，**不实现登录页和 IdP 连接器**
- 把 `subject` 映射为会议里的 `user_id`
- 区分员工（必须 Tauri、强制本地录音）与嘉宾（链接+密码、无本地录音）
- 执行会中锁/踢、会后 ACL、破窗审批

嘉宾进会仍走本系统的链接+密码；会后邮箱验证（SMTP 或会中验证码）仍由会议网关完成。这与「员工 IdP 自建」不冲突。

## 3. 前端

### 3.1 栈

- Vite 6、React、TypeScript
- Tailwind CSS、shadcn/ui、lucide-react、sonner
- TanStack Router、TanStack Query
- react-hook-form、zod
- LiveKit 客户端与 React 组件
- Tauri 2 API：仅员工端调用录音、深链、打开客户端

不用 Next.js：同一份静态资源要进 Tauri WebView 和嘉宾浏览器，SSR 无收益。

不整仓 fork LiveKit Meet：用其依赖的组件库，外壳做成 Meetily 式产品，而不是「打开就是一个房间」。

### 3.2 三个界面

1. **员工壳（参考 Meetily）**  
   左侧：会议列表、进行中、知识库、设置。  
   右侧：转写 / 纪要 / 待办 / 录像回看。
2. **会中**  
   LiveKit 网格 + 本产品的锁、踢、录音确认、本地录音暂停（仅员工 Tauri）。
3. **嘉宾进会**  
   密码 → 录音确认 → 会中。无完整侧栏。

不抄 Meetily：实时转写条、本机 Whisper、系统声混合、SQLite、笔记本上的 Ollama。

### 3.3 员工 Tauri / Rust

只做架构已锁定的本机备份：

- `cpal` 采本机麦克风（不采系统声 / loopback）
- 分块、checksum、加密落盘、断点续传到网关
- 暂停缺口写审计事件

会中音视频仍走 LiveKit，不走 Rust 混音引擎。

## 4. 企业网关（Go + Gin）

模块按包切开，避免单文件堆 API：

- `authn`：校验企业 token，得到身份契约
- `authz`：会议级 RBAC（组织者、参会人、嘉宾、破窗）
- `meeting`：创建、密码、邀请、锁、踢、结束、空闲超时
- `livekit`：签发房间 JWT（员工/嘉宾不同 identity）
- `upload`：员工本地录音分块上传与合并
- `artifact`：转写、纪要、下载、修订
- `knowledge`：把「当前用户可访问会议集合」交给 Vespa 查询，再调 LLM
- `audit`：只追加审计

Gin 中间件：请求 ID、鉴权、审计、上传大小限制。不在网关里跑 ASR。

会后流水线由网关 **启动 Dapr Workflow**（一场会一个实例），自己不在进程内阻塞转写。

## 5. Dapr 怎么用

现场已有 Dapr，用作运行时积木，**不是数据真相**。

| 积木 | 用途 | 禁止 |
|---|---|---|
| Workflow | 会后状态机：`MEDIA_READY` → 转写 → 抽纪要 → 索引 → `READY`；失败进 `RETRYABLE_ERROR` / `MANUAL_REVIEW` | 用工作流状态代替 PostgreSQL 里的任务行 |
| Pub/Sub | 把 ASR、embedding 等重活发给 Python Worker；失败 `RETRY`，耗尽进死信 | 只靠 Pub/Sub、没有 PG 任务状态 |
| State store | 仅工作流引擎需要的内部检查点（若组件如此配置） | 存会议、纪要、ACL |
| Service invocation | 网关调 Worker 健康检查等可选 | 替代 LiveKit 媒体路径 |

约定：

- 工作流代码放在 **Go 网关**（SDK 更成熟）
- Activity 里往 Pub/Sub 发任务，等 Worker 写回 PG 再继续
- Worker 崩溃：工作流按重试策略再投；业务阶段以 PostgreSQL 为准
- 死信与 `MANUAL_REVIEW` 必须可在管理端看见

Pub/Sub 的 broker 用现场已有 Dapr component（常见为 Redis/RabbitMQ/Kafka），本仓库不另选一套队列产品。

## 6. PostgreSQL 与 Vespa

### 6.1 PostgreSQL（权威）

保存：用户映射、会议、参会人、房间密码、聊天、转写片段、纪要与修订、待办、审计、会后任务状态、保留策略、破窗申请。

删除或改 ACL 时，先提交 PostgreSQL，再驱动 Vespa 更新。冲突时以 PG 为准，Vespa 可重建索引。

### 6.2 Vespa（检索副本）

每条可检索文档至少包含架构已定的 ACL 字段：

```text
meeting_id
allowed_user_ids
allowed_guest_emails
source_type
source_id
timestamp
text
embedding
```

查询必须带当前用户的 `user_id` 或已验证 `email` 过滤，**禁止先全库 ANN 再在应用层滤权限**。

组织者加白名单、知识保留到期：更新/删除 PG 后，同一流水线更新 Vespa。漏删视为权限缺陷。

录像文件不进 Vespa。纪要在 PG 被编辑后，对应文档要重索引。

## 7. Python Worker

独立 Deployment，带 Dapr sidecar，订阅会后任务：

- 媒体规范化
- FunASR / WhisperX（PoC 评测；交付只用已授权权重）
- 缺轨时对该员工本地备份做 `local_fallback`
- 私有 LLM 抽纪要（待办负责人只能是内部用户）
- embedding，写入 Vespa

不直接对浏览器提供 API。只消费 Pub/Sub，写 PostgreSQL 与 Vespa，必要时通知工作流继续。

## 8. 仓库布局

```text
apps/web          前端 SPA（Vite）
apps/desktop      Tauri 薄壳，加载 web 构建产物
services/gateway  Go + Gin + Dapr 工作流宿主
services/worker   Python 会后 Worker
infra             本系统的 K8s 清单 / Dapr component 覆盖（身份、Vespa、LiveKit 地址用现场配置）
docs              架构与本技术栈
```

不把企业 IdP、Vespa 集群、Dapr 控制面放进本仓库当「从零安装」。只声明依赖的 component 名、secret、以及会议服务自己的 Deployment。

## 9. PoC 范围（在现有集群上）

PoC 使用现成 K8s / Dapr / Vespa / 身份，不自建这些平面。仍遵守架构：内网嘉宾即可，不做公网 DMZ。

P0 建议顺序：

1. `apps/web` 会中（LiveKit）+ 员工壳骨架（Meetily 侧栏）
2. `apps/desktop` 深链与「员工禁止浏览器开会」
3. `services/gateway`：校验企业 token、建会、发 LiveKit JWT
4. Egress 落 MinIO + PG 元数据
5. Dapr Workflow 跑通「录音齐 → 假转写 → READY」
6. 再换真实 ASR，并写 Vespa（查询必须带 ACL）

## 10. 明确不做

- 在本服务实现 OIDC/LDAP 连接器或登录 UI
- 用 Dapr state / Vespa 替代 PostgreSQL
- 用 Meetily 的本机 Whisper 作为转写主路径
- 为 Jitsi 做媒体抽象
- PoC 阶段上公网 DMZ
