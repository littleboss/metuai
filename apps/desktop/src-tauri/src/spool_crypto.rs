//! spool 落盘加密（架构 §5.3）。
//!
//! 这是**设备落盘保护**，不是端到端加密：服务端仍然必须拿到明文 PCM 做审计和
//! `local_fallback` ASR，所以解密发生在客户端上传之前，网关那边契约一个字都不改。
//! 换句话说，这层保护的是「笔记本被拿走 / 同机其他账号翻目录」，不是「不信任服务端」。
//!
//! # 为什么要分帧，而不是整段加密
//!
//! 录音是边录边写的：写盘线程从 channel 收到一小块 PCM 就得落盘，进程崩了也只丢
//! 最后一点。如果整场录音是一个 GCM 消息，就必须把整场（一小时约 330 MB）攒在内存里
//! 才能算出 tag —— 这既吃内存，又违背「边录边落盘」的初衷。
//!
//! 所以**一条 channel 消息 = 一个独立的 GCM 帧**，顺序追加：
//!
//! ```text
//! magic      8 bytes   b"METUAI1\0"
//! 帧 × N，每帧：
//!   nonce    12 bytes  文件随机前缀(8) || 帧序号 u32 大端(4)
//!   ct_len   4 bytes   u32 小端，密文长度（已含 16 字节 GCM tag）
//!   ct       ct_len bytes
//! ```
//!
//! # nonce 为什么是「随机前缀 + 计数器」
//!
//! GCM 最致命的误用是**同一把密钥下 nonce 重复**（会直接泄露明文异或值并让 tag 可伪造）。
//! 每帧都独立随机取 12 字节看似简单，但同一把设备密钥会加密海量帧，碰撞概率随帧数
//! 平方增长。改成「每个文件取一次 8 字节随机前缀，帧序号当低 4 字节」后：
//! 文件内部由计数器保证绝不重复，文件之间由 64 位随机前缀区分。
//!
//! 附带好处：解密时能**推算**出每帧本该用的 nonce，于是帧被重排、删中间几帧这类
//! 篡改会直接暴露（普通格式只是把 nonce 存在文件里，攻击者连 nonce 带密文一起搬走
//! 就查不出来了）。
//!
//! # 刻意不做的事
//!
//! 尾部帧写了一半就崩溃 → 解密直接报 `Truncated`，不做「尽力恢复前面几帧」。
//! 崩溃队列恢复是单独的一块工作，这里宁可明确报错，也不要悄悄交出一段残缺录音。

use aes_gcm::aead::{Aead, KeyInit, Payload};
use aes_gcm::{Aes256Gcm, Nonce};
use sha2::{Digest, Sha256};
use std::fmt;
use std::io::{self, Read, Write};
use std::path::Path;

/// 文件头。既是格式标识，也当每帧的 AAD 用 —— 这样换了格式版本的密文
/// 拿到旧代码里解会直接失败，而不是解出一段乱七八糟的「明文」。
pub const MAGIC: &[u8; 8] = b"METUAI1\0";

/// 设备/会话密钥的环境变量名（64 个 hex 字符 = 32 字节）。
pub const KEY_ENV: &str = "METUAI_SPOOL_KEY";

/// PoC 开发默认密钥的派生材料。**只用于本地开发**：它写在源码里，
/// 等于公开的，生产必须通过 `METUAI_SPOOL_KEY` 下发真密钥。
const DEV_KEY_MATERIAL: &[u8] = b"metuai-poc-dev-spool-key-v1";

/// GCM tag 长度。
const TAG_BYTES: usize = 16;

/// 单帧明文上限。帧本来只有几 KB（一次音频回调的量），这个上限是给**解密**用的：
/// 文件损坏时 `ct_len` 可能是个天文数字，没有上限就会当场申请几 GB 内存。
const MAX_FRAME_BYTES: usize = 8 * 1024 * 1024;

/// 密钥从哪儿来的。上层拿它写审计 / 提醒用户「你还在用开发密钥」。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum KeySource {
    /// 来自 `METUAI_SPOOL_KEY`（本机环境变量，优先）。
    Env,
    /// 来自网关 `GET /v1/device/spool-key`（企业下发 PoC）。
    Enterprise,
    /// 源码里的固定测试密钥（仅本地开发）。
    DevDefault,
}

impl KeySource {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Env => "env",
            Self::Enterprise => "enterprise",
            Self::DevDefault => "dev_default",
        }
    }
}

#[derive(Debug)]
pub enum SpoolCryptoError {
    Io(io::Error),
    /// 文件头对不上：多半根本不是 spool 密文（比如老版本留下的明文 PCM）。
    BadMagic,
    /// 文件在帧中间就结束了。
    Truncated,
    /// `ct_len` 大得不合理，文件已经坏了。
    FrameTooLarge(u64),
    /// 帧序号与推算出的 nonce 对不上：文件被重排 / 删帧。
    OutOfOrder { expected: u32, got: u32 },
    /// GCM 校验失败：密钥不对，或者密文被改过。这两种情况密码学上无法区分。
    Decrypt,
    /// 密钥本身有问题（长度不对、不是 hex）。
    BadKey(String),
}

impl fmt::Display for SpoolCryptoError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Io(e) => write!(f, "io: {e}"),
            Self::BadMagic => write!(f, "not a metuai spool file (bad magic)"),
            Self::Truncated => write!(f, "spool file truncated mid-frame"),
            Self::FrameTooLarge(n) => write!(f, "frame length {n} exceeds {MAX_FRAME_BYTES}"),
            Self::OutOfOrder { expected, got } => {
                write!(f, "frame out of order: expected #{expected}, got #{got}")
            }
            Self::Decrypt => write!(f, "decrypt failed: wrong key or tampered data"),
            Self::BadKey(m) => write!(f, "bad spool key: {m}"),
        }
    }
}

impl std::error::Error for SpoolCryptoError {}

impl From<io::Error> for SpoolCryptoError {
    fn from(e: io::Error) -> Self {
        Self::Io(e)
    }
}

/// 32 字节 AES-256 密钥。
///
/// 手写 `Debug` 而不是 `#[derive]`：派生出来的实现会把密钥字节打进任何
/// `{:?}` / panic 消息里，而架构 §5.3 明确要求凭据不入日志。
#[derive(Clone)]
pub struct SpoolKey([u8; 32]);

impl fmt::Debug for SpoolKey {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("SpoolKey(<redacted>)")
    }
}

impl SpoolKey {
    /// 生产路径的密钥都来自 hex（env / 开发默认），只有测试直接给原始字节。
    #[cfg_attr(not(test), allow(dead_code))]
    pub fn from_bytes(bytes: [u8; 32]) -> Self {
        Self(bytes)
    }

    /// 解析 64 个 hex 字符。
    pub fn from_hex(hex_str: &str) -> Result<Self, SpoolCryptoError> {
        let trimmed = hex_str.trim();
        if trimmed.len() != 64 {
            return Err(SpoolCryptoError::BadKey(format!(
                "expected 64 hex chars, got {}",
                trimmed.len()
            )));
        }
        let raw = hex::decode(trimmed)
            .map_err(|e| SpoolCryptoError::BadKey(format!("not hex: {e}")))?;
        let mut key = [0u8; 32];
        key.copy_from_slice(&raw);
        Ok(Self(key))
    }

    /// 按优先级取密钥：`METUAI_SPOOL_KEY` > 网关企业下发 > PoC 开发默认。
    pub fn resolve() -> Result<(Self, KeySource), SpoolCryptoError> {
        Self::resolve_from(std::env::var(KEY_ENV).ok().as_deref())
    }

    /// 优先本机 env；否则向网关拉企业密钥；再失败才用开发默认。
    pub fn resolve_preferring_enterprise(
        gateway_base: &str,
        employee_jwt: &str,
    ) -> Result<(Self, KeySource), SpoolCryptoError> {
        match std::env::var(KEY_ENV) {
            Ok(v) if !v.trim().is_empty() => Ok((Self::from_hex(&v)?, KeySource::Env)),
            _ => match Self::fetch_enterprise_hex(gateway_base, employee_jwt) {
                Ok(hex) => Ok((Self::from_hex(&hex)?, KeySource::Enterprise)),
                Err(_) => Ok((Self::dev_default(), KeySource::DevDefault)),
            },
        }
    }

    /// 从网关拉 `spool_key_hex`。网关未配置密钥时返回 Err（调用方退回开发默认）。
    pub fn fetch_enterprise_hex(gateway_base: &str, employee_jwt: &str) -> Result<String, String> {
        let base = gateway_base.trim_end_matches('/');
        if base.is_empty() || employee_jwt.is_empty() {
            return Err("missing gateway or jwt".into());
        }
        let url = format!("{base}/v1/device/spool-key");
        let resp = ureq::get(&url)
            .set("Authorization", &format!("Bearer {employee_jwt}"))
            .call()
            .map_err(|e| e.to_string())?;
        if resp.status() != 200 {
            return Err(format!("spool-key http {}", resp.status()));
        }
        let body: serde_json::Value = resp.into_json().map_err(|e| e.to_string())?;
        body.get("spool_key_hex")
            .and_then(|v| v.as_str())
            .map(|s| s.to_string())
            .filter(|s| !s.trim().is_empty())
            .ok_or_else(|| "spool_key_hex missing".into())
    }

    /// `resolve` 的纯函数版本。
    ///
    /// 之所以把「读环境变量」这一步单独抽出去：环境变量是进程全局的，
    /// 测试里改它会影响并行跑的其他测试。分开之后取值逻辑可以随便测。
    ///
    /// 注意：env 设了但**格式不对时直接报错**，绝不悄悄退回开发密钥 ——
    /// 否则运维以为下发了真密钥，实际文件是用源码里那把公开密钥加的。
    pub fn resolve_from(env_value: Option<&str>) -> Result<(Self, KeySource), SpoolCryptoError> {
        match env_value {
            Some(v) if !v.trim().is_empty() => Ok((Self::from_hex(v)?, KeySource::Env)),
            _ => Ok((Self::dev_default(), KeySource::DevDefault)),
        }
    }

    /// 开发默认密钥：把固定材料过一遍 sha256 凑够 32 字节。
    /// 不是安全设计，只是让 PoC 在没配环境变量时也能跑通全流程。
    pub fn dev_default() -> Self {
        let mut hasher = Sha256::new();
        hasher.update(DEV_KEY_MATERIAL);
        let mut key = [0u8; 32];
        key.copy_from_slice(&hasher.finalize());
        Self(key)
    }

    fn cipher(&self) -> Aes256Gcm {
        Aes256Gcm::new(self.0.as_slice().into())
    }
}

/// 拼出第 `index` 帧的 nonce：随机前缀(8) || 序号大端(4)。
fn nonce_for(prefix: &[u8; 8], index: u32) -> [u8; 12] {
    let mut nonce = [0u8; 12];
    nonce[..8].copy_from_slice(prefix);
    nonce[8..].copy_from_slice(&index.to_be_bytes());
    nonce
}

/// 边写边加密。每调用一次 `write_frame` 就产生一个独立的 GCM 帧。
///
/// 泛型 `W: Write` 而不是写死 `File`：写盘线程外面已经套了 `BufWriter`，
/// 测试里则直接套 `Vec<u8>`，两边走的是同一份代码。
pub struct SpoolEncryptor<W: Write> {
    inner: W,
    cipher: Aes256Gcm,
    prefix: [u8; 8],
    next_index: u32,
}

impl<W: Write> SpoolEncryptor<W> {
    /// 建加密写入器，并立刻把 magic 写进去（空录音也会留下一个合法的头）。
    pub fn new(mut inner: W, key: &SpoolKey) -> Result<Self, SpoolCryptoError> {
        let prefix = random_prefix();
        inner.write_all(MAGIC)?;
        Ok(Self {
            inner,
            cipher: key.cipher(),
            prefix,
            next_index: 0,
        })
    }

    /// 加密一块 PCM 并追加。空输入直接跳过 —— 空帧只会白白多 32 字节头。
    pub fn write_frame(&mut self, plain: &[u8]) -> Result<(), SpoolCryptoError> {
        if plain.is_empty() {
            return Ok(());
        }
        if plain.len() > MAX_FRAME_BYTES {
            return Err(SpoolCryptoError::FrameTooLarge(plain.len() as u64));
        }
        let nonce = nonce_for(&self.prefix, self.next_index);
        let ciphertext = self
            .cipher
            .encrypt(
                Nonce::from_slice(&nonce),
                Payload {
                    msg: plain,
                    aad: MAGIC,
                },
            )
            // 加密只会因为「一帧太大」失败，上面已经拦过一次，这里是兜底
            .map_err(|_| SpoolCryptoError::FrameTooLarge(plain.len() as u64))?;

        self.inner.write_all(&nonce)?;
        self.inner
            .write_all(&(ciphertext.len() as u32).to_le_bytes())?;
        self.inner.write_all(&ciphertext)?;

        // 计数器耗尽就必须停：继续下去会重用 nonce，那是 GCM 最严重的误用。
        // 4 294 967 295 帧在现实里到不了，但这条分支比一句注释可靠。
        self.next_index = self
            .next_index
            .checked_add(1)
            .ok_or_else(|| SpoolCryptoError::BadKey("frame counter exhausted".into()))?;
        Ok(())
    }

    /// 交还底层 writer（由调用方负责 flush / fsync）。
    pub fn into_inner(self) -> W {
        self.inner
    }
}

/// 8 字节随机前缀。用 uuid v4（内部走 getrandom，即操作系统 CSPRNG）。
fn random_prefix() -> [u8; 8] {
    let bytes = uuid::Uuid::new_v4().into_bytes();
    let mut prefix = [0u8; 8];
    prefix.copy_from_slice(&bytes[..8]);
    prefix
}

/// 顺序解密整个文件流，把明文写进 `dst`，返回明文字节数。
///
/// 调用方负责 `dst` 的权限（明文 PCM 同样敏感，必须 0600）。
pub fn decrypt_stream<R: Read, W: Write>(
    src: R,
    dst: &mut W,
    key: &SpoolKey,
) -> Result<u64, SpoolCryptoError> {
    let mut reader = io::BufReader::new(src);
    let mut magic = [0u8; 8];
    // 连 8 字节头都读不满 → 不可能是合法密文
    reader
        .read_exact(&mut magic)
        .map_err(|_| SpoolCryptoError::BadMagic)?;
    if &magic != MAGIC {
        return Err(SpoolCryptoError::BadMagic);
    }

    let cipher = key.cipher();
    let mut prefix: Option<[u8; 8]> = None;
    let mut index: u32 = 0;
    let mut written: u64 = 0;

    loop {
        let mut nonce = [0u8; 12];
        match read_full(&mut reader, &mut nonce)? {
            0 => break,             // 正好在帧边界结束 = 正常读完
            12 => {}                // 完整的 nonce
            _ => return Err(SpoolCryptoError::Truncated),
        }

        let file_prefix = *prefix.get_or_insert_with(|| {
            let mut p = [0u8; 8];
            p.copy_from_slice(&nonce[..8]);
            p
        });
        // 推算这一帧本该用的 nonce，对不上就说明帧被重排或删掉过
        let expected = nonce_for(&file_prefix, index);
        if nonce != expected {
            let got = u32::from_be_bytes([nonce[8], nonce[9], nonce[10], nonce[11]]);
            return Err(SpoolCryptoError::OutOfOrder { expected: index, got });
        }

        let mut len_bytes = [0u8; 4];
        if read_full(&mut reader, &mut len_bytes)? != 4 {
            return Err(SpoolCryptoError::Truncated);
        }
        let ct_len = u32::from_le_bytes(len_bytes) as usize;
        // 密文至少要装得下一个 tag；上限防止坏文件让我们申请海量内存
        if ct_len < TAG_BYTES || ct_len > MAX_FRAME_BYTES + TAG_BYTES {
            return Err(SpoolCryptoError::FrameTooLarge(ct_len as u64));
        }
        let mut ciphertext = vec![0u8; ct_len];
        if read_full(&mut reader, &mut ciphertext)? != ct_len {
            return Err(SpoolCryptoError::Truncated);
        }

        let plain = cipher
            .decrypt(
                Nonce::from_slice(&nonce),
                Payload {
                    msg: &ciphertext,
                    aad: MAGIC,
                },
            )
            .map_err(|_| SpoolCryptoError::Decrypt)?;
        dst.write_all(&plain)?;
        written += plain.len() as u64;

        index = index
            .checked_add(1)
            .ok_or_else(|| SpoolCryptoError::FrameTooLarge(u32::MAX as u64))?;
    }

    dst.flush()?;
    Ok(written)
}

/// 读满 `buf`，返回实际读到多少字节。
///
/// `Read::read` 允许「读到一半就返回」（网络流、管道都会这样），
/// 所以不能用一次 `read` 的返回值判断文件结束，必须自己循环补满。
/// `read_exact` 又区分不了「干净结束」和「读了一半断掉」，而我们两者都要认。
fn read_full<R: Read>(reader: &mut R, buf: &mut [u8]) -> io::Result<usize> {
    let mut filled = 0;
    while filled < buf.len() {
        match reader.read(&mut buf[filled..]) {
            Ok(0) => break,
            Ok(n) => filled += n,
            Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
            Err(e) => return Err(e),
        }
    }
    Ok(filled)
}

/// 这个文件看起来是 spool 密文吗（只看 magic，不解密）。
///
/// 给「兼容老版本留下的明文 spool」用：读不到 / 头不对就当明文处理。
pub fn file_looks_encrypted(path: &Path) -> bool {
    let mut file = match std::fs::File::open(path) {
        Ok(f) => f,
        Err(_) => return false,
    };
    let mut head = [0u8; 8];
    read_full(&mut file, &mut head).map(|n| n == 8).unwrap_or(false) && &head == MAGIC
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 把几段明文加密成一个完整的 spool 文件（内存里）。
    fn encrypt_all(frames: &[&[u8]], key: &SpoolKey) -> Vec<u8> {
        let mut enc = SpoolEncryptor::new(Vec::new(), key).unwrap();
        for f in frames {
            enc.write_frame(f).unwrap();
        }
        enc.into_inner()
    }

    fn decrypt_all(blob: &[u8], key: &SpoolKey) -> Result<Vec<u8>, SpoolCryptoError> {
        let mut out = Vec::new();
        decrypt_stream(io::Cursor::new(blob), &mut out, key)?;
        Ok(out)
    }

    fn test_key() -> SpoolKey {
        SpoolKey::from_bytes([7u8; 32])
    }

    #[test]
    fn round_trip_preserves_every_byte_in_order() {
        let key = test_key();
        let a = vec![1u8; 1000];
        let b = vec![2u8; 37];
        let c = vec![3u8; 4096];
        let blob = encrypt_all(&[&a, &b, &c], &key);

        let mut expected = a.clone();
        expected.extend_from_slice(&b);
        expected.extend_from_slice(&c);
        assert_eq!(decrypt_all(&blob, &key).unwrap(), expected, "顺序和内容都要保住");
    }

    #[test]
    fn encrypted_file_is_not_recognizable_pcm() {
        let key = test_key();
        // 一段有明显规律的假 PCM：如果落盘时没加密，它会原样出现在文件里
        let pcm: Vec<u8> = (0..4096u32).map(|i| (i % 251) as u8).collect();
        let blob = encrypt_all(&[&pcm], &key);

        assert_eq!(&blob[..8], MAGIC, "文件要自描述，头 8 字节是 magic");
        assert!(
            !blob.windows(64).any(|w| w == &pcm[..64]),
            "密文里不该出现明文片段"
        );
        assert!(blob.len() > pcm.len(), "至少多出 nonce + 长度 + tag");
    }

    #[test]
    fn wrong_key_cannot_decrypt() {
        let blob = encrypt_all(&[b"secret audio"], &test_key());
        let wrong = SpoolKey::from_bytes([8u8; 32]);
        assert!(
            matches!(decrypt_all(&blob, &wrong), Err(SpoolCryptoError::Decrypt)),
            "换一把密钥必须解不开，而不是解出乱码"
        );
    }

    #[test]
    fn dev_default_key_differs_from_a_real_key() {
        let blob = encrypt_all(&[b"secret audio"], &SpoolKey::dev_default());
        assert!(matches!(
            decrypt_all(&blob, &test_key()),
            Err(SpoolCryptoError::Decrypt)
        ));
        // 开发密钥本身要稳定，否则重启一次就解不开上一次录的文件
        assert!(decrypt_all(&blob, &SpoolKey::dev_default()).is_ok());
    }

    #[test]
    fn tampered_ciphertext_is_rejected() {
        let key = test_key();
        let mut blob = encrypt_all(&[&vec![5u8; 512]], &key);
        // 翻掉密文里的一个 bit —— GCM 的 tag 就是用来发现这种事的
        let last = blob.len() - 1;
        blob[last] ^= 0x01;
        assert!(matches!(decrypt_all(&blob, &key), Err(SpoolCryptoError::Decrypt)));
    }

    #[test]
    fn plain_pcm_file_is_rejected_by_magic() {
        let key = test_key();
        let plain = vec![0u8; 256];
        assert!(matches!(decrypt_all(&plain, &key), Err(SpoolCryptoError::BadMagic)));
        // 空文件同样不是合法密文
        assert!(matches!(decrypt_all(&[], &key), Err(SpoolCryptoError::BadMagic)));
    }

    #[test]
    fn truncated_frame_is_rejected() {
        let key = test_key();
        let blob = encrypt_all(&[&vec![9u8; 800]], &key);
        // 砍掉最后 10 字节：模拟写到一半崩溃
        let cut = &blob[..blob.len() - 10];
        assert!(matches!(decrypt_all(cut, &key), Err(SpoolCryptoError::Truncated)));
    }

    #[test]
    fn reordered_frames_are_detected() {
        let key = test_key();
        // 两帧等长，交换起来正好是整块搬移，模拟攻击者调换录音片段顺序
        let frame_a = vec![1u8; 100];
        let frame_b = vec![2u8; 100];
        let blob = encrypt_all(&[&frame_a, &frame_b], &key);

        let frame_len = 12 + 4 + 100 + TAG_BYTES;
        let mut swapped = Vec::from(MAGIC.as_slice());
        swapped.extend_from_slice(&blob[8 + frame_len..8 + 2 * frame_len]);
        swapped.extend_from_slice(&blob[8..8 + frame_len]);

        assert!(
            matches!(
                decrypt_all(&swapped, &key),
                Err(SpoolCryptoError::OutOfOrder { .. })
            ),
            "帧序号由 nonce 推算，换顺序必须被抓到"
        );
    }

    #[test]
    fn corrupt_length_field_does_not_allocate_wildly() {
        let key = test_key();
        let mut blob = encrypt_all(&[&vec![4u8; 64]], &key);
        // 把 ct_len 改成 4 GB：没有上限保护的话这里会当场申请 4 GB 内存
        blob[8 + 12..8 + 16].copy_from_slice(&u32::MAX.to_le_bytes());
        assert!(matches!(
            decrypt_all(&blob, &key),
            Err(SpoolCryptoError::FrameTooLarge(_))
        ));
    }

    #[test]
    fn empty_recording_yields_header_only_file() {
        let key = test_key();
        let blob = encrypt_all(&[], &key);
        assert_eq!(blob.len(), 8, "0 字节录音只留一个 magic 头");
        assert_eq!(decrypt_all(&blob, &key).unwrap(), Vec::<u8>::new());
        // 空 buffer 不产生帧，否则每次都会白白多 32 字节
        assert_eq!(encrypt_all(&[b""], &key).len(), 8);
    }

    #[test]
    fn nonces_never_repeat_within_a_file() {
        let key = test_key();
        let blob = encrypt_all(&[b"aa", b"bb", b"cc"], &key);
        let frame_len = 12 + 4 + 2 + TAG_BYTES;
        let nonces: Vec<&[u8]> = (0..3)
            .map(|i| &blob[8 + i * frame_len..8 + i * frame_len + 12])
            .collect();
        assert_ne!(nonces[0], nonces[1]);
        assert_ne!(nonces[1], nonces[2]);
        // 前 8 字节是同一个文件前缀，后 4 字节是递增序号
        assert_eq!(&nonces[0][..8], &nonces[2][..8]);
        assert_eq!(&nonces[2][8..], &2u32.to_be_bytes());
    }

    #[test]
    fn each_file_gets_its_own_random_prefix() {
        let key = test_key();
        let first = encrypt_all(&[b"same input"], &key);
        let second = encrypt_all(&[b"same input"], &key);
        assert_ne!(
            &first[8..16],
            &second[8..16],
            "两个文件用同一把密钥，nonce 前缀必须不同"
        );
        assert_ne!(first, second, "同样的明文不能加出同样的密文");
    }

    #[test]
    fn key_hex_round_trips() {
        let hex_str = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff";
        let key = SpoolKey::from_hex(hex_str).expect("64 hex chars");
        let blob = encrypt_all(&[b"x"], &key);
        let same = SpoolKey::from_hex(&hex_str.to_uppercase()).expect("hex 大小写都收");
        assert!(decrypt_all(&blob, &same).is_ok());
    }

    #[test]
    fn malformed_keys_are_rejected() {
        for bad in ["", "abc", &"z".repeat(64), &"ab".repeat(20)] {
            assert!(
                matches!(SpoolKey::from_hex(bad), Err(SpoolCryptoError::BadKey(_))),
                "{bad:?} 不是合法密钥"
            );
        }
    }

    #[test]
    fn env_key_wins_and_bad_env_key_never_falls_back() {
        let hex_str = "11".repeat(32);
        let (key, source) = SpoolKey::resolve_from(Some(&hex_str)).unwrap();
        assert_eq!(source, KeySource::Env);
        let blob = encrypt_all(&[b"x"], &key);
        assert!(decrypt_all(&blob, &SpoolKey::from_hex(&hex_str).unwrap()).is_ok());

        // 没设 / 设了空串 → 开发默认
        assert_eq!(SpoolKey::resolve_from(None).unwrap().1, KeySource::DevDefault);
        assert_eq!(
            SpoolKey::resolve_from(Some("  ")).unwrap().1,
            KeySource::DevDefault
        );
        // 设了但写错 → 报错，绝不悄悄用开发密钥
        assert!(SpoolKey::resolve_from(Some("nonsense")).is_err());
    }

    #[test]
    fn key_debug_output_never_leaks_material() {
        let key = SpoolKey::from_bytes([0xABu8; 32]);
        let printed = format!("{key:?}");
        assert!(!printed.contains("ab"), "密钥不能进日志: {printed}");
        assert!(printed.contains("redacted"));
    }

    #[test]
    fn magic_detection_works_on_real_files() {
        let dir = tempfile::tempdir().unwrap();
        let enc_path = dir.path().join("mic.pcm.enc");
        std::fs::write(&enc_path, encrypt_all(&[b"hello"], &test_key())).unwrap();
        let plain_path = dir.path().join("mic.pcm");
        std::fs::write(&plain_path, vec![0u8; 100]).unwrap();

        assert!(file_looks_encrypted(&enc_path));
        assert!(!file_looks_encrypted(&plain_path));
        assert!(!file_looks_encrypted(&dir.path().join("nope")));
        // 比 magic 还短的文件不能误判
        let tiny = dir.path().join("tiny");
        std::fs::write(&tiny, b"MET").unwrap();
        assert!(!file_looks_encrypted(&tiny));
    }

    #[test]
    fn many_small_frames_round_trip() {
        // 真实录音就是几千个小 buffer，逐帧加密不能在边界上出错
        let key = test_key();
        let frames: Vec<Vec<u8>> = (0..500u32).map(|i| vec![(i % 256) as u8; 64]).collect();
        let refs: Vec<&[u8]> = frames.iter().map(|f| f.as_slice()).collect();
        let blob = encrypt_all(&refs, &key);
        let expected: Vec<u8> = frames.concat();
        assert_eq!(decrypt_all(&blob, &key).unwrap(), expected);
    }
}
