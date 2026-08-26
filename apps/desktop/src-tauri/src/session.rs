//! 本机录音会话：把「麦克风采集 → 暂停开关 → 落盘 spool 文件」串成一条线。
//!
//! 分工（每层都能单独测）：
//! - `capture.rs` 只负责从 cpal 拿字节，不知道文件在哪；
//! - 这里负责**会话生命周期**：开文件、开麦、暂停、收尾；
//! - `uploader.rs` 只负责把收尾后的文件传上去。
//!
//! 三条关键设计：
//! 1. **音频回调里不做文件 IO**。回调线程有硬实时预算（几毫秒），一次磁盘卡顿就会丢帧。
//!    所以回调只把字节丢进 channel，由一个专门的写盘线程慢慢写。
//! 2. **暂停不拆流**。`paused` 是个 `AtomicBool`，暂停时回调直接丢样本，
//!    但 cpal 的 stream 一直活着。这样恢复是瞬时的，不用重新申请麦克风权限。
//!    （架构 §5：暂停要写审计缺口；静音不是暂停。）
//! 3. **落盘即加密**（架构 §5.3）。写盘线程收到的每一块 PCM 都会被加成一个独立的
//!    AES-256-GCM 帧再追加进 `mic.pcm.enc`，磁盘上任何时刻都没有裸 PCM。
//!    上传前才解密出临时明文 `mic.pcm` —— 服务端要拿明文做审计和 ASR，
//!    所以这是**设备落盘保护**，不是端到端加密。

use crate::capture::{spawn_default_mic, AudioFormat, MicHandle};
use crate::chunk::chunk_bytes_for_duration;
use crate::spool_crypto::{
    decrypt_stream, file_looks_encrypted, KeySource, SpoolEncryptor, SpoolKey,
};
use std::io;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{self, Sender};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;

/// 一块大约装几秒音频。5 秒在 48 kHz 单声道下约 480 KiB，
/// 既不会让一场会产生上万个请求，也远低于网关单块 8 MiB 的上限。
const CHUNK_SECONDS: u32 = 5;

/// 加密后的 spool 文件名。扩展名带 `.enc` 是给人看的：
/// 有人翻到这个目录时，一眼就知道不该拿去当 raw PCM 播。
pub const ENCRYPTED_SPOOL_NAME: &str = "mic.pcm.enc";

/// 上传前解出来的临时明文。上传完成后由 `purge` 一并删掉。
pub const PLAINTEXT_SPOOL_NAME: &str = "mic.pcm";

/// spool 目录布局：`<appData>/local-recording/<meetingId>/<uploadId>/`。
/// 按 uploadId 分目录，是为了「同一场会重录一次」不会覆盖上一次没传完的文件。
pub fn default_spool_dir(app_data: &Path, meeting_id: &str, upload_id: &str) -> PathBuf {
    app_data
        .join("local-recording")
        .join(sanitize_segment(meeting_id))
        .join(sanitize_segment(upload_id))
}

/// meetingId / uploadId 来自调用方，直接拼进路径的话，`../..` 就能把文件写到目录外面去。
/// 白名单式过滤：只留字母数字和 `-` `_`（这两个 id 本来就是 uuid 风格），其余换成 `_`。
/// 白名单比黑名单安全 —— 黑名单永远会漏掉某个没想到的字符。
fn sanitize_segment(segment: &str) -> String {
    if segment.is_empty() {
        return "_".to_string();
    }
    segment
        .chars()
        .map(|c| {
            if c.is_ascii_alphanumeric() || matches!(c, '-' | '_') {
                c
            } else {
                '_'
            }
        })
        .collect()
}

/// 建一个只有本人可读写的文件（unix 0o600）。
/// 录音是敏感数据，不能落成默认 0o644 让同机其他账号读走。
fn create_private_file(path: &Path) -> io::Result<std::fs::File> {
    let mut opts = std::fs::OpenOptions::new();
    opts.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    opts.open(path)
}

/// 写盘线程的句柄。
struct SpoolWriter {
    join: Option<JoinHandle<()>>,
    written: Arc<AtomicU64>,
    error: Arc<Mutex<Option<String>>>,
}

/// 起写盘线程：从 channel 收 PCM 字节，**逐块加密**后顺序追加到 `path`。
///
/// 每收到一条消息就加一个 GCM 帧（见 `spool_crypto`），所以内存里从来不会
/// 攒着整场录音，磁盘上也从来不会出现裸 PCM。
///
/// 返回写盘句柄和**发送端**。发送端可以随便 clone（麦克风线程一份、
/// 前端手动喂 PCM 的路径一份）；所有发送端都 drop 掉，线程自然收尾退出。
fn spawn_spool_writer(path: &Path, key: &SpoolKey) -> io::Result<(SpoolWriter, Sender<Vec<u8>>)> {
    let file = create_private_file(path)?;
    // BufWriter：把很多个小 buffer 攒成大块再落盘，少几百次系统调用。
    // 顺序是 BufWriter 在里、加密器在外，所以缓冲的已经是密文。
    let encryptor = SpoolEncryptor::new(io::BufWriter::new(file), key)
        .map_err(|e| io::Error::new(io::ErrorKind::Other, e.to_string()))?;
    let (tx, rx) = mpsc::channel::<Vec<u8>>();
    let written = Arc::new(AtomicU64::new(0));
    let error: Arc<Mutex<Option<String>>> = Arc::new(Mutex::new(None));

    let join = std::thread::Builder::new()
        .name("metuai-spool".into())
        .spawn({
            let written = Arc::clone(&written);
            let error = Arc::clone(&error);
            move || {
                let mut out = encryptor;
                let mut failed = false;
                // recv() 在所有发送端 drop 后返回 Err，循环自然结束
                while let Ok(buf) = rx.recv() {
                    if failed {
                        // 已经出过错就只把 channel 排空，别再刷同一条错误
                        continue;
                    }
                    match out.write_frame(&buf) {
                        Ok(()) => {
                            // 记的是**明文**字节数：UI 的进度、审计里的 at_byte
                            // 说的都是「录了多少音频」，不是「占了多少磁盘」。
                            written.fetch_add(buf.len() as u64, Ordering::Relaxed);
                        }
                        Err(e) => {
                            failed = true;
                            *error.lock().expect("spool error lock") = Some(e.to_string());
                        }
                    }
                }
                // 收尾：BufWriter 里还压着没写的字节，必须 flush + fsync，
                // 否则进程崩溃时最后几秒录音就没了
                let finish = out
                    .into_inner()
                    .into_inner()
                    .map_err(|e| e.to_string())
                    .and_then(|f| f.sync_all().map_err(|e| e.to_string()));
                if let Err(e) = finish {
                    if !failed {
                        *error.lock().expect("spool error lock") = Some(e);
                    }
                }
            }
        })?;

    Ok((
        SpoolWriter {
            join: Some(join),
            written,
            error,
        },
        tx,
    ))
}

/// 麦克风状态，给前端显示用（"正在录" / "没有麦克风"）。
#[derive(Debug, Clone, serde::Serialize)]
pub struct MicStatus {
    pub active: bool,
    pub sample_rate: u32,
    pub channels: u16,
    /// 开麦失败的原因；成功时为 `None`。
    pub error: Option<String>,
}

/// 一次本机录音会话。
pub struct LocalRecordingSession {
    meeting_id: String,
    gateway_base: String,
    employee_jwt: String,
    upload_id: String,
    spool_dir: PathBuf,
    /// 加密后的录音，唯一长期留在磁盘上的那份。
    enc_path: PathBuf,
    /// 上传前临时解出来的明文，`decrypt_for_upload` 之前并不存在。
    plain_path: PathBuf,
    key: SpoolKey,
    key_source: KeySource,
    paused: Arc<AtomicBool>,
    /// 会话自己持有的发送端。收尾时先 drop 它，写盘线程才会退出。
    pcm_tx: Option<Sender<Vec<u8>>>,
    writer: Option<SpoolWriter>,
    /// 与写盘线程共享的明文字节计数器。收尾会把 `writer` 取走，
    /// 所以计数器要单独留一份，不然停止录音后进度就读不到了。
    written: Arc<AtomicU64>,
    mic: Option<MicHandle>,
    format: AudioFormat,
    mic_error: Option<String>,
}

impl LocalRecordingSession {
    /// 建会话：取落盘密钥 → 开加密 spool 文件和写盘线程，**不碰麦克风**。
    ///
    /// 开麦是单独一步（`open_mic`），这样单测可以完整跑「落盘 → 收尾 → 上传」，
    /// 不需要 CI 机器上真有一块声卡。
    ///
    /// 密钥取不到（`METUAI_SPOOL_KEY` 写错了）时**直接失败**，不退回明文落盘 ——
    /// 悄悄降级成明文比录不上音更糟：用户以为文件是加密的。
    pub fn create(
        meeting_id: impl Into<String>,
        gateway_base: impl Into<String>,
        employee_jwt: impl Into<String>,
        upload_id: impl Into<String>,
        spool_dir: PathBuf,
    ) -> io::Result<Self> {
        let gateway_base = gateway_base.into();
        let employee_jwt = employee_jwt.into();
        let (key, source) =
            SpoolKey::resolve_preferring_enterprise(&gateway_base, &employee_jwt)
                .map_err(|e| io::Error::new(io::ErrorKind::InvalidInput, e.to_string()))?;
        Self::create_with_key(
            meeting_id,
            gateway_base,
            employee_jwt,
            upload_id,
            spool_dir,
            key,
            source,
        )
    }

    /// 显式指定密钥的构造方式。测试用它避免去改进程级的环境变量
    /// （环境变量是全局的，并行跑的测试会互相踩）。
    pub fn create_with_key(
        meeting_id: impl Into<String>,
        gateway_base: impl Into<String>,
        employee_jwt: impl Into<String>,
        upload_id: impl Into<String>,
        spool_dir: PathBuf,
        key: SpoolKey,
        key_source: KeySource,
    ) -> io::Result<Self> {
        std::fs::create_dir_all(&spool_dir)?;
        let enc_path = spool_dir.join(ENCRYPTED_SPOOL_NAME);
        let plain_path = spool_dir.join(PLAINTEXT_SPOOL_NAME);
        let (writer, tx) = spawn_spool_writer(&enc_path, &key)?;
        let written = Arc::clone(&writer.written);
        Ok(Self {
            meeting_id: meeting_id.into(),
            gateway_base: gateway_base.into(),
            employee_jwt: employee_jwt.into(),
            upload_id: upload_id.into(),
            spool_dir,
            enc_path,
            plain_path,
            key,
            key_source,
            paused: Arc::new(AtomicBool::new(false)),
            pcm_tx: Some(tx),
            writer: Some(writer),
            written,
            mic: None,
            format: AudioFormat::default(),
            mic_error: None,
        })
    }

    /// 崩溃恢复：打开已有加密 spool，不再写盘；只用于 decrypt + 上传。
    pub fn open_existing_for_resume(
        meeting_id: impl Into<String>,
        gateway_base: impl Into<String>,
        employee_jwt: impl Into<String>,
        upload_id: impl Into<String>,
        spool_dir: PathBuf,
    ) -> io::Result<Self> {
        let (key, source) = SpoolKey::resolve()
            .map_err(|e| io::Error::new(io::ErrorKind::InvalidInput, e.to_string()))?;
        let enc_path = spool_dir.join(ENCRYPTED_SPOOL_NAME);
        if !enc_path.is_file() {
            return Err(io::Error::new(
                io::ErrorKind::NotFound,
                format!("missing {}", enc_path.display()),
            ));
        }
        let plain_path = spool_dir.join(PLAINTEXT_SPOOL_NAME);
        let written = Arc::new(AtomicU64::new(
            std::fs::metadata(&enc_path).map(|m| m.len()).unwrap_or(0),
        ));
        Ok(Self {
            meeting_id: meeting_id.into(),
            gateway_base: gateway_base.into(),
            employee_jwt: employee_jwt.into(),
            upload_id: upload_id.into(),
            spool_dir,
            enc_path,
            plain_path,
            key,
            key_source: source,
            paused: Arc::new(AtomicBool::new(false)),
            pcm_tx: None,
            writer: None,
            written,
            mic: None,
            format: AudioFormat::default(),
            mic_error: None,
        })
    }

    /// 这次落盘用的是下发密钥还是开发默认密钥（写进审计，方便事后排查）。
    pub fn key_source(&self) -> KeySource {
        self.key_source
    }

    /// 打开默认输入设备并开始采集。失败时记下原因、返回 `Err`，
    /// 但会话本身照样活着 —— 前端仍可用 `append_local_pcm` 喂 PCM（WebAudio 兜底）。
    pub fn open_mic(&mut self) -> Result<AudioFormat, String> {
        let tx = self
            .pcm_tx
            .as_ref()
            .ok_or_else(|| "session already finalized".to_string())?
            .clone();
        match spawn_default_mic(Arc::clone(&self.paused), tx) {
            Ok(handle) => {
                self.format = handle.format();
                self.mic = Some(handle);
                self.mic_error = None;
                Ok(self.format)
            }
            Err(e) => {
                self.mic_error = Some(e.clone());
                Err(e)
            }
        }
    }

    pub fn mic_status(&self) -> MicStatus {
        MicStatus {
            active: self.mic.is_some(),
            sample_rate: self.format.sample_rate,
            channels: self.format.channels,
            error: self.mic_error.clone(),
        }
    }

    /// 翻暂停开关。注意流不拆，只是让回调开始丢样本。
    pub fn set_paused(&self, paused: bool) {
        self.paused.store(paused, Ordering::Relaxed);
    }

    pub fn is_paused(&self) -> bool {
        self.paused.load(Ordering::Relaxed)
    }

    /// 前端手动喂一段 PCM（没有 cpal 的降级构建、或用 WebAudio 采集时用）。
    /// 走的是和麦克风完全相同的 channel，所以两条路的数据会正确交织在一个文件里。
    pub fn append(&self, pcm: Vec<u8>) -> Result<(), String> {
        if self.is_paused() {
            // 暂停期间不该有数据，静默丢掉比写进文件更符合审计语义
            return Ok(());
        }
        self.pcm_tx
            .as_ref()
            .ok_or_else(|| "session already finalized".to_string())?
            .send(pcm)
            .map_err(|_| "spool writer stopped".to_string())
    }

    /// 已经落盘的**明文**字节数（= 录了多少音频，不是密文占了多少磁盘）。
    /// 写盘是异步的，所以这个值可能比刚喂进去的略少，
    /// 只用来给 UI 显示进度，别拿它做正确性判断。
    ///
    /// 计数器由会话自己持有，收尾之后依然读得到 —— 否则前端一停止录音，
    /// 进度就会跳回 0。
    pub fn bytes_written(&self) -> u64 {
        self.written.load(Ordering::Relaxed)
    }

    /// 一块传多少字节：按当前采样率折算约 5 秒音频，并夹在网关允许的区间内。
    pub fn chunk_bytes(&self) -> usize {
        chunk_bytes_for_duration(self.format.bytes_per_second(), CHUNK_SECONDS)
    }

    pub fn meeting_id(&self) -> &str {
        &self.meeting_id
    }

    pub fn gateway_base(&self) -> &str {
        &self.gateway_base
    }

    pub fn employee_jwt(&self) -> &str {
        &self.employee_jwt
    }

    pub fn upload_id(&self) -> &str {
        &self.upload_id
    }

    /// 录音落在哪个目录。上传失败时前端要把这个路径显示给用户
    /// （架构 §5：没拿到服务端确认之前，本地文件一律不删）。
    pub fn spool_dir(&self) -> &Path {
        &self.spool_dir
    }

    /// 这次会话可能在磁盘上留下的文件（密文 + 临时明文），给 `purge` 用。
    /// 顺序是「先删明文再删密文」：万一中途失败，留下的是加密的那份。
    pub fn local_files(&self) -> Vec<PathBuf> {
        vec![self.plain_path.clone(), self.enc_path.clone()]
    }

    /// 收尾：停麦 → 关掉发送端 → 等写盘线程把剩下的字节刷完 → 返回**密文**路径。
    ///
    /// 顺序很重要：先停麦才能保证不会再有新数据进 channel；
    /// 必须 drop 掉**所有**发送端写盘线程才会退出，否则 join 会永远卡住。
    pub fn finalize(&mut self) -> Result<PathBuf, String> {
        if let Some(mut mic) = self.mic.take() {
            mic.stop();
        }
        drop(self.pcm_tx.take()); // 会话这份发送端
        if let Some(mut writer) = self.writer.take() {
            if let Some(join) = writer.join.take() {
                join.join().map_err(|_| "spool writer panicked".to_string())?;
            }
            if let Some(err) = writer.error.lock().expect("spool error lock").take() {
                return Err(format!("spool write failed: {err}"));
            }
        }
        Ok(self.enc_path.clone())
    }

    /// 收尾之后把密文解成临时明文 `mic.pcm`，返回明文路径。
    ///
    /// 为什么上传前一定要落成明文文件，而不是边传边解密：
    /// 网关契约是「每块 sha256 + 合并后整文件 sha256」，**都按明文 PCM 算**。
    /// 先解成一个完整文件，`FileChunks` 就还能按偏移 seek 取块，
    /// 断点续传（重传第 N 块）也不用把前面 N-1 块重解一遍。
    ///
    /// 解不开就报错，由调用方保留密文并进 `UPLOAD_FAILED` —— 绝不能传上去一段空文件。
    pub fn decrypt_for_upload(&self) -> Result<PathBuf, String> {
        if self.writer.is_some() {
            // 还在录就解密的话，缓冲里的最后几帧根本没落盘
            return Err("session not finalized yet".to_string());
        }
        if !file_looks_encrypted(&self.enc_path) {
            return Err(format!(
                "spool file is not encrypted: {}",
                self.enc_path.display()
            ));
        }
        let src = std::fs::File::open(&self.enc_path)
            .map_err(|e| format!("cannot open encrypted spool: {e}"))?;
        // 明文和密文一样敏感，同样只给本人读写
        let mut dst = create_private_file(&self.plain_path)
            .map_err(|e| format!("cannot create plaintext spool: {e}"))?;
        match decrypt_stream(src, &mut dst, &self.key) {
            Ok(_) => {
                // 后面 FileChunks 要按 metadata().len() 分块，必须先把缓冲落到磁盘
                dst.sync_all()
                    .map_err(|e| format!("cannot flush plaintext spool: {e}"))?;
                Ok(self.plain_path.clone())
            }
            Err(e) => {
                // 半截明文比没有更危险：既不能上传，又是一份没上锁的录音残片
                drop(dst);
                self.discard_plaintext();
                Err(format!("cannot decrypt spool: {e}"))
            }
        }
    }

    /// 删掉上传用的临时明文，密文原样留着。
    ///
    /// 上传失败时一定要调：明文只是为了算 checksum 和分块才解出来的，
    /// 留在磁盘上就等于把落盘加密白做了 —— 而重传时再解一次几乎不花时间。
    /// 「未确认不删本地文件」保住的是**密文**那一份。
    pub fn discard_plaintext(&self) {
        // 已经不在了也算成功，所以忽略错误
        let _ = std::fs::remove_file(&self.plain_path);
    }
}

impl Drop for LocalRecordingSession {
    fn drop(&mut self) {
        // 前端崩了 / 用户直接关窗口时，也要保证麦克风灯灭掉、缓冲刷进文件
        let _ = self.finalize();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::chunk::{sha256_file, sha256_hex};
    use crate::spool_crypto::{SpoolCryptoError, MAGIC};

    /// 固定测试密钥。不去动 `METUAI_SPOOL_KEY`：环境变量是进程全局的，
    /// cargo 又默认并行跑测试，改它等于让测试之间互相干扰。
    fn test_key() -> SpoolKey {
        SpoolKey::from_bytes([42u8; 32])
    }

    fn session_in(dir: &Path) -> LocalRecordingSession {
        LocalRecordingSession::create_with_key(
            "mtg_1",
            "http://127.0.0.1:18080",
            "jwt-token",
            "up_1",
            dir.to_path_buf(),
            test_key(),
            KeySource::Env,
        )
        .expect("session should be creatable")
    }

    /// 收尾 + 解密，返回明文路径。绝大多数断言都是对明文做的。
    fn finalize_and_decrypt(session: &mut LocalRecordingSession) -> PathBuf {
        session.finalize().expect("finalize");
        session.decrypt_for_upload().expect("decrypt")
    }

    #[test]
    fn appended_pcm_survives_the_encrypt_decrypt_round_trip() {
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        let a = vec![1u8; 1000];
        let b = vec![2u8; 500];
        session.append(a.clone()).unwrap();
        session.append(b.clone()).unwrap();

        let path = finalize_and_decrypt(&mut session);
        let mut expected = a.clone();
        expected.extend_from_slice(&b);
        assert_eq!(std::fs::read(&path).unwrap(), expected, "顺序必须保持");
        // 解出来的明文 checksum 才是之后要跟服务端比对的那个值
        assert_eq!(sha256_file(&path).unwrap(), sha256_hex(&expected));
    }

    #[test]
    fn spool_file_on_disk_is_never_raw_pcm() {
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        // 有规律的假 PCM：没加密的话它会原样躺在文件里
        let pcm: Vec<u8> = (0..8192u32).map(|i| (i % 251) as u8).collect();
        session.append(pcm.clone()).unwrap();
        let enc_path = session.finalize().unwrap();

        assert_eq!(enc_path.file_name().unwrap(), ENCRYPTED_SPOOL_NAME);
        let on_disk = std::fs::read(&enc_path).unwrap();
        assert_eq!(&on_disk[..8], MAGIC, "文件要自描述");
        assert!(
            !on_disk.windows(128).any(|w| w == &pcm[..128]),
            "密文里不该出现可识别的 PCM 片段"
        );
        assert_ne!(sha256_file(&enc_path).unwrap(), sha256_hex(&pcm));
        // 解密前，明文文件根本不存在
        assert!(!dir.path().join(PLAINTEXT_SPOOL_NAME).exists());
    }

    #[test]
    fn wrong_key_cannot_read_the_spool_file() {
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        session.append(vec![3u8; 2048]).unwrap();
        let enc_path = session.finalize().unwrap();

        let src = std::fs::File::open(&enc_path).unwrap();
        let mut out = Vec::new();
        let err = decrypt_stream(src, &mut out, &SpoolKey::from_bytes([1u8; 32]))
            .expect_err("换一把密钥必须解不开");
        assert!(matches!(err, SpoolCryptoError::Decrypt), "得到 {err:?}");
        // 正确的密钥仍然能解开
        assert_eq!(
            std::fs::read(session.decrypt_for_upload().unwrap()).unwrap().len(),
            2048
        );
    }

    #[test]
    fn decrypting_before_finalize_is_rejected() {
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        session.append(vec![1u8; 64]).unwrap();
        assert!(
            session.decrypt_for_upload().is_err(),
            "还在录就解密的话，缓冲里的帧还没落盘"
        );
        finalize_and_decrypt(&mut session);
    }

    #[test]
    fn paused_session_drops_samples_but_stays_alive() {
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        session.append(vec![7u8; 10]).unwrap();

        session.set_paused(true);
        assert!(session.is_paused());
        session.append(vec![9u8; 999]).unwrap(); // 暂停期间的数据要被丢掉

        session.set_paused(false);
        session.append(vec![7u8; 10]).unwrap();

        let path = finalize_and_decrypt(&mut session);
        let data = std::fs::read(&path).unwrap();
        assert_eq!(data.len(), 20, "暂停期间的 999 字节不该落盘");
        assert!(data.iter().all(|b| *b == 7));
    }

    #[test]
    fn bytes_written_counts_plaintext_not_ciphertext() {
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        session.append(vec![0u8; 4096]).unwrap();
        let enc_path = session.finalize().unwrap();

        // 进度和审计里的 at_byte 说的是「录了多少音频」，不是「占了多少磁盘」
        assert_eq!(session.bytes_written(), 4096);
        assert!(
            std::fs::metadata(&enc_path).unwrap().len() > 4096,
            "密文会比明文多出 magic / nonce / 长度 / tag"
        );
        let plain = session.decrypt_for_upload().unwrap();
        assert_eq!(std::fs::metadata(&plain).unwrap().len(), 4096);
    }

    #[test]
    fn append_after_finalize_is_rejected() {
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        session.finalize().unwrap();
        assert!(
            session.append(vec![1u8; 4]).is_err(),
            "收尾之后再喂数据必须报错，不能悄悄丢"
        );
    }

    #[test]
    fn finalize_is_idempotent() {
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        session.append(vec![3u8; 8]).unwrap();
        let first = session.finalize().unwrap();
        // Drop 里还会再调一次，所以重复调用不能 panic 或卡死
        let second = session.finalize().unwrap();
        assert_eq!(first, second);
    }

    #[test]
    fn empty_session_still_produces_a_file() {
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        let enc_path = session.finalize().unwrap();
        assert!(enc_path.exists(), "0 字节也要有文件，方便上层报 empty_recording");
        // 密文侧只剩一个 magic 头；解出来是 0 字节明文
        assert_eq!(std::fs::metadata(&enc_path).unwrap().len(), MAGIC.len() as u64);
        let plain = session.decrypt_for_upload().unwrap();
        assert_eq!(std::fs::metadata(&plain).unwrap().len(), 0);
    }

    #[test]
    fn chunk_bytes_follows_the_capture_format() {
        let dir = tempfile::tempdir().unwrap();
        let session = session_in(dir.path());
        // 缺省 48 kHz 单声道 s16le，5 秒 = 48000 * 1 * 2 * 5
        assert_eq!(session.chunk_bytes(), 480_000);
    }

    #[test]
    fn spool_dir_layout_matches_upload_id() {
        let base = Path::new("/tmp/app");
        let dir = default_spool_dir(base, "mtg_1", "up_9");
        assert_eq!(dir, Path::new("/tmp/app/local-recording/mtg_1/up_9"));
    }

    #[test]
    fn spool_dir_rejects_path_traversal() {
        let base = Path::new("/tmp/app");
        let dir = default_spool_dir(base, "../../etc", "up_9");
        assert!(
            !dir.to_string_lossy().contains(".."),
            "meetingId 里的 .. 必须被消掉: {dir:?}"
        );
        assert!(dir.starts_with(base));
        assert_eq!(default_spool_dir(base, "..", "u").file_name().unwrap(), "u");
    }

    #[cfg(unix)]
    #[test]
    fn both_spool_files_are_owner_only() {
        use std::os::unix::fs::PermissionsExt;
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        session.append(vec![1u8; 128]).unwrap();
        session.finalize().unwrap();
        session.decrypt_for_upload().unwrap();

        // 解出来的明文和密文一样敏感，两份都不能让同机其他账号读到
        for path in session.local_files() {
            let mode = std::fs::metadata(&path).unwrap().permissions().mode();
            assert_eq!(mode & 0o777, 0o600, "{} 权限过宽", path.display());
        }
    }

    #[test]
    fn discarding_the_plaintext_keeps_the_ciphertext() {
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        session.append(vec![6u8; 256]).unwrap();
        let enc_path = session.finalize().unwrap();
        let plain_path = session.decrypt_for_upload().unwrap();

        // 上传失败走的就是这条路：明文清掉，密文留着等重传
        session.discard_plaintext();
        assert!(!plain_path.exists(), "失败后不能留下没上锁的明文录音");
        assert!(enc_path.exists(), "密文是唯一的副本，绝不能删");

        // 重传时再解一次就好，内容不变
        assert_eq!(
            std::fs::read(session.decrypt_for_upload().unwrap()).unwrap(),
            vec![6u8; 256]
        );
        // 重复调用不该 panic（purge 里也会再删一次）
        session.discard_plaintext();
        session.discard_plaintext();
    }

    #[test]
    fn local_files_lists_both_copies_for_purge() {
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        session.append(vec![1u8; 32]).unwrap();
        finalize_and_decrypt(&mut session);

        let files = session.local_files();
        assert_eq!(files.len(), 2);
        assert!(files.iter().all(|p| p.exists()), "两份都在: {files:?}");
        // 明文排在前面：中途失败时留下的应该是加密的那份
        assert_eq!(files[0].file_name().unwrap(), PLAINTEXT_SPOOL_NAME);
        assert_eq!(files[1].file_name().unwrap(), ENCRYPTED_SPOOL_NAME);
    }

    #[cfg(not(feature = "cpal"))]
    #[test]
    fn open_mic_fails_cleanly_without_cpal() {
        let dir = tempfile::tempdir().unwrap();
        let mut session = session_in(dir.path());
        let err = session.open_mic().expect_err("降级构建开不了麦");
        assert!(err.contains("cpal"), "错误信息要指明缺的是 cpal: {err}");
        // 开麦失败不影响会话继续用手工喂数据
        assert!(!session.mic_status().active);
        session.append(vec![1u8; 16]).unwrap();
        let path = finalize_and_decrypt(&mut session);
        assert_eq!(std::fs::metadata(&path).unwrap().len(), 16);
    }
}
