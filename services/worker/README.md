# Metuai post-meeting worker (Python)

会后流水线 Worker：媒体 → ASR →（假）纪要 → Vespa（未接）。

## 当前状态

| 能力 | 状态 |
|---|---|
| `POST .../pipeline/run-fake` | ✅ 整条推到 `READY` |
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

# stub ASR（会议须已结束；需有独立音轨/本机兜底，或显式传 --audio）
python3 -m worker.main --mode asr --meeting mtg_xxxxxxxx

# 真 FunASR
pip install funasr  # 可选依赖，体积大
ASR_BACKEND=funasr FUNASR_MODEL=paraformer-zh \
  python3 -m worker.main --mode asr --meeting mtg_xxxxxxxx --audio /path/to.wav
```

若未传 `--audio`，Worker 会尝试用 boto3 从 MinIO 拉 `local_mic` / `room_audio`；
失败则 stub 仍可提交占位转写（日志会标明 `backend=stub`）。

## 环境变量

| 变量 | 含义 |
|---|---|
| `ASR_BACKEND` | `stub`（默认）或 `funasr` |
| `FUNASR_MODEL` | 默认 `paraformer-zh` |
| `S3_UPLOAD_ENDPOINT` / `S3_*` | 可选，拉 MinIO 媒体 |

## 计划

- Dapr Pub/Sub 订阅 `meeting_ended`
- 真纪要 LLM、embedding、Vespa ACL
