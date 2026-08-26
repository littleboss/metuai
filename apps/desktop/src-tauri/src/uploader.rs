//! 分块上传：把 `ChunkSource` 推给网关，并驱动 UPLOADING / RETRY_WAIT / VERIFYING / ACKED。
//!
//! HTTP 被抽成 `ChunkSink` trait，所以驱动逻辑（重试、退避、状态迁移、整文件校验）
//! 可以用假实现完整单测，不用起真服务器、也不用真麦克风。

use crate::chunk::{sha256_hex, ChunkSource};
use crate::recording::RecordingState;
use std::fmt;
use std::time::Duration;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum UploadError {
    /// 连不上 / 断流：可重试。
    Transport(String),
    /// 服务端返回非 2xx。
    Status { code: u16, body: String },
    /// 服务端合并后的整文件 checksum 跟本地不一致 —— 数据坏了，重传也没用。
    Verify { expected: String, got: String },
    /// 读本地分块失败。
    Io(String),
    /// 重试次数用尽（附最后一次的原因）。
    Exhausted(String),
}

impl UploadError {
    /// 值得再试一次吗？
    /// 4xx 基本是契约/鉴权问题（401 过期、400 checksum 不符），重试只是刷日志；
    /// 408/429/5xx 是暂态，退避后重来。
    pub fn is_retryable(&self) -> bool {
        match self {
            Self::Transport(_) => true,
            Self::Status { code, .. } => *code == 408 || *code == 429 || *code >= 500,
            Self::Verify { .. } | Self::Io(_) | Self::Exhausted(_) => false,
        }
    }
}

impl fmt::Display for UploadError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Transport(m) => write!(f, "transport: {m}"),
            Self::Status { code, body } => write!(f, "http {code}: {body}"),
            Self::Verify { expected, got } => {
                write!(f, "checksum mismatch: expected {expected}, server {got}")
            }
            Self::Io(m) => write!(f, "io: {m}"),
            Self::Exhausted(m) => write!(f, "retries exhausted: {m}"),
        }
    }
}

impl std::error::Error for UploadError {}

/// 上传目的地。生产是网关 HTTP，测试是内存假实现。
pub trait ChunkSink: Send + Sync {
    /// 查询服务端已收到哪些分块（旧网关或不支持时可返回空）。
    fn status(&self) -> Result<RemoteUploadStatus, UploadError> {
        Ok(RemoteUploadStatus::default())
    }

    /// PUT 单块。`checksum` 是这块的 sha256 hex。
    /// 必须幂等：同一个 index 重传要能覆盖，断点续传依赖这点。
    fn put_chunk(&self, index: usize, checksum: &str, body: &[u8]) -> Result<(), UploadError>;

    /// POST complete，返回服务端合并后的整文件 sha256 与（可选）落点信息。
    fn complete(&self, parts: usize) -> Result<CompleteAck, UploadError>;
}

/// 重试策略。退避是线性递增（attempt × backoff），够用且好测。
#[derive(Debug, Clone)]
pub struct RetryPolicy {
    pub max_attempts: u32,
    pub backoff: Duration,
}

impl Default for RetryPolicy {
    fn default() -> Self {
        Self {
            max_attempts: 5,
            backoff: Duration::from_secs(2),
        }
    }
}

impl RetryPolicy {
    /// 测试用：不睡觉，试一次就够。
    #[cfg(test)]
    pub fn immediate(max_attempts: u32) -> Self {
        Self {
            max_attempts,
            backoff: Duration::ZERO,
        }
    }

    fn wait_for(&self, attempt: u32) -> Duration {
        self.backoff.saturating_mul(attempt)
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct UploadOutcome {
    pub parts: usize,
    pub checksum: String,
    /// 网关报告的落点：`s3` / `local_spool` / `local_spool_only`；旧网关可能为空。
    pub stored_in: String,
    pub object_key: String,
}

/// complete 接口的应答（checksum 必填；落点字段兼容旧网关）。
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct CompleteAck {
    pub checksum: String,
    pub stored_in: String,
    pub object_key: String,
}

/// GET status：已收到的分块索引，供断点续传跳过。
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct RemoteUploadStatus {
    pub received: Vec<usize>,
    pub finalized: bool,
    pub checksum: String,
}

/// 上传阶段的状态机游标。
///
/// 关键点：状态**只能**通过 `RecordingState` 上的迁移函数前进，不允许直接赋值。
/// 直接赋值的写法（`observer(RecordingState::Verifying)`）能编译、能跑，
/// 但架构文档里画的那张图就退化成注释了 —— 谁改错一步都没人拦。
struct UploadFsm<'a> {
    state: RecordingState,
    observer: &'a mut dyn FnMut(RecordingState),
}

impl UploadFsm<'_> {
    /// 走一步。迁移非法就直接报错，而不是悄悄跳到目标状态。
    fn advance(
        &mut self,
        step: fn(RecordingState) -> Option<RecordingState>,
    ) -> Result<(), UploadError> {
        let next = step(self.state).ok_or_else(|| {
            UploadError::Io(format!("illegal upload transition from {}", self.state.as_str()))
        })?;
        self.state = next;
        (self.observer)(next);
        Ok(())
    }

    /// 进 UPLOAD_FAILED。已经是终态就什么都不做（别把失败原因覆盖掉）。
    fn fail(&mut self) {
        if let Some(next) = self.state.fail() {
            self.state = next;
            (self.observer)(next);
        }
    }
}

/// 驱动整个上传流程，每次状态变化都回调 `observer`（上层拿去更新 UI / 审计）。
///
/// 流程（架构 §5.1）：
/// `QUEUED → UPLOADING →(逐块 PUT)→ VERIFYING →(比对整文件 checksum)→ ACKED`
/// 失败：`UPLOADING/VERIFYING → RETRY_WAIT → UPLOADING`，用尽或不可重试 → `UPLOAD_FAILED`。
///
/// 调用方进来时状态应当已经是 QUEUED（收尾落盘完成）。
/// UPLOAD_FAILED 之后本地文件必须由调用方保留。
pub fn drive_upload(
    src: &dyn ChunkSource,
    sink: &dyn ChunkSink,
    expected_whole_checksum: &str,
    policy: &RetryPolicy,
    observer: &mut dyn FnMut(RecordingState),
) -> Result<UploadOutcome, UploadError> {
    let parts = src.parts();
    let mut fsm = UploadFsm {
        state: RecordingState::Queued,
        observer,
    };

    if parts == 0 {
        // 一个字节都没录到就别去打扰网关了（complete 那边 parts<=0 也会 400）。
        fsm.fail();
        return Err(UploadError::Io("nothing recorded".into()));
    }

    fsm.advance(RecordingState::upload)?; // QUEUED -> UPLOADING

    // 断点续传：跳过服务端已经有的分块（中断后重试 / 进程重启后同 upload_id）。
    let remote = sink.status().unwrap_or_default();
    let already: std::collections::HashSet<usize> = remote.received.into_iter().collect();

    for index in 0..parts {
        if already.contains(&index) {
            continue;
        }
        let body = match src.chunk(index) {
            Ok(body) => body,
            Err(e) => {
                fsm.fail();
                return Err(UploadError::Io(e.to_string()));
            }
        };
        let checksum = sha256_hex(&body);
        with_retry(policy, &mut fsm, Phase::Chunks, || {
            sink.put_chunk(index, &checksum, &body)
        })?;
    }

    fsm.advance(RecordingState::verify)?; // UPLOADING -> VERIFYING
    let ack = with_retry(policy, &mut fsm, Phase::Verify, || sink.complete(parts))?;

    if ack.checksum != expected_whole_checksum {
        // 服务端合并结果和本地不一致：重传同样的字节也不会变对，直接失败。
        fsm.fail();
        return Err(UploadError::Verify {
            expected: expected_whole_checksum.to_string(),
            got: ack.checksum,
        });
    }

    fsm.advance(RecordingState::ack)?; // VERIFYING -> ACKED
    Ok(UploadOutcome {
        parts,
        checksum: ack.checksum,
        stored_in: ack.stored_in,
        object_key: ack.object_key,
    })
}

/// 重试发生在哪个阶段，决定退避结束后要回到哪儿。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Phase {
    Chunks,
    Verify,
}

/// 一次操作的重试包装。
///
/// 退避路径按架构文档走 `RETRY_WAIT -> UPLOADING`；如果失败发生在 VERIFYING 阶段，
/// 回到 UPLOADING 后再走一次 `verify`，而不是从 RETRY_WAIT 直接跳回 VERIFYING
/// （文档里没有那条边）。
fn with_retry<T>(
    policy: &RetryPolicy,
    fsm: &mut UploadFsm<'_>,
    phase: Phase,
    mut op: impl FnMut() -> Result<T, UploadError>,
) -> Result<T, UploadError> {
    let attempts = policy.max_attempts.max(1);
    let mut last = UploadError::Transport("no attempt".into());
    for attempt in 1..=attempts {
        match op() {
            Ok(v) => return Ok(v),
            Err(e) if e.is_retryable() && attempt < attempts => {
                last = e;
                fsm.advance(RecordingState::retry_wait)?; // UPLOADING/VERIFYING -> RETRY_WAIT
                let wait = policy.wait_for(attempt);
                if !wait.is_zero() {
                    std::thread::sleep(wait);
                }
                fsm.advance(RecordingState::upload)?; // RETRY_WAIT -> UPLOADING
                if phase == Phase::Verify {
                    fsm.advance(RecordingState::verify)?; // UPLOADING -> VERIFYING
                }
            }
            Err(e) => {
                // 不可重试，或最后一次也失败了
                fsm.fail();
                return Err(if e.is_retryable() {
                    UploadError::Exhausted(e.to_string())
                } else {
                    e
                });
            }
        }
    }
    fsm.fail();
    Err(UploadError::Exhausted(last.to_string()))
}

/// 真网关实现。
///
/// - `PUT  {base}/v1/meetings/{id}/local-recording/{uploadId}/chunks/{index}`
/// - `POST {base}/v1/meetings/{id}/local-recording/{uploadId}/complete`
///
/// JWT 只往 Authorization 头里放，绝不进日志（架构 §5.3：录音与凭据不入日志/遥测）。
pub struct GatewaySink {
    base_url: String,
    meeting_id: String,
    upload_id: String,
    employee_jwt: String,
    agent: ureq::Agent,
}

impl GatewaySink {
    pub fn new(
        base_url: impl Into<String>,
        meeting_id: impl Into<String>,
        upload_id: impl Into<String>,
        employee_jwt: impl Into<String>,
    ) -> Self {
        let agent = ureq::AgentBuilder::new()
            .timeout_connect(Duration::from_secs(10))
            .timeout(Duration::from_secs(60))
            .build();
        Self {
            base_url: base_url.into().trim_end_matches('/').to_string(),
            meeting_id: meeting_id.into(),
            upload_id: upload_id.into(),
            employee_jwt: employee_jwt.into(),
            agent,
        }
    }

    fn chunk_url(&self, index: usize) -> String {
        format!(
            "{}/v1/meetings/{}/local-recording/{}/chunks/{}",
            self.base_url,
            urlencoding(&self.meeting_id),
            urlencoding(&self.upload_id),
            index
        )
    }

    fn complete_url(&self) -> String {
        format!(
            "{}/v1/meetings/{}/local-recording/{}/complete",
            self.base_url,
            urlencoding(&self.meeting_id),
            urlencoding(&self.upload_id),
        )
    }

    fn status_url(&self) -> String {
        format!(
            "{}/v1/meetings/{}/local-recording/{}/status",
            self.base_url,
            urlencoding(&self.meeting_id),
            urlencoding(&self.upload_id),
        )
    }

    fn bearer(&self) -> String {
        format!("Bearer {}", self.employee_jwt)
    }

    /// 把本机录音审计批量回传网关（架构 §5.2）。失败由调用方决定是否重试。
    pub fn post_audit_events(
        &self,
        events: &[(String, String)],
    ) -> Result<usize, UploadError> {
        if events.is_empty() {
            return Ok(0);
        }
        let url = format!(
            "{}/v1/meetings/{}/local-recording/audit",
            self.base_url,
            urlencoding(&self.meeting_id),
        );
        let payload: Vec<serde_json::Value> = events
            .iter()
            .map(|(action, detail)| {
                ureq::json!({
                    "action": action,
                    "detail": detail,
                })
            })
            .collect();
        let response = self
            .agent
            .post(&url)
            .set("Authorization", &self.bearer())
            .set("X-Metuai-Client", "tauri")
            .send_json(ureq::json!({ "events": payload }))
            .map_err(map_ureq_error)?;
        let parsed: serde_json::Value = response
            .into_json()
            .map_err(|e| UploadError::Transport(e.to_string()))?;
        Ok(parsed
            .get("accepted")
            .and_then(|v| v.as_u64())
            .unwrap_or(events.len() as u64) as usize)
    }
}

/// 把 ureq 的错误映射成我们的分类；顺手保证错误串里不会带上 JWT。
fn map_ureq_error(err: ureq::Error) -> UploadError {
    match err {
        ureq::Error::Status(code, response) => {
            let body = response
                .into_string()
                .unwrap_or_else(|_| "<unreadable body>".into());
            UploadError::Status { code, body }
        }
        ureq::Error::Transport(t) => UploadError::Transport(t.to_string()),
    }
}

impl ChunkSink for GatewaySink {
    fn status(&self) -> Result<RemoteUploadStatus, UploadError> {
        let response = self
            .agent
            .get(&self.status_url())
            .set("Authorization", &self.bearer())
            .set("X-Metuai-Client", "tauri")
            .call()
            .map_err(map_ureq_error)?;
        let parsed: serde_json::Value = response
            .into_json()
            .map_err(|e| UploadError::Transport(e.to_string()))?;
        let received = parsed
            .get("received")
            .and_then(|v| v.as_array())
            .map(|arr| {
                arr.iter()
                    .filter_map(|x| x.as_u64().map(|n| n as usize))
                    .collect()
            })
            .unwrap_or_default();
        Ok(RemoteUploadStatus {
            received,
            finalized: parsed
                .get("finalized")
                .and_then(|v| v.as_bool())
                .unwrap_or(false),
            checksum: parsed
                .get("checksum")
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string(),
        })
    }

    fn put_chunk(&self, index: usize, checksum: &str, body: &[u8]) -> Result<(), UploadError> {
        self.agent
            .put(&self.chunk_url(index))
            .set("Authorization", &self.bearer())
            .set("X-Checksum-Sha256", checksum)
            .set("X-Metuai-Client", "tauri")
            .set("Content-Type", "application/octet-stream")
            .send_bytes(body)
            .map(|_| ())
            .map_err(map_ureq_error)
    }

    fn complete(&self, parts: usize) -> Result<CompleteAck, UploadError> {
        let response = self
            .agent
            .post(&self.complete_url())
            .set("Authorization", &self.bearer())
            .set("X-Metuai-Client", "tauri")
            .send_json(ureq::json!({ "parts": parts }))
            .map_err(map_ureq_error)?;
        let parsed: serde_json::Value = response
            .into_json()
            .map_err(|e| UploadError::Transport(e.to_string()))?;
        let checksum = parsed
            .get("checksum")
            .and_then(|v| v.as_str())
            .map(|s| s.to_string())
            .ok_or_else(|| UploadError::Transport("complete response missing checksum".into()))?;
        Ok(CompleteAck {
            checksum,
            stored_in: parsed
                .get("stored_in")
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string(),
            object_key: parsed
                .get("object_key")
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string(),
        })
    }
}

/// 极简 path segment 转义。meetingId / uploadId 是我们自己生成的 uuid 风格串，
/// 这里只兜底防止意外字符把 URL 撑破。
fn urlencoding(segment: &str) -> String {
    segment
        .chars()
        .flat_map(|c| {
            if c.is_ascii_alphanumeric() || matches!(c, '-' | '_' | '.' | '~') {
                vec![c]
            } else {
                format!("%{:02X}", c as u32 as u8).chars().collect()
            }
        })
        .collect()
}

#[cfg(test)]
pub(crate) mod testing {
    use super::*;
    use std::collections::HashMap;
    use std::sync::Mutex;

    /// 假网关：像真服务端那样按 index 存块、校验 checksum、合并后重算整文件 sha256。
    /// 还能被指定「前 N 次调用故意失败」，用来测重试路径。
    pub struct FakeSink {
        pub stored: Mutex<HashMap<usize, Vec<u8>>>,
        pub put_calls: Mutex<usize>,
        pub complete_calls: Mutex<usize>,
        fail_puts: Mutex<Vec<UploadError>>,
        fail_completes: Mutex<Vec<UploadError>>,
        corrupt_final: bool,
    }

    impl FakeSink {
        pub fn new() -> Self {
            Self {
                stored: Mutex::new(HashMap::new()),
                put_calls: Mutex::new(0),
                complete_calls: Mutex::new(0),
                fail_puts: Mutex::new(Vec::new()),
                fail_completes: Mutex::new(Vec::new()),
                corrupt_final: false,
            }
        }

        /// 前面几次 put 依次返回这些错误（消费完就开始成功）。
        pub fn failing_puts(mut errors: Vec<UploadError>) -> Self {
            let sink = Self::new();
            errors.reverse(); // pop 从尾部取，反转后就是先进先出
            *sink.fail_puts.lock().unwrap() = errors;
            sink
        }

        pub fn failing_completes(mut errors: Vec<UploadError>) -> Self {
            let sink = Self::new();
            errors.reverse();
            *sink.fail_completes.lock().unwrap() = errors;
            sink
        }

        /// complete 返回一个错的 checksum，模拟服务端数据损坏。
        pub fn corrupting() -> Self {
            let mut sink = Self::new();
            sink.corrupt_final = true;
            sink
        }

        /// 按 index 顺序拼回完整数据。
        pub fn merged(&self) -> Vec<u8> {
            let stored = self.stored.lock().unwrap();
            let mut keys: Vec<_> = stored.keys().copied().collect();
            keys.sort_unstable();
            keys.iter().flat_map(|k| stored[k].clone()).collect()
        }
    }

    impl ChunkSink for FakeSink {
        fn status(&self) -> Result<RemoteUploadStatus, UploadError> {
            let stored = self.stored.lock().unwrap();
            let mut received: Vec<usize> = stored.keys().copied().collect();
            received.sort_unstable();
            Ok(RemoteUploadStatus {
                received,
                finalized: false,
                checksum: String::new(),
            })
        }

        fn put_chunk(&self, index: usize, checksum: &str, body: &[u8]) -> Result<(), UploadError> {
            *self.put_calls.lock().unwrap() += 1;
            if let Some(err) = self.fail_puts.lock().unwrap().pop() {
                return Err(err);
            }
            // 真网关会校验，这里也校验，免得客户端算错了测试还绿
            if sha256_hex(body) != checksum {
                return Err(UploadError::Status {
                    code: 400,
                    body: "checksum_mismatch".into(),
                });
            }
            self.stored.lock().unwrap().insert(index, body.to_vec());
            Ok(())
        }

        fn complete(&self, parts: usize) -> Result<CompleteAck, UploadError> {
            *self.complete_calls.lock().unwrap() += 1;
            if let Some(err) = self.fail_completes.lock().unwrap().pop() {
                return Err(err);
            }
            let stored = self.stored.lock().unwrap();
            for i in 0..parts {
                if !stored.contains_key(&i) {
                    return Err(UploadError::Status {
                        code: 400,
                        body: format!("missing_part_{i}"),
                    });
                }
            }
            drop(stored);
            let checksum = if self.corrupt_final {
                sha256_hex(b"not what the client sent")
            } else {
                sha256_hex(&self.merged())
            };
            Ok(CompleteAck {
                checksum,
                stored_in: "local_spool".into(),
                object_key: String::new(),
            })
        }
    }
}

#[cfg(test)]
mod tests {
    use super::testing::FakeSink;
    use super::*;
    use crate::chunk::MemoryChunks;

    fn pcm(len: usize) -> Vec<u8> {
        // 假 PCM：不需要真麦克风也能测完整上传链路
        (0..len).map(|i| (i % 256) as u8).collect()
    }

    #[test]
    fn happy_path_uploads_every_chunk_and_acks() {
        let data = pcm(1000);
        let src = MemoryChunks::new(data.clone(), 256);
        let sink = FakeSink::new();
        let expected = sha256_hex(&data);
        let mut states = Vec::new();

        let outcome = drive_upload(
            &src,
            &sink,
            &expected,
            &RetryPolicy::immediate(3),
            &mut |s| states.push(s),
        )
        .expect("upload should succeed");

        assert_eq!(outcome.parts, 4);
        assert_eq!(outcome.checksum, expected);
        assert_eq!(*sink.put_calls.lock().unwrap(), 4);
        assert_eq!(sink.merged(), data, "服务端合并后必须字节级一致");
        assert_eq!(
            states,
            vec![
                RecordingState::Uploading,
                RecordingState::Verifying,
                RecordingState::Acked
            ]
        );
    }

    #[test]
    fn resume_skips_chunks_already_on_server() {
        let data = pcm(800);
        let src = MemoryChunks::new(data.clone(), 256);
        let sink = FakeSink::new();
        let first = src.chunk(0).unwrap();
        sink.put_chunk(0, &sha256_hex(&first), &first).unwrap();
        *sink.put_calls.lock().unwrap() = 0;

        let expected = sha256_hex(&data);
        let outcome = drive_upload(
            &src,
            &sink,
            &expected,
            &RetryPolicy::immediate(1),
            &mut |_| {},
        )
        .expect("resume upload");

        assert_eq!(outcome.checksum, expected);
        assert_eq!(*sink.put_calls.lock().unwrap(), src.parts() - 1);
    }

    #[test]
    fn transport_failure_goes_through_retry_wait_then_succeeds() {
        let data = pcm(300);
        let src = MemoryChunks::new(data.clone(), 256);
        let sink = FakeSink::failing_puts(vec![UploadError::Transport("connection reset".into())]);
        let expected = sha256_hex(&data);
        let mut states = Vec::new();

        let outcome = drive_upload(
            &src,
            &sink,
            &expected,
            &RetryPolicy::immediate(3),
            &mut |s| states.push(s),
        )
        .expect("retry should recover");

        assert_eq!(outcome.parts, 2);
        assert_eq!(*sink.put_calls.lock().unwrap(), 3, "第一块失败一次后重试");
        assert!(
            states.contains(&RecordingState::RetryWait),
            "必须经过 RETRY_WAIT，实际: {states:?}"
        );
        assert_eq!(states.last(), Some(&RecordingState::Acked));
        // RETRY_WAIT 之后要回到 UPLOADING 再继续
        let idx = states.iter().position(|s| *s == RecordingState::RetryWait).unwrap();
        assert_eq!(states[idx + 1], RecordingState::Uploading);
    }

    #[test]
    fn server_5xx_is_retried() {
        let data = pcm(100);
        let src = MemoryChunks::new(data.clone(), 256);
        let sink = FakeSink::failing_puts(vec![UploadError::Status {
            code: 503,
            body: "unavailable".into(),
        }]);
        let mut states = Vec::new();
        let out = drive_upload(
            &src,
            &sink,
            &sha256_hex(&data),
            &RetryPolicy::immediate(3),
            &mut |s| states.push(s),
        );
        assert!(out.is_ok(), "5xx 应该重试成功: {out:?}");
    }

    #[test]
    fn auth_failure_is_not_retried() {
        let data = pcm(100);
        let src = MemoryChunks::new(data.clone(), 256);
        let sink = FakeSink::failing_puts(vec![
            UploadError::Status {
                code: 401,
                body: "token expired".into(),
            },
            UploadError::Status {
                code: 401,
                body: "token expired".into(),
            },
        ]);
        let mut states = Vec::new();
        let err = drive_upload(
            &src,
            &sink,
            &sha256_hex(&data),
            &RetryPolicy::immediate(5),
            &mut |s| states.push(s),
        )
        .expect_err("401 必须直接失败");

        assert!(matches!(err, UploadError::Status { code: 401, .. }));
        assert_eq!(*sink.put_calls.lock().unwrap(), 1, "401 不该重试");
        assert_eq!(states.last(), Some(&RecordingState::UploadFailed));
    }

    #[test]
    fn retries_are_exhausted_and_reported() {
        let data = pcm(100);
        let src = MemoryChunks::new(data.clone(), 256);
        let sink = FakeSink::failing_puts(vec![
            UploadError::Transport("boom".into()),
            UploadError::Transport("boom".into()),
        ]);
        let mut states = Vec::new();
        let err = drive_upload(
            &src,
            &sink,
            &sha256_hex(&data),
            &RetryPolicy::immediate(2),
            &mut |s| states.push(s),
        )
        .expect_err("两次都失败且上限为 2");

        assert!(matches!(err, UploadError::Exhausted(_)), "得到 {err:?}");
        assert_eq!(states.last(), Some(&RecordingState::UploadFailed));
    }

    #[test]
    fn complete_failure_retries_from_verifying() {
        let data = pcm(300);
        let src = MemoryChunks::new(data.clone(), 256);
        let sink = FakeSink::failing_completes(vec![UploadError::Status {
            code: 500,
            body: "merge failed".into(),
        }]);
        let mut states = Vec::new();
        drive_upload(
            &src,
            &sink,
            &sha256_hex(&data),
            &RetryPolicy::immediate(3),
            &mut |s| states.push(s),
        )
        .expect("complete 重试后应成功");

        assert_eq!(*sink.complete_calls.lock().unwrap(), 2);
        // 架构文档只有 RETRY_WAIT -> UPLOADING 这条边，所以 VERIFYING 阶段失败后
        // 必须绕回 UPLOADING 再重新 verify，不能从 RETRY_WAIT 直接跳回 VERIFYING。
        assert_eq!(
            states,
            vec![
                RecordingState::Uploading,
                RecordingState::Verifying,
                RecordingState::RetryWait,
                RecordingState::Uploading,
                RecordingState::Verifying,
                RecordingState::Acked,
            ]
        );
    }

    #[test]
    fn every_observed_state_is_a_legal_transition() {
        // 观察到的状态序列必须能被状态机自己走出来 —— 这条测试是防止有人
        // 以后又改回「直接赋值目标状态」的写法。
        let data = pcm(600);
        let src = MemoryChunks::new(data.clone(), 256);
        let sink = FakeSink::failing_puts(vec![UploadError::Transport("reset".into())]);
        let mut states = Vec::new();
        drive_upload(
            &src,
            &sink,
            &sha256_hex(&data),
            &RetryPolicy::immediate(3),
            &mut |s| states.push(s),
        )
        .unwrap();

        let all = [
            RecordingState::start,
            RecordingState::pause,
            RecordingState::resume,
            RecordingState::finalize,
            RecordingState::queue,
            RecordingState::upload,
            RecordingState::verify,
            RecordingState::ack,
            RecordingState::retry_wait,
            RecordingState::fail,
        ];
        let mut current = RecordingState::Queued;
        for observed in &states {
            let reachable = all.iter().any(|step| step(current) == Some(*observed));
            assert!(
                reachable,
                "从 {} 到 {} 不是状态机允许的迁移",
                current.as_str(),
                observed.as_str()
            );
            current = *observed;
        }
        assert_eq!(current, RecordingState::Acked);
    }

    #[test]
    fn checksum_mismatch_fails_without_retry() {
        let data = pcm(300);
        let src = MemoryChunks::new(data.clone(), 256);
        let sink = FakeSink::corrupting();
        let mut states = Vec::new();
        let err = drive_upload(
            &src,
            &sink,
            &sha256_hex(&data),
            &RetryPolicy::immediate(3),
            &mut |s| states.push(s),
        )
        .expect_err("整文件 checksum 不符必须失败");

        assert!(matches!(err, UploadError::Verify { .. }));
        assert_eq!(*sink.complete_calls.lock().unwrap(), 1);
        assert_eq!(states.last(), Some(&RecordingState::UploadFailed));
    }

    #[test]
    fn empty_recording_is_rejected_locally() {
        let src = MemoryChunks::new(Vec::new(), 256);
        let sink = FakeSink::new();
        let mut states = Vec::new();
        let err = drive_upload(&src, &sink, "", &RetryPolicy::immediate(3), &mut |s| {
            states.push(s)
        })
        .expect_err("0 字节不该发请求");
        assert!(matches!(err, UploadError::Io(_)));
        assert_eq!(*sink.put_calls.lock().unwrap(), 0);
    }

    #[test]
    fn error_classification() {
        assert!(UploadError::Transport("x".into()).is_retryable());
        assert!(UploadError::Status { code: 500, body: String::new() }.is_retryable());
        assert!(UploadError::Status { code: 429, body: String::new() }.is_retryable());
        assert!(!UploadError::Status { code: 400, body: String::new() }.is_retryable());
        assert!(!UploadError::Status { code: 404, body: String::new() }.is_retryable());
        assert!(!UploadError::Verify {
            expected: "a".into(),
            got: "b".into()
        }
        .is_retryable());
    }

    #[test]
    fn gateway_urls_match_contract() {
        let sink = GatewaySink::new("http://127.0.0.1:8080/", "m-1", "u-9", "jwt-token");
        assert_eq!(
            sink.chunk_url(3),
            "http://127.0.0.1:8080/v1/meetings/m-1/local-recording/u-9/chunks/3"
        );
        assert_eq!(
            sink.complete_url(),
            "http://127.0.0.1:8080/v1/meetings/m-1/local-recording/u-9/complete"
        );
    }

    #[test]
    fn path_segments_are_escaped() {
        let sink = GatewaySink::new("http://h", "a/../b", "u 1", "jwt");
        let url = sink.chunk_url(0);
        assert!(
            !url.contains("a/../b"),
            "路径穿越必须被转义: {url}"
        );
        assert!(url.contains("u%201"));
    }
}

/// 真 HTTP 路径的测试。
///
/// 上面那些用 `FakeSink` 的测试验的是**驱动逻辑**；这里验的是 `GatewaySink` 真的
/// 按网关契约发请求 —— header 名字写错、JSON 字段读错这类问题，假实现是抓不到的。
/// 用一个手写的极简 HTTP 服务端，不引第三方 mock 依赖。
#[cfg(test)]
mod http_tests {
    use super::*;
    use std::io::{BufRead, BufReader, Read, Write};
    use std::net::TcpListener;
    use std::thread::JoinHandle;

    /// 服务端收到的那一个请求。
    struct Captured {
        method: String,
        path: String,
        headers: Vec<(String, String)>,
        body: Vec<u8>,
    }

    impl Captured {
        fn header(&self, name: &str) -> Option<&str> {
            self.headers
                .iter()
                .find(|(k, _)| k.eq_ignore_ascii_case(name))
                .map(|(_, v)| v.as_str())
        }
    }

    fn http_response(status: &str, body: &str) -> String {
        // Content-Length 必须算准，否则客户端会一直等着读不完的 body
        format!(
            "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
            body.len()
        )
    }

    /// 起一个只服务一个请求就退出的服务端，返回 base_url 和拿回请求内容的 handle。
    fn serve_once(response: String) -> (String, JoinHandle<Captured>) {
        // 端口给 0 让内核挑一个空闲端口，测试并行跑也不会撞
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
        let addr = listener.local_addr().expect("addr");
        let handle = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().expect("accept");
            let mut reader = BufReader::new(stream.try_clone().expect("clone"));

            let mut request_line = String::new();
            reader.read_line(&mut request_line).expect("request line");
            let mut parts = request_line.split_whitespace();
            let method = parts.next().unwrap_or_default().to_string();
            let path = parts.next().unwrap_or_default().to_string();

            let mut headers = Vec::new();
            let mut content_length = 0usize;
            loop {
                let mut line = String::new();
                reader.read_line(&mut line).expect("header line");
                // 空行 = header 结束
                if line.trim().is_empty() {
                    break;
                }
                if let Some((key, value)) = line.split_once(':') {
                    let key = key.trim().to_string();
                    let value = value.trim().to_string();
                    if key.eq_ignore_ascii_case("content-length") {
                        content_length = value.parse().unwrap_or(0);
                    }
                    headers.push((key, value));
                }
            }

            let mut body = vec![0u8; content_length];
            if content_length > 0 {
                reader.read_exact(&mut body).expect("body");
            }

            stream.write_all(response.as_bytes()).expect("write response");
            stream.flush().ok();
            Captured {
                method,
                path,
                headers,
                body,
            }
        });
        (format!("http://{addr}"), handle)
    }

    #[test]
    fn put_chunk_matches_the_gateway_contract() {
        let (base, server) = serve_once(http_response("200 OK", r#"{"ok":true,"index":3}"#));
        let sink = GatewaySink::new(base, "mtg_1", "up_7", "jwt-token");
        let payload = vec![7u8; 1024];
        let checksum = sha256_hex(&payload);

        sink.put_chunk(3, &checksum, &payload).expect("put ok");
        let req = server.join().expect("server thread");

        assert_eq!(req.method, "PUT");
        assert_eq!(req.path, "/v1/meetings/mtg_1/local-recording/up_7/chunks/3");
        assert_eq!(req.header("Authorization"), Some("Bearer jwt-token"));
        // 网关按这个头校验分块完整性（http.go 里的 X-Checksum-Sha256）
        assert_eq!(req.header("X-Checksum-Sha256"), Some(checksum.as_str()));
        assert_eq!(req.header("X-Metuai-Client"), Some("tauri"));
        assert_eq!(req.body, payload, "body 必须是原始字节，不能被转码");
    }

    #[test]
    fn complete_sends_parts_and_reads_back_checksum() {
        let (base, server) = serve_once(http_response(
            "200 OK",
            r#"{"ok":true,"checksum":"abc123","stored_in":"s3","object_key":"metuai-media/x.bin"}"#,
        ));
        let sink = GatewaySink::new(base, "mtg_1", "up_7", "jwt-token");

        let got = sink.complete(9).expect("complete ok");
        let req = server.join().expect("server thread");

        assert_eq!(got.checksum, "abc123", "要读服务端合并后的整文件 checksum");
        assert_eq!(got.stored_in, "s3");
        assert_eq!(got.object_key, "metuai-media/x.bin");
        assert_eq!(req.method, "POST");
        assert_eq!(req.path, "/v1/meetings/mtg_1/local-recording/up_7/complete");
        assert_eq!(req.header("Authorization"), Some("Bearer jwt-token"));
        let body: serde_json::Value = serde_json::from_slice(&req.body).expect("json body");
        assert_eq!(body["parts"], 9);
    }

    #[test]
    fn expired_token_surfaces_as_non_retryable_401() {
        let (base, server) = serve_once(http_response("401 Unauthorized", r#"{"error":"expired"}"#));
        let sink = GatewaySink::new(base, "mtg_1", "up_7", "stale-token");

        let err = sink.put_chunk(0, &sha256_hex(b"x"), b"x").expect_err("401");
        server.join().ok();

        match err {
            UploadError::Status { code, ref body } => {
                assert_eq!(code, 401);
                assert!(body.contains("expired"), "要保留服务端的原因: {body}");
            }
            other => panic!("expected 401 status, got {other:?}"),
        }
        assert!(!err.is_retryable(), "token 过期重试也没用");
    }

    #[test]
    fn missing_checksum_in_response_is_an_error() {
        // 服务端回了 200 但没带 checksum：不能当成上传成功
        let (base, server) = serve_once(http_response("200 OK", r#"{"ok":true}"#));
        let sink = GatewaySink::new(base, "mtg_1", "up_7", "jwt-token");

        let err = sink.complete(1).expect_err("没有 checksum 就不算成功");
        server.join().ok();
        assert!(matches!(err, UploadError::Transport(_)), "得到 {err:?}");
    }

    #[test]
    fn connection_refused_is_retryable() {
        // 绑一个端口再立刻放掉，拿到一个几乎肯定没人监听的地址
        let base = {
            let listener = TcpListener::bind("127.0.0.1:0").unwrap();
            format!("http://{}", listener.local_addr().unwrap())
        };
        let sink = GatewaySink::new(base, "mtg_1", "up_7", "jwt-token");
        let err = sink.put_chunk(0, &sha256_hex(b"x"), b"x").expect_err("连不上");
        assert!(
            err.is_retryable(),
            "网关暂时连不上应该退避重试，而不是直接判死: {err:?}"
        );
    }
}
