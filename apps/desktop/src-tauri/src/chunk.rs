//! 分块与 checksum。
//!
//! 网关契约（services/gateway/internal/upload）：
//! - 每块单独算 sha256 hex，放在 `X-Checksum-Sha256` 头里 PUT 上去；
//! - 服务端把 0..parts-1 合并后重算整文件 sha256，客户端要拿它跟本地的比。
//!
//! 这里刻意不碰音频，也不碰 HTTP：给一段字节就能测。

use sha2::{Digest, Sha256};
use std::fs::File;
use std::io::{self, Read, Seek, SeekFrom};
use std::path::{Path, PathBuf};

/// 网关单块硬上限，客户端自己先夹一次，别等 400。
/// 对应 `services/gateway/internal/upload/http.go` 里的 `io.LimitReader(body, 8<<20)`。
pub const MAX_CHUNK_BYTES: usize = 8 * 1024 * 1024;

/// 块大小下限。太小的块会让一场会产生几万个请求，光 HTTP 头就够呛人了。
pub const MIN_CHUNK_BYTES: usize = 16 * 1024;

/// 算一段字节的 sha256，返回小写 hex（跟 Go 侧 `hex.EncodeToString` 对齐）。
pub fn sha256_hex(bytes: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(bytes);
    hex::encode(hasher.finalize())
}

/// 流式算整个文件的 sha256。
///
/// 为什么不用 `sha256_hex(&std::fs::read(path)?)`：一小时会议的 s16le/48k 单声道
/// 大约 330 MB，整读进内存就是白白吃掉一块内存。这里每次只读 64 KiB。
pub fn sha256_file(path: &Path) -> io::Result<String> {
    let mut file = File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buf = vec![0u8; 64 * 1024];
    loop {
        let read = file.read(&mut buf)?;
        if read == 0 {
            break; // 读到 0 字节 = 文件结束
        }
        hasher.update(&buf[..read]);
    }
    Ok(hex::encode(hasher.finalize()))
}

/// 按「秒数」折算块大小。
///
/// 只接受「每秒多少字节」，而不是自己再乘一遍 sample_rate × channels × 位深 ——
/// 那个乘法归 `AudioFormat::bytes_per_second()` 管，同一个公式写两遍早晚会写歪一个。
///
/// 结果夹在 [MIN_CHUNK_BYTES, MAX_CHUNK_BYTES] 之间，避免配置写歪了产生极端块。
pub fn chunk_bytes_for_duration(bytes_per_second: usize, seconds: u32) -> usize {
    let raw = bytes_per_second as u64 * seconds.max(1) as u64;
    (raw as usize).clamp(MIN_CHUNK_BYTES, MAX_CHUNK_BYTES)
}

/// 总长度会被切成几块（向上取整）。空文件算 0 块。
pub fn part_count(total_len: u64, chunk_bytes: usize) -> usize {
    let size = chunk_bytes.max(1) as u64;
    ((total_len + size - 1) / size) as usize
}

/// 内存切块：最后一块允许比 chunk_bytes 短。
///
/// 生产路径走 `FileChunks`（按偏移 seek，不把整个录音读进内存），
/// 所以这个只在测试里当「内存版参照实现」用。
#[cfg(test)]
pub fn split(data: &[u8], chunk_bytes: usize) -> Vec<&[u8]> {
    if data.is_empty() {
        return Vec::new();
    }
    data.chunks(chunk_bytes.max(1)).collect()
}

/// 上传源：按下标取块。抽成 trait 是为了测试能用内存假数据，
/// 生产走文件，不必把整个录音读进内存。
pub trait ChunkSource {
    fn parts(&self) -> usize;
    fn total_len(&self) -> u64;
    fn chunk(&self, index: usize) -> io::Result<Vec<u8>>;
}

/// 落盘录音文件的分块视图（生产路径）。
pub struct FileChunks {
    path: PathBuf,
    chunk_bytes: usize,
    total_len: u64,
}

impl FileChunks {
    pub fn new(path: impl Into<PathBuf>, chunk_bytes: usize) -> io::Result<Self> {
        let path = path.into();
        let total_len = std::fs::metadata(&path)?.len();
        Ok(Self {
            path,
            chunk_bytes: chunk_bytes.clamp(1, MAX_CHUNK_BYTES),
            total_len,
        })
    }
}

impl ChunkSource for FileChunks {
    fn parts(&self) -> usize {
        part_count(self.total_len, self.chunk_bytes)
    }

    fn total_len(&self) -> u64 {
        self.total_len
    }

    fn chunk(&self, index: usize) -> io::Result<Vec<u8>> {
        let offset = index as u64 * self.chunk_bytes as u64;
        if offset >= self.total_len {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                format!("chunk {index} out of range"),
            ));
        }
        let take = std::cmp::min(self.chunk_bytes as u64, self.total_len - offset) as usize;
        let mut file = File::open(&self.path)?;
        file.seek(SeekFrom::Start(offset))?;
        let mut buf = vec![0u8; take];
        file.read_exact(&mut buf)?;
        Ok(buf)
    }
}

/// 内存分块视图。只在测试里用：给上传驱动喂一段假 PCM，不用先落盘。
#[cfg(test)]
pub struct MemoryChunks {
    data: Vec<u8>,
    chunk_bytes: usize,
}

#[cfg(test)]
impl MemoryChunks {
    pub fn new(data: Vec<u8>, chunk_bytes: usize) -> Self {
        Self {
            data,
            chunk_bytes: chunk_bytes.max(1),
        }
    }
}

#[cfg(test)]
impl ChunkSource for MemoryChunks {
    fn parts(&self) -> usize {
        part_count(self.data.len() as u64, self.chunk_bytes)
    }

    fn total_len(&self) -> u64 {
        self.data.len() as u64
    }

    fn chunk(&self, index: usize) -> io::Result<Vec<u8>> {
        split(&self.data, self.chunk_bytes)
            .get(index)
            .map(|c| c.to_vec())
            .ok_or_else(|| {
                io::Error::new(
                    io::ErrorKind::InvalidInput,
                    format!("chunk {index} out of range"),
                )
            })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sha256_matches_known_vector() {
        // 标准测试向量：sha256("abc")
        assert_eq!(
            sha256_hex(b"abc"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
        // 空输入也得是稳定值（网关那边空块会被拒，但 hash 本身要对）
        assert_eq!(
            sha256_hex(b""),
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        );
    }

    #[test]
    fn sha256_file_matches_in_memory_hash() {
        // 流式与整读必须得出同一个值，否则 complete 时跟服务端对不上
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("pcm.raw");
        // 特意超过 64 KiB 的读缓冲，逼出「多轮 update」的分支
        let data: Vec<u8> = (0..200_000u32).map(|i| (i % 251) as u8).collect();
        std::fs::write(&path, &data).unwrap();
        assert_eq!(sha256_file(&path).unwrap(), sha256_hex(&data));
    }

    #[test]
    fn sha256_file_handles_empty_file() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("empty.raw");
        std::fs::write(&path, b"").unwrap();
        assert_eq!(sha256_file(&path).unwrap(), sha256_hex(b""));
    }

    #[test]
    fn sha256_is_lowercase_hex_of_32_bytes() {
        let sum = sha256_hex(&[0u8; 4096]);
        assert_eq!(sum.len(), 64, "sha256 hex 一定是 64 个字符");
        assert!(sum.chars().all(|c| c.is_ascii_hexdigit() && !c.is_uppercase()));
    }

    #[test]
    fn split_last_chunk_is_short() {
        let data: Vec<u8> = (0..250u32).map(|i| i as u8).collect();
        let chunks = split(&data, 100);
        assert_eq!(chunks.len(), 3);
        assert_eq!(chunks[0].len(), 100);
        assert_eq!(chunks[1].len(), 100);
        assert_eq!(chunks[2].len(), 50);
        // 拼回去必须和原始数据完全一致，否则服务端合并的 checksum 不会对
        let rejoined: Vec<u8> = chunks.concat();
        assert_eq!(rejoined, data);
    }

    #[test]
    fn split_exact_multiple_has_no_empty_tail() {
        let data = vec![7u8; 200];
        let chunks = split(&data, 100);
        assert_eq!(chunks.len(), 2);
        assert!(chunks.iter().all(|c| c.len() == 100));
    }

    #[test]
    fn split_empty_input_yields_no_chunks() {
        assert!(split(&[], 100).is_empty());
        assert_eq!(part_count(0, 100), 0);
    }

    #[test]
    fn part_count_rounds_up() {
        assert_eq!(part_count(1, 100), 1);
        assert_eq!(part_count(100, 100), 1);
        assert_eq!(part_count(101, 100), 2);
    }

    #[test]
    fn chunk_bytes_for_duration_is_clamped() {
        // 48 kHz 单声道 s16le = 96000 字节/秒，3 秒 = 288000
        assert_eq!(chunk_bytes_for_duration(96_000, 3), 288_000);
        // 太小的配置被抬到下限（8 kHz 单声道 1 秒只有 16000 字节）
        assert_eq!(chunk_bytes_for_duration(16_000, 1), MIN_CHUNK_BYTES);
        // 太大的配置被压到网关上限
        assert_eq!(chunk_bytes_for_duration(384_000, 600), MAX_CHUNK_BYTES);
        // 0 秒当 1 秒处理，不能返回 0（否则 part_count 会除零）
        assert!(chunk_bytes_for_duration(96_000, 0) >= MIN_CHUNK_BYTES);
    }

    #[test]
    fn memory_chunks_round_trip() {
        let data: Vec<u8> = (0..1000u32).map(|i| (i % 251) as u8).collect();
        let src = MemoryChunks::new(data.clone(), 256);
        assert_eq!(src.parts(), 4);
        assert_eq!(src.total_len(), 1000);
        let mut got = Vec::new();
        for i in 0..src.parts() {
            got.extend_from_slice(&src.chunk(i).unwrap());
        }
        assert_eq!(got, data);
        assert!(src.chunk(4).is_err(), "越界取块要报错");
    }

    #[test]
    fn file_chunks_match_memory_chunks() {
        let data: Vec<u8> = (0..5000u32).map(|i| (i % 97) as u8).collect();
        let dir = std::env::temp_dir().join(format!("metuai-chunk-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("pcm.raw");
        std::fs::write(&path, &data).unwrap();

        let file_src = FileChunks::new(&path, 1024).unwrap();
        let mem_src = MemoryChunks::new(data.clone(), 1024);
        assert_eq!(file_src.parts(), mem_src.parts());
        for i in 0..file_src.parts() {
            assert_eq!(file_src.chunk(i).unwrap(), mem_src.chunk(i).unwrap());
        }
        // 整文件 checksum 应等于逐块拼接后的 checksum（服务端就是这么合的）
        let joined: Vec<u8> = (0..file_src.parts())
            .map(|i| file_src.chunk(i).unwrap())
            .collect::<Vec<_>>()
            .concat();
        assert_eq!(sha256_hex(&joined), sha256_hex(&data));

        std::fs::remove_dir_all(&dir).ok();
    }
}
