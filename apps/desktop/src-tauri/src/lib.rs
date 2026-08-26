mod capture;
mod chunk;
mod queue;
mod recording;
mod session;
mod spool_crypto;
mod uploader;

use chunk::{sha256_file, ChunkSource, FileChunks};
use queue::PendingUpload;
use recording::{AuditEntry, RecordingState};
use session::{default_spool_dir, LocalRecordingSession, MicStatus};
use std::path::PathBuf;
use std::sync::Mutex;
use tauri::{AppHandle, Manager, State};
use uploader::{drive_upload, GatewaySink, RetryPolicy};

#[derive(Clone)]
struct UploadCreds {
    meeting_id: String,
    gateway_base: String,
    employee_jwt: String,
    upload_id: String,
}

/// 全局状态：一个录音状态机 + 至多一个进行中的会话。
///
/// 几把独立的锁，而不是把它们塞进一个大锁：上传过程会反复更新状态机（几十次），
/// 如果和会话共用一把锁，前端查状态时就得排队等上传。
struct AppState {
    recording: Mutex<RecordingState>,
    session: Mutex<Option<LocalRecordingSession>>,
    /// 审计事件（架构 §5.2：开始/暂停/恢复/停止/上传/确认/删除都要写）。
    /// 放在 AppState 而不是会话里，因为会话收尾时会被取走，审计要活得更久。
    audit: Mutex<Vec<AuditEntry>>,
    /// 已经回传网关的审计条数（从队头算起）；flush 只发还没传过的。
    audit_flushed: Mutex<usize>,
    /// 最近一次会话的网关凭证，会话结束后仍可用于 flush / purge 审计。
    last_creds: Mutex<Option<UploadCreds>>,
    /// 已 ACKED、等管理员策略决定删不删的本地文件（密文 + 上传时解出来的临时明文）。
    /// 架构 §5.2：没拿到确认前不能删，拿到确认后也要显式走 PURGE 才删。
    settled: Mutex<Option<Vec<PathBuf>>>,
    /// 应用数据目录（待传队列落盘用）；首次 start 录音时写入。
    app_data: Mutex<Option<PathBuf>>,
}

impl AppState {
    fn new() -> Self {
        Self {
            recording: Mutex::new(RecordingState::Idle),
            session: Mutex::new(None),
            audit: Mutex::new(Vec::new()),
            audit_flushed: Mutex::new(0),
            last_creds: Mutex::new(None),
            settled: Mutex::new(None),
            app_data: Mutex::new(None),
        }
    }

    fn set_app_data(&self, dir: PathBuf) {
        if let Ok(mut g) = self.app_data.lock() {
            *g = Some(dir);
        }
    }

    fn app_data_path(&self) -> Result<PathBuf, String> {
        self.app_data
            .lock()
            .map_err(|e| e.to_string())?
            .clone()
            .ok_or_else(|| "app_data_unknown".to_string())
    }

    /// 追加一条审计事件。审计写不进去不该让录音本身失败，所以这里吞掉锁错误。
    fn log(&self, action: &'static str, detail: impl Into<String>) {
        if let Ok(mut log) = self.audit.lock() {
            log.push(AuditEntry::new(action, detail));
        }
    }

    fn remember_creds(
        &self,
        meeting_id: &str,
        gateway_base: &str,
        employee_jwt: &str,
        upload_id: &str,
    ) {
        if let Ok(mut guard) = self.last_creds.lock() {
            *guard = Some(UploadCreds {
                meeting_id: meeting_id.to_string(),
                gateway_base: gateway_base.to_string(),
                employee_jwt: employee_jwt.to_string(),
                upload_id: upload_id.to_string(),
            });
        }
    }

    /// 把尚未回传的审计推给网关；成功则推进 flushed 游标。
    fn flush_audit_to_gateway(&self) -> Result<usize, String> {
        let creds = self
            .last_creds
            .lock()
            .map_err(|e| e.to_string())?
            .clone()
            .ok_or_else(|| "no_gateway_creds".to_string())?;
        let flushed = *self.audit_flushed.lock().map_err(|e| e.to_string())?;
        let pending: Vec<(String, String)> = {
            let log = self.audit.lock().map_err(|e| e.to_string())?;
            log.iter()
                .skip(flushed)
                .map(|e| (e.action.to_string(), e.detail.clone()))
                .collect()
        };
        if pending.is_empty() {
            return Ok(0);
        }
        let sink = GatewaySink::new(
            creds.gateway_base,
            creds.meeting_id,
            creds.upload_id,
            creds.employee_jwt,
        );
        let accepted = sink
            .post_audit_events(&pending)
            .map_err(|e| e.to_string())?;
        *self.audit_flushed.lock().map_err(|e| e.to_string())? += accepted;
        Ok(accepted)
    }
}

/// 审计事件的前端视图。`SystemTime` 不好直接 serde，转成 epoch 毫秒。
#[derive(serde::Serialize)]
struct AuditRecord {
    action: String,
    detail: String,
    at_ms: u128,
}

impl From<&AuditEntry> for AuditRecord {
    fn from(entry: &AuditEntry) -> Self {
        Self {
            action: entry.action.to_string(),
            detail: entry.detail.clone(),
            at_ms: entry
                .at
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_millis())
                .unwrap_or(0),
        }
    }
}

#[derive(serde::Serialize)]
struct StartResult {
    state: String,
    upload_id: String,
    /// 录音落盘的目录，上传失败时前端要把它显示给用户。
    spool_dir: String,
    /// 麦克风开没开起来；没开起来时 `error` 说明原因，前端可以提示用户授权。
    mic: MicStatus,
}

#[derive(serde::Serialize)]
struct StopResult {
    state: String,
    parts: usize,
    /// 服务端合并后回报的整文件 sha256，已与本地校验一致。
    checksum: String,
    bytes: u64,
    /// 本轮顺带回传网关的审计条数（失败则为 0，可再调 flush_recording_audit）。
    audit_flushed: usize,
    /// 网关落点：`s3` / `local_spool` / `local_spool_only`（旧网关可能为空）。
    stored_in: String,
    object_key: String,
}

#[tauri::command]
fn client_kind() -> String {
    "tauri".into()
}

#[tauri::command]
fn recording_state(state: State<'_, AppState>) -> String {
    state
        .recording
        .lock()
        .expect("recording lock")
        .as_str()
        .to_string()
}

/// 开始录音：建 spool 文件 → 开麦 → 状态机进 RECORDING。
///
/// 开麦失败**不算致命**：会话照样建起来，前端可以退回用 `append_local_pcm`
/// 手动喂 PCM（比如 WebAudio 采集，或者降级构建）。失败原因通过返回值告诉前端。
#[tauri::command]
fn start_local_recording(
    app: AppHandle,
    state: State<'_, AppState>,
    meeting_id: String,
    gateway_base_url: String,
    employee_jwt: String,
) -> Result<StartResult, String> {
    let mut rec = state.recording.lock().map_err(|e| e.to_string())?;
    // 分开报错，因为这两种情况用户要做的事完全不同：
    // 还在录 → 先停；录完了但还没传上去 → 先重传，否则会丢掉未确认的录音。
    if rec.is_capturing() {
        return Err(format!("already recording ({})", rec.as_str()));
    }
    if rec.has_pending_upload() {
        return Err(format!(
            "previous recording still has data to upload ({}); retry that upload first",
            rec.as_str()
        ));
    }
    // 上一场已经拿到服务端确认了，归零之后才能开下一场
    if let Some(idle) = rec.reset() {
        *rec = idle;
    }
    let next = rec
        .start()
        .ok_or_else(|| format!("cannot start from {}", rec.as_str()))?;

    let upload_id = format!("up_{}", now_ms());
    let app_data = app_data_dir(&app);
    state.set_app_data(app_data.clone());
    let spool = default_spool_dir(&app_data, &meeting_id, &upload_id);
    let mut new_session = LocalRecordingSession::create(
        &meeting_id,
        &gateway_base_url,
        &employee_jwt,
        &upload_id,
        spool,
    )
    .map_err(|e| format!("cannot open spool file: {e}"))?;

    // 开麦失败只记录，不回滚会话
    let _ = new_session.open_mic();
    let mic = new_session.mic_status();
    let spool_dir = new_session.spool_dir().to_string_lossy().into_owned();
    let key_source = new_session.key_source();

    *state.session.lock().map_err(|e| e.to_string())? = Some(new_session);
    *rec = next;

    // 一次录音只允许有一个待清理的本地文件，新会话开始时上一个的记账就作废
    if let Ok(mut settled) = state.settled.lock() {
        *settled = None;
    }
    // 开新会话前尽力冲掉上一场未回传的审计，再清空本地审计缓冲。
    let _ = state.flush_audit_to_gateway();
    if let Ok(mut log) = state.audit.lock() {
        log.clear();
    }
    if let Ok(mut flushed) = state.audit_flushed.lock() {
        *flushed = 0;
    }
    state.remember_creds(&meeting_id, &gateway_base_url, &employee_jwt, &upload_id);
    state.log(
        "local_recording_started",
        format!(
            "upload_id={upload_id} mic_active={} sample_rate={} spool_key={}",
            mic.active,
            mic.sample_rate,
            key_source.as_str()
        ),
    );

    Ok(StartResult {
        state: rec.as_str().to_string(),
        upload_id,
        spool_dir,
        mic,
    })
}

/// 暂停：先把 `paused` 置上，再改状态机。
/// 顺序反过来的话，「状态已是 PAUSED 但回调还在写数据」的窗口里落进去的样本，
/// 就跟审计记录对不上了。cpal 的流不拆，恢复是瞬时的。
#[tauri::command]
fn pause_local_recording(state: State<'_, AppState>) -> Result<String, String> {
    let mut rec = state.recording.lock().map_err(|e| e.to_string())?;
    let next = rec
        .pause()
        .ok_or_else(|| format!("cannot pause from {}", rec.as_str()))?;
    let mut at_byte = 0;
    if let Some(s) = state.session.lock().map_err(|e| e.to_string())?.as_ref() {
        s.set_paused(true);
        at_byte = s.bytes_written();
    }
    *rec = next;
    // 架构 §5.1：暂停必须写审计缺口。记下字节偏移，事后能对齐缺口在录音里的位置。
    state.log("local_recording_paused", format!("at_byte={at_byte}"));
    Ok(rec.as_str().to_string())
}

/// 恢复：先改状态机，再放开 `paused`，同样是为了不产生「状态外」的音频。
#[tauri::command]
fn resume_local_recording(state: State<'_, AppState>) -> Result<String, String> {
    let mut rec = state.recording.lock().map_err(|e| e.to_string())?;
    let next = rec
        .resume()
        .ok_or_else(|| format!("cannot resume from {}", rec.as_str()))?;
    *rec = next;
    let mut at_byte = 0;
    if let Some(s) = state.session.lock().map_err(|e| e.to_string())?.as_ref() {
        s.set_paused(false);
        at_byte = s.bytes_written();
    }
    state.log("local_recording_resumed", format!("at_byte={at_byte}"));
    Ok(rec.as_str().to_string())
}

/// 前端手动喂 PCM（s16le）。和麦克风走同一条 channel，两者可以并存。
/// 返回目前已落盘的字节数 —— 写盘是异步的，这个值只用来显示进度。
#[tauri::command]
fn append_local_pcm(state: State<'_, AppState>, pcm: Vec<u8>) -> Result<u64, String> {
    let rec = *state.recording.lock().map_err(|e| e.to_string())?;
    if rec != RecordingState::Recording {
        return Err(format!("not recording ({})", rec.as_str()));
    }
    let session = state.session.lock().map_err(|e| e.to_string())?;
    let s = session.as_ref().ok_or("no_session")?;
    s.append(pcm)?;
    Ok(s.bytes_written())
}

/// 停止并上传：停麦 → 收尾落盘（密文）→ 解出明文 → 算整文件 checksum
/// → 分块 PUT → complete → 比对 checksum。
///
/// 加密只作用在**磁盘**上：网关契约里的每块 sha256 和整文件 sha256 依然是
/// 明文 PCM 的，服务端要拿明文做审计和 `local_fallback` ASR（架构 §5.3）。
///
/// 中间每一步的状态变化都会写回状态机，前端轮询 `recording_state` 就能看到
/// UPLOADING / RETRY_WAIT / VERIFYING / ACKED 的推进。
#[tauri::command]
fn stop_and_upload_local_recording(state: State<'_, AppState>) -> Result<StopResult, String> {
    // 先把状态机推到 QUEUED，锁在这个块里就还掉
    {
        let mut rec = state.recording.lock().map_err(|e| e.to_string())?;
        *rec = rec
            .finalize()
            .ok_or_else(|| format!("cannot finalize from {}", rec.as_str()))?;
        *rec = rec.queue().ok_or("cannot queue")?;
    }

    // 把会话从全局状态里取出来，收尾后不再放回去
    let mut ended = {
        let mut guard = state.session.lock().map_err(|e| e.to_string())?;
        guard.take().ok_or("no_session")?
    };
    let enc_path = ended.finalize()?;

    // 解密失败（密钥换了 / 文件坏了）不能当作「没录到」：密文必须原样留着，
    // 状态机进 UPLOAD_FAILED，之后配回正确的 METUAI_SPOOL_KEY 还有救。
    let pcm_path = ended.decrypt_for_upload().map_err(|reason| {
        if let Ok(mut rec) = state.recording.lock() {
            if let Some(next) = rec.fail() {
                *rec = next;
            }
        }
        if let Ok(app_data) = state.app_data_path() {
            let _ = queue::upsert(
                &app_data,
                PendingUpload {
                    meeting_id: ended.meeting_id().to_string(),
                    upload_id: ended.upload_id().to_string(),
                    gateway_base: ended.gateway_base().to_string(),
                    spool_dir: ended.spool_dir().to_string_lossy().into(),
                    encrypted_path: enc_path.to_string_lossy().into(),
                    updated_at_ms: 0,
                },
            );
        }
        state.log(
            "local_recording_upload_failed",
            format!("{reason} keep_local=true path={}", enc_path.display()),
        );
        reason
    })?;

    let chunk_bytes = ended.chunk_bytes();
    let src = FileChunks::new(&pcm_path, chunk_bytes)
        .map_err(|e| format!("cannot read spool file: {e}"))?;
    let total_bytes = src.total_len();
    let expected = sha256_file(&pcm_path).map_err(|e| format!("cannot checksum: {e}"))?;
    state.log(
        "local_recording_stopped",
        format!("bytes={total_bytes} parts={} sha256={expected}", src.parts()),
    );

    let sink = GatewaySink::new(
        ended.gateway_base(),
        ended.meeting_id(),
        ended.upload_id(),
        ended.employee_jwt(),
    );

    // 上传器每推进一步就回调一次，这里把状态写进状态机并留一条审计。
    // 注意锁只在闭包内部短暂持有，不会把 recording 锁按住整场上传。
    let recording = &state.recording;
    let app_state = &*state;
    let mut observe = |next: RecordingState| {
        if let Ok(mut rec) = recording.lock() {
            *rec = next;
        }
        app_state.log("local_recording_upload_state", next.as_str());
    };

    let outcome = drive_upload(
        &src,
        &sink,
        &expected,
        &RetryPolicy::default(),
        &mut observe,
    )
    .map_err(|e| {
        // 失败必须保留本地副本，并把路径告诉用户（架构 §5.2）。
        // 顺手断言一下状态机确实还处在「不许删」的状态，免得以后有人改错。
        let reason = e.to_string();
        let keep = state
            .recording
            .lock()
            .map(|rec| rec.must_keep_local())
            .unwrap_or(true);
        // 保留的是**密文**；临时明文要删掉，否则落盘加密就白做了，
        // 而且失败后这份没上锁的录音会一直躺在磁盘上（重传时再解一次即可）。
        ended.discard_plaintext();
        if let Ok(app_data) = state.app_data_path() {
            let _ = queue::upsert(
                &app_data,
                PendingUpload {
                    meeting_id: ended.meeting_id().to_string(),
                    upload_id: ended.upload_id().to_string(),
                    gateway_base: ended.gateway_base().to_string(),
                    spool_dir: ended.spool_dir().to_string_lossy().into(),
                    encrypted_path: enc_path.to_string_lossy().into(),
                    updated_at_ms: 0,
                },
            );
        }
        state.log(
            "local_recording_upload_failed",
            format!("{reason} keep_local={keep} path={}", enc_path.display()),
        );
        reason
    })?;

    state.log(
        "local_recording_acked",
        format!(
            "parts={} sha256={} stored_in={} object_key={}",
            outcome.parts, outcome.checksum, outcome.stored_in, outcome.object_key
        ),
    );

    if let Ok(app_data) = state.app_data_path() {
        let _ = queue::remove(&app_data, ended.upload_id());
    }

    // 上传成功也**不删**本地文件：架构 §5.2 要求由管理员策略决定，
    // 所以这里只登记「可以清理了」，真正删除走 `purge_local_recording`。
    // 登记的是两份：加密的 spool，和上传时解出来的临时明文。
    if let Ok(mut settled) = state.settled.lock() {
        *settled = Some(ended.local_files());
    }

    // 上传成功后尽力回传审计；失败不阻断 ACKED（本地仍有 recording_audit 可重试）。
    let flushed = state.flush_audit_to_gateway().unwrap_or(0);

    Ok(StopResult {
        state: state
            .recording
            .lock()
            .map_err(|e| e.to_string())?
            .as_str()
            .to_string(),
        parts: outcome.parts,
        checksum: outcome.checksum,
        bytes: total_bytes,
        audit_flushed: flushed,
        stored_in: outcome.stored_in,
        object_key: outcome.object_key,
    })
}

/// 服务端已确认后，按管理员策略删除本地副本：ACKED → PURGE_PENDING → PURGED。
///
/// 只有 ACKED 才允许调用 —— 状态机会拦住「还没确认就想删」的调用。
#[tauri::command]
fn purge_local_recording(state: State<'_, AppState>) -> Result<String, String> {
    let mut rec = state.recording.lock().map_err(|e| e.to_string())?;
    *rec = rec
        .request_purge()
        .ok_or_else(|| format!("cannot purge from {}", rec.as_str()))?;

    let paths = state
        .settled
        .lock()
        .map_err(|e| e.to_string())?
        .take()
        .ok_or("no settled recording to purge")?;
    // 密文和临时明文都要删干净。只删一份的话，清理完还留着一段能直接播的录音。
    for path in &paths {
        if path.exists() {
            std::fs::remove_file(path).map_err(|e| format!("cannot remove local copy: {e}"))?;
        }
    }

    *rec = rec.purge().ok_or("cannot mark purged")?;
    let removed: Vec<String> = paths.iter().map(|p| p.display().to_string()).collect();
    state.log("local_recording_purged", removed.join(","));
    let _ = state.flush_audit_to_gateway();
    Ok(rec.as_str().to_string())
}

/// 审计事件流（架构 §5.2）。前端可以展示，也可以回传给网关审计。
#[tauri::command]
fn recording_audit(state: State<'_, AppState>) -> Result<Vec<AuditRecord>, String> {
    let log = state.audit.lock().map_err(|e| e.to_string())?;
    Ok(log.iter().map(AuditRecord::from).collect())
}

/// 把尚未回传的本机录音审计推给网关；上传失败后也可手动重试。
#[tauri::command]
fn flush_recording_audit(state: State<'_, AppState>) -> Result<usize, String> {
    state.flush_audit_to_gateway()
}

/// 列出崩溃/上传失败后仍待传的本地录音（不含 JWT）。
#[tauri::command]
fn list_pending_uploads(app: AppHandle, state: State<'_, AppState>) -> Result<Vec<PendingUpload>, String> {
    let dir = app_data_dir(&app);
    state.set_app_data(dir.clone());
    Ok(queue::list(&dir))
}

/// 用新的员工 JWT 重试某一条待传（解密 → 分块上传 → 比对 checksum）。
#[tauri::command]
fn resume_pending_upload(
    app: AppHandle,
    state: State<'_, AppState>,
    upload_id: String,
    employee_jwt: String,
) -> Result<StopResult, String> {
    let dir = app_data_dir(&app);
    state.set_app_data(dir.clone());
    let pending = queue::get(&dir, &upload_id).ok_or("pending_not_found")?;
    if employee_jwt.trim().is_empty() {
        return Err("employee_jwt required".into());
    }

    let enc = PathBuf::from(&pending.encrypted_path);
    if !enc.is_file() {
        return Err(format!("encrypted spool missing: {}", enc.display()));
    }

    {
        let mut rec = state.recording.lock().map_err(|e| e.to_string())?;
        if !matches!(
            *rec,
            RecordingState::Idle | RecordingState::UploadFailed | RecordingState::Purged
        ) {
            return Err(format!("cannot resume from {}", rec.as_str()));
        }
        *rec = RecordingState::Queued;
    }

    let sess = LocalRecordingSession::open_existing_for_resume(
        &pending.meeting_id,
        &pending.gateway_base,
        &employee_jwt,
        &pending.upload_id,
        PathBuf::from(&pending.spool_dir),
    )
    .map_err(|e| e.to_string())?;

    let pcm_path = sess.decrypt_for_upload().map_err(|reason| {
        if let Ok(mut rec) = state.recording.lock() {
            if let Some(next) = rec.fail() {
                *rec = next;
            }
        }
        reason
    })?;

    let chunk_bytes = sess.chunk_bytes();
    let src = FileChunks::new(&pcm_path, chunk_bytes).map_err(|e| e.to_string())?;
    let total_bytes = src.total_len();
    let expected = sha256_file(&pcm_path).map_err(|e| e.to_string())?;
    state.remember_creds(
        &pending.meeting_id,
        &pending.gateway_base,
        &employee_jwt,
        &pending.upload_id,
    );

    let sink = GatewaySink::new(
        &pending.gateway_base,
        &pending.meeting_id,
        &pending.upload_id,
        &employee_jwt,
    );
    let recording = &state.recording;
    let app_state = &*state;
    let mut observe = |next: RecordingState| {
        if let Ok(mut rec) = recording.lock() {
            *rec = next;
        }
        app_state.log("local_recording_upload_state", next.as_str());
    };

    let outcome =
        drive_upload(&src, &sink, &expected, &RetryPolicy::default(), &mut observe).map_err(
            |e| {
                sess.discard_plaintext();
                if let Ok(mut rec) = state.recording.lock() {
                    if let Some(next) = rec.fail() {
                        *rec = next;
                    }
                }
                e.to_string()
            },
        )?;

    let _ = queue::remove(&dir, &pending.upload_id);
    if let Ok(mut settled) = state.settled.lock() {
        *settled = Some(sess.local_files());
    }
    state.log(
        "local_recording_acked",
        format!("resume parts={} sha256={}", outcome.parts, outcome.checksum),
    );
    let flushed = state.flush_audit_to_gateway().unwrap_or(0);
    Ok(StopResult {
        state: state
            .recording
            .lock()
            .map_err(|e| e.to_string())?
            .as_str()
            .to_string(),
        parts: outcome.parts,
        checksum: outcome.checksum,
        bytes: total_bytes,
        audit_flushed: flushed,
        stored_in: outcome.stored_in,
        object_key: outcome.object_key,
    })
}

/// 应用数据目录；拿不到就退回系统临时目录，至少别让录音丢在内存里。
fn app_data_dir(app: &AppHandle) -> PathBuf {
    app.path()
        .app_data_dir()
        .unwrap_or_else(|_| std::env::temp_dir().join("metuai-desktop"))
}

fn now_ms() -> u128 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0)
}

/// 端到端串联测试：录音会话 → spool 文件 → 分块 → 上传 → 校验。
///
/// 用假网关（`FakeSink`）代替真 HTTP，所以不需要起服务端；
/// 用 `append` 代替真麦克风，所以不需要声卡。命令层那几个 `#[tauri::command]`
/// 需要真的 `AppHandle` 才能跑，所以这里测的是它们内部调用的同一条链路。
#[cfg(test)]
mod tests {
    use super::*;
    use crate::spool_crypto::{KeySource, SpoolKey};
    use crate::uploader::testing::FakeSink;

    /// 固定密钥建会话。不动 `METUAI_SPOOL_KEY`：环境变量是进程全局的，
    /// 并行跑测试时改它会互相干扰。
    fn session_in(dir: &std::path::Path, upload_id: &str) -> LocalRecordingSession {
        LocalRecordingSession::create_with_key(
            "mtg_1",
            "http://127.0.0.1:18080",
            "jwt",
            upload_id,
            dir.to_path_buf(),
            SpoolKey::from_bytes([42u8; 32]),
            KeySource::Env,
        )
        .unwrap()
    }

    #[test]
    fn recorded_session_uploads_byte_identical_data() {
        let dir = tempfile::tempdir().unwrap();
        let mut sess = session_in(dir.path(), "up_1");

        // 模拟一次「录 → 暂停 → 恢复 → 录」：暂停期间的数据不该进文件
        let head = vec![1u8; 700];
        let tail = vec![2u8; 700];
        sess.append(head.clone()).unwrap();
        sess.set_paused(true);
        sess.append(vec![255u8; 5000]).unwrap();
        sess.set_paused(false);
        sess.append(tail.clone()).unwrap();

        // 磁盘上是密文，上传前才解出明文；分块与 checksum 都按明文算
        sess.finalize().unwrap();
        let path = sess.decrypt_for_upload().unwrap();
        let expected: Vec<u8> = head.iter().chain(tail.iter()).copied().collect();
        let checksum = sha256_file(&path).unwrap();

        // 故意用很小的块，逼出多块 + 最后一块偏短的情况
        let src = FileChunks::new(&path, 256).unwrap();
        assert_eq!(src.total_len(), 1400);
        assert_eq!(src.parts(), 6, "1400 / 256 向上取整 = 6 块");

        let sink = FakeSink::new();
        let mut states = Vec::new();
        let outcome = drive_upload(
            &src,
            &sink,
            &checksum,
            &RetryPolicy::immediate(3),
            &mut |s| states.push(s),
        )
        .expect("upload should succeed");

        assert_eq!(outcome.parts, 6);
        assert_eq!(outcome.checksum, checksum);
        assert_eq!(sink.merged(), expected, "服务端合并后必须与录到的字节完全一致");
        assert_eq!(states.last(), Some(&RecordingState::Acked));
    }

    #[test]
    fn empty_recording_fails_upload_and_keeps_state_consistent() {
        let dir = tempfile::tempdir().unwrap();
        let mut sess = session_in(dir.path(), "up_2");
        sess.finalize().unwrap();
        let path = sess.decrypt_for_upload().unwrap();

        let src = FileChunks::new(&path, 256).unwrap();
        let sink = FakeSink::new();
        let mut states = Vec::new();
        drive_upload(&src, &sink, "", &RetryPolicy::immediate(3), &mut |s| {
            states.push(s)
        })
        .expect_err("一个字节都没录到不该去打扰网关");

        assert_eq!(*sink.put_calls.lock().unwrap(), 0);
        assert_eq!(states.last(), Some(&RecordingState::UploadFailed));
    }

    #[test]
    fn undecryptable_spool_keeps_the_ciphertext_and_fails_the_upload() {
        let dir = tempfile::tempdir().unwrap();
        let mut sess = session_in(dir.path(), "up_3");
        sess.append(vec![9u8; 4096]).unwrap();
        let enc_path = sess.finalize().unwrap();

        // 模拟磁盘损坏 / 密文被动过：翻掉最后一个字节，GCM tag 就对不上了
        let mut blob = std::fs::read(&enc_path).unwrap();
        *blob.last_mut().unwrap() ^= 0x01;
        std::fs::write(&enc_path, &blob).unwrap();

        let err = sess.decrypt_for_upload().expect_err("解不开就不能上传");
        assert!(err.contains("decrypt"), "错误要说清是解密失败: {err}");
        assert!(enc_path.exists(), "解不开也必须留着密文，别把唯一的副本删了");
        assert!(
            !dir.path().join("mic.pcm").exists(),
            "失败时不能留下半截明文"
        );

        // 这一步发生在 QUEUED，状态机要能进 UPLOAD_FAILED 并保住本地文件
        let failed = RecordingState::Queued.fail().expect("QUEUED 可以进失败态");
        assert_eq!(failed, RecordingState::UploadFailed);
        assert!(failed.must_keep_local());
    }

    #[test]
    fn purge_removes_both_the_ciphertext_and_the_plaintext() {
        let dir = tempfile::tempdir().unwrap();
        let mut sess = session_in(dir.path(), "up_4");
        sess.append(vec![5u8; 512]).unwrap();
        sess.finalize().unwrap();
        sess.decrypt_for_upload().unwrap();

        // purge 命令本身要真 AppHandle，这里验它删的那份清单
        let files = sess.local_files();
        assert!(files.iter().all(|p| p.exists()));
        for path in &files {
            std::fs::remove_file(path).unwrap();
        }
        assert!(
            std::fs::read_dir(dir.path()).unwrap().next().is_none(),
            "清理完不能还剩一段能直接播的录音"
        );
    }

    #[test]
    fn audit_log_keeps_order_and_never_holds_credentials() {
        let state = AppState::new();
        state.log("local_recording_started", "upload_id=up_1 mic_active=true");
        state.log("local_recording_paused", "at_byte=4096");
        state.log("local_recording_resumed", "at_byte=4096");
        state.log("local_recording_acked", "parts=3 sha256=abc");

        let records: Vec<AuditRecord> = state
            .audit
            .lock()
            .unwrap()
            .iter()
            .map(AuditRecord::from)
            .collect();

        let actions: Vec<&str> = records.iter().map(|r| r.action.as_str()).collect();
        assert_eq!(
            actions,
            vec![
                "local_recording_started",
                "local_recording_paused",
                "local_recording_resumed",
                "local_recording_acked",
            ],
            "审计必须按发生顺序排列"
        );
        // 暂停缺口要能定位到字节偏移（架构 §5.1）
        assert_eq!(records[1].detail, "at_byte=4096");
        assert!(records.iter().all(|r| r.at_ms > 0), "时间戳不能是 0");

        // 架构 §5.3：凭据不能进审计 / 日志
        let json = serde_json::to_string(&records).unwrap();
        assert!(!json.contains("jwt"), "审计不能带凭据: {json}");
    }

    #[test]
    fn purge_is_only_reachable_after_ack() {
        // 命令层需要真 AppHandle 才能跑，这里直接验它依赖的状态机约束：
        // 只有 ACKED 能进 PURGE_PENDING，其余状态一律拒绝。
        assert_eq!(
            RecordingState::Acked.request_purge(),
            Some(RecordingState::PurgePending)
        );
        assert_eq!(
            RecordingState::PurgePending.purge(),
            Some(RecordingState::Purged)
        );
        for state in [
            RecordingState::Recording,
            RecordingState::Paused,
            RecordingState::Queued,
            RecordingState::Uploading,
            RecordingState::Verifying,
            RecordingState::UploadFailed,
        ] {
            assert_eq!(
                state.request_purge(),
                None,
                "{} 还没拿到服务端确认，不该允许删本地文件",
                state.as_str()
            );
        }
    }

    #[test]
    fn spool_path_is_scoped_to_meeting_and_upload() {
        let dir = tempfile::tempdir().unwrap();
        let first = default_spool_dir(dir.path(), "mtg_1", "up_1");
        let second = default_spool_dir(dir.path(), "mtg_1", "up_2");
        assert_ne!(
            first, second,
            "同一场会重录一次不能覆盖上一次还没传完的文件"
        );
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .manage(AppState::new())
        .invoke_handler(tauri::generate_handler![
            client_kind,
            recording_state,
            recording_audit,
            start_local_recording,
            pause_local_recording,
            resume_local_recording,
            append_local_pcm,
            stop_and_upload_local_recording,
            purge_local_recording,
            flush_recording_audit,
            list_pending_uploads,
            resume_pending_upload,
        ])
        .run(tauri::generate_context!())
        .expect("error while running metuai desktop");
}
