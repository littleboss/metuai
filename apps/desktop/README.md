# apps/desktop（员工 Tauri 壳）

技术栈文档要求：Tauri 2 加载同一份 `apps/web` SPA，并承担**本机麦克风备份**与续传。

## 当前状态

| 项 | 状态 |
|---|---|
| Tauri 2 工程可 `cargo test` / `cargo build` | ✅ |
| 录音状态机（IDLE→…→ACKED，含 RETRY_WAIT / UPLOAD_FAILED） | ✅ |
| 本机麦克风采集（`cpal`，默认输入设备） | ✅ |
| PCM 分块 + 每块 sha256 → 网关分块上传 API | ✅ |
| 服务端合并后整文件 checksum 比对 | ✅ |
| spool 落盘加密（AES-256-GCM，架构 §5.3） | ✅ PoC，密钥走 `METUAI_SPOOL_KEY` |
| 审计事件（开始/暂停/恢复/停止/上传/确认/删除） | ⚙️ stop/purge 时尽力回传；崩溃前未 flush 的内存事件仍可能丢失 |
| `X-Metuai-Client: tauri` | Web 在 Tauri WebView 内自动加头 |

## 前置（macOS）

```bash
# Git Bash / zsh
export PATH="$HOME/.cargo/bin:$PATH"
```

`cpal` 在 macOS 上走 CoreAudio，依赖 **Xcode Command Line Tools**（`coreaudio-sys`
构建时要用 `libclang` 生成 binding）：

```bash
xcode-select --install   # 已装过会提示 already installed
```

首次运行时 macOS 会弹麦克风授权。被拒绝 / 没有输入设备时 `start_local_recording`
不会失败，而是在返回值的 `mic.error` 里说明原因（录音会话照样建起来，前端可以
用 `append_local_pcm` 兜底喂 PCM）。

## 落盘加密：`METUAI_SPOOL_KEY`

录音在写进磁盘之前就被加密了（架构 §5.3）。这是**设备落盘保护** —— 防的是笔记本
丢了、或者同机其他账号翻目录；**不是**端到端加密：服务端仍然必须拿到明文 PCM 做
审计和 `local_fallback` ASR，所以解密发生在客户端上传之前，网关契约一个字没改。

### 怎么配密钥

```bash
# 生成一把 32 字节（64 个 hex 字符）的密钥
openssl rand -hex 32

# 启动桌面端前设进环境变量
export METUAI_SPOOL_KEY=<上面那 64 个 hex 字符>
```

取值优先级：

1. `METUAI_SPOOL_KEY`（64 hex 字符 = 32 字节）—— **生产必须设**，由企业下发；
2. 没设时退回**源码里的固定开发密钥**（`spool_crypto.rs` 的 `dev_default`）。
   它写在源码里，等于公开的，**只能用于本地开发**。

两条刻意的行为，都是为了不出现「以为加了密其实没有」：

- 环境变量设了但格式不对（不是 64 个 hex）→ `start_local_recording` **直接失败**，
  绝不悄悄退回开发密钥；
- 密钥取不到就不建会话，不会降级成明文落盘。

密钥换了之后，**旧的 spool 文件就解不开了**（上传会进 `UPLOAD_FAILED` 并保留密文）。
所以换密钥前先确认没有未上传的录音。

### 磁盘上长什么样

```text
<appData>/local-recording/<meetingId>/<uploadId>/
├── mic.pcm.enc   # 加密后的录音，长期留着的就是这一份
└── mic.pcm       # 上传时临时解出来的明文，purge 时一并删掉
```

`mic.pcm.enc` 的格式（自描述，`spool_crypto.rs`）：

```text
magic      8 bytes   "METUAI1\0"
帧 × N，每帧：
  nonce    12 bytes  文件随机前缀(8) || 帧序号 u32 大端(4)
  ct_len   4 bytes   u32 小端，密文长度（含 16 字节 GCM tag）
  ct       ct_len bytes
```

为什么是**一条 channel 消息一帧**而不是整段加密：录音是边录边写的，整段加密就得把
整场（一小时约 330 MB）攒在内存里才能算出 tag。分帧之后写盘线程收一块加一块，
内存占用是常数。

nonce 用「每文件随机前缀 + 帧内计数器」而不是每帧独立随机：GCM 最致命的误用是同一把
密钥下 nonce 重复，计数器保证文件内绝不重复，64 位随机前缀区分不同文件。附带好处是
解密时能推算出每帧本该用的 nonce，帧被重排或删掉会当场暴露。

两个文件权限都是 `0600`。`mic.pcm` 是**临时**的：解密中途失败、上传失败、以及
`purge` 时都会把它删掉，只留 `mic.pcm.enc`。「未拿到服务端确认前不删本地文件」
保住的是密文那一份 —— 重传时再解密一次即可，把明文一直留在磁盘上等于白加密。

## 怎么测

### 1. 单元测试（不需要麦克风、不需要网关）

```bash
export PATH="$HOME/.cargo/bin:$PATH"
cd apps/desktop/src-tauri
cargo test
```

覆盖的点：

- **checksum**：`chunk.rs` 用标准向量校验 sha256，并验证「流式算整个文件」
  与「一次性算」结果一致；
- **分块切分**：最后一块偏短、正好整除、空输入，以及 `FileChunks`（按偏移 seek）
  与内存参照实现逐块一致；
- **状态机**：`recording.rs` 覆盖 pause/resume、重复暂停被拒、失败重试回路、
  以及「没确认前不许删本地文件」；
- **上传链路**：`uploader.rs` 用假网关（`FakeSink`）跑通 PUT→complete→比对 checksum，
  并覆盖 401 不重试、5xx 重试、重试次数用尽、整文件 checksum 不符等分支；
- **落盘加密**：`spool_crypto.rs` 覆盖加解密往返、错误密钥解不开、密文被改一个 bit
  就报错、文件截断 / 帧重排 / 长度字段损坏被拒、以及「密钥不会进 `{:?}` 日志」；
  `session.rs` 再验一遍落盘文件**不是**裸 PCM（找不到明文片段）、明文与密文都是 `0600`；
- **端到端**：`lib.rs` 用假 PCM + 假网关串起「录 → 暂停 → 恢复 → 收尾 → 解密 → 分块上传」，
  断言服务端合并结果与录到的字节**完全一致**，暂停期间的数据没有落盘，
  以及解不开密文时保留密文、不留半截明文。

降级构建（没有音频依赖的机器）也必须绿：

```bash
cargo test --no-default-features
```

此时 `cpal` 不编译，`spawn_default_mic` 直接返回错误，其余逻辑照常测。

### 2. 真机联调（要麦克风 + 网关）

先起网关（默认 `http://127.0.0.1:8080`），拿一个员工 JWT，然后：

```bash
cd apps/desktop
export METUAI_SPOOL_KEY=$(openssl rand -hex 32)   # 不设就用源码里的开发密钥
pnpm run dev     # 会开桌面窗口，并自动起 apps/web 的 Vite
```

在 WebView 的 devtools console 里：

```ts
import { invoke } from '@tauri-apps/api/core'

// 开始录音（会弹麦克风授权）
await invoke('start_local_recording', {
  meetingId: 'mtg_1',
  gatewayBaseUrl: 'http://127.0.0.1:8080',
  employeeJwt: '<employee JWT>',
})

await invoke('recording_state')          // "RECORDING"
await invoke('pause_local_recording')    // "PAUSED"，并写一条审计缺口
await invoke('resume_local_recording')   // "RECORDING"

// 停止并上传：分块 PUT → complete → 比对整文件 checksum
await invoke('stop_and_upload_local_recording')
// => { state: "ACKED", parts: 7, checksum: "…", bytes: 3360000 }

await invoke('recording_audit')          // 审计事件列表
await invoke<number>('flush_recording_audit') // 手动回传尚未推送的审计
await invoke('purge_local_recording')    // 确认之后才允许删本地副本
```

验证服务端确实收到了：网关的 spool 目录下会有 `000000.part…` 和 `merged.bin`，
`complete` 返回的 `checksum` 就是 `merged.bin` 的 sha256，应与上面返回值一致。

验证本地确实加密了（`purge` 之前看）：

```bash
cd "<appData>/local-recording/<meetingId>/<uploadId>"
head -c 8 mic.pcm.enc | xxd    # 前 8 字节是 "METUAI1\0"，不是 PCM 样本
ls -l                          # 两份都应是 -rw-------
```

## Tauri commands

| command | 作用 |
|---|---|
| `start_local_recording(meetingId, gatewayBaseUrl, employeeJwt)` | 取落盘密钥 → 建加密 spool 文件 → 开麦 → `RECORDING` |
| `pause_local_recording` / `resume_local_recording` | 暂停 / 恢复，并写审计缺口 |
| `append_local_pcm(pcm)` | 手动喂 s16le PCM（没有麦克风时的兜底路径） |
| `stop_and_upload_local_recording` | 停麦 → 收尾落盘（密文）→ 解出临时明文 → 分块上传 → 校验 → `ACKED` |
| `recording_state` | 当前状态名（`IDLE` / `RECORDING` / … / `ACKED`） |
| `recording_audit` | 审计事件列表 |
| `flush_recording_audit` | 把未回传的审计 POST 到网关 |
| `purge_local_recording` | 已确认后按策略删本地副本，密文和临时明文一起删（`ACKED` → `PURGED`） |

## 录音状态机（架构 §5.1）

```text
IDLE → RECORDING ⇄ PAUSED → FINALIZING → QUEUED → UPLOADING → VERIFYING → ACKED
                                                       ↓           ↓
                                                   RETRY_WAIT → UPLOADING
                                              任意失败 → UPLOAD_FAILED（保留本地文件）
ACKED → PURGE_PENDING → PURGED
```

实现要点：

- 状态只能通过 `RecordingState` 上的迁移函数前进（`uploader.rs` 里的 `UploadFsm`），
  不允许直接赋值目标状态 —— 否则文档里那张图就退化成注释了。
- 录的是**员工自己的麦克风**（`default_input_device`），不是 system loopback，
  也不是整场混音。
- 暂停不拆 cpal 流，只让回调丢样本，所以恢复是瞬时的，不用重新申请授权。
- 音频回调里不做文件 IO：回调只把字节丢进 channel，由专门的写盘线程落盘。
- 落盘的是**加密后**的 s16le raw PCM（见上面「落盘加密」），
  目录 `<appData>/local-recording/<meetingId>/<uploadId>/`，文件权限 `0600`；
  `meetingId` / `uploadId` 做过白名单过滤，防路径穿越。
- 上传前才解密成明文：每块 sha256 和整文件 sha256 **都按明文 PCM 算**，
  网关契约不变。解不开就进 `UPLOAD_FAILED` 并保留密文，不会传上去一段空文件。
- 未拿到服务端确认前**不删**本地文件；确认后也要显式调 `purge_local_recording`，
  它会把密文和临时明文一起删掉。

## 已知限制

- 审计会在 stop/purge 时尽力回传网关（`local_recording_*`）；失败可 `flush_recording_audit` 重试。进程崩溃仍可能丢未 flush 的缓冲。
- 落盘加密目前是 PoC：密钥从环境变量取，没有企业下发通道，也没有密钥轮换 /
  按会话派生子密钥；没设 `METUAI_SPOOL_KEY` 时用的是源码里的公开开发密钥。
- 写到一半崩溃留下的半个 GCM 帧，解密时直接报 `Truncated`，不做「尽力恢复前面几帧」。
- 客户端重启后**不会**自动恢复未完成的上传队列（spool 文件留着，但要手动重传）。
- `stop_and_upload_local_recording` 是同步命令，大文件上传期间会占住这次 invoke；
  前端应当自己做 loading 态。
- 网关只按 JWT 认员工身份，没有再校验「这个员工是否属于这场会」。
