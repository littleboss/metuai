# Metuai post-meeting worker (Python)

会后流水线 Worker：媒体 → ASR →（假）纪要 → Vespa（未接）。

## 当前状态

| 能力 | 状态 |
|---|---|
| `POST .../pipeline/run-fake` | ✅ 整条推到 `READY` |
| `POST .../pipeline/tasks/claim` | ✅ 领取会后任务（租约），失败可重试 / 死信 |
| `POST .../pipeline/asr-result` | ✅ Worker 提交转写 → `TRANSCRIPT_READY` |
| ASR stub | ✅ 默认，不装 funasr |
| FunASR | ⚙️ 可选：`ASR_BACKEND=funasr` + `pip install funasr` |

## 用法

```bash
export GATEWAY_URL=http://127.0.0.1:18080
export EMPLOYEE_JWT="$(cd ../../services/gateway && go run ./cmd/devtoken)"
cd services/worker

# 假流水线（纪要+转写全假）
python3 -m worker.main --mode fake --meeting mtg_xxxxxxxx

# 从任务表领取一条（会议结束后网关会入队）
python3 -m worker.main --claim --once


# 真 FunASR
pip install funasr  # 可选依赖，体积大
ASR_BACKEND=funasr FUNASR_MODEL=paraformer-zh \
  python3 -m worker.main --mode asr --meeting mtg_xxxxxxxx --audio /path/to.wav
```

若未传 `--audio`，Worker 会按人挑选权威音源：`participant_track` 优先，缺轨再用该员工 `local_mic`。
房间混音不会进入 ASR。若全场都没有权威音源，Worker 会把会议标为 `MANUAL_REVIEW`。

## 环境变量

| 变量 | 含义 |
|---|---|
| `WORKER_TOKEN` | 可选，Worker 回调共享密钥（与员工 JWT 二选一） |
| `WORKER_OWNER` | 租约持有者名，默认 `worker` |
| `ASR_BACKEND` | `stub`（默认）或 `funasr` |
| `FUNASR_MODEL` | 默认 `paraformer-zh` |
| `S3_UPLOAD_ENDPOINT` / `S3_*` | 可选，拉 MinIO 媒体 |

## 计划

- Dapr Pub/Sub 订阅 `meeting_ended`
- 真纪要 LLM、embedding、Vespa ACL
