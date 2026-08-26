//! 崩溃 / 上传失败后的待传队列（架构 §5.2：客户端重启后能恢复待上传队列）。
//!
//! 只持久化「哪场会、哪个 upload_id、spool 目录、网关基址」——**不写 JWT**。
//! 恢复上传时由前端再次传入员工令牌。

use serde::{Deserialize, Serialize};
use std::fs;
use std::io;
use std::path::{Path, PathBuf};

pub const QUEUE_FILE_NAME: &str = "pending_uploads.json";

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct PendingUpload {
    pub meeting_id: String,
    pub upload_id: String,
    pub gateway_base: String,
    pub spool_dir: String,
    /// 加密 spool 路径（mic.pcm.enc）。
    pub encrypted_path: String,
    pub updated_at_ms: u64,
}

#[derive(Debug, Default, Serialize, Deserialize)]
struct QueueFile {
    items: Vec<PendingUpload>,
}

fn now_ms() -> u64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

pub fn queue_path(app_data: &Path) -> PathBuf {
    app_data.join(QUEUE_FILE_NAME)
}

fn load(path: &Path) -> QueueFile {
    let Ok(bytes) = fs::read(path) else {
        return QueueFile::default();
    };
    serde_json::from_slice(&bytes).unwrap_or_default()
}

fn save(path: &Path, q: &QueueFile) -> io::Result<()> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    let bytes = serde_json::to_vec_pretty(q).map_err(|e| io::Error::new(io::ErrorKind::Other, e))?;
    let tmp = path.with_extension("json.tmp");
    fs::write(&tmp, bytes)?;
    fs::rename(tmp, path)?;
    Ok(())
}

/// 登记或更新一条待传（同 upload_id 覆盖）。
pub fn upsert(app_data: &Path, mut item: PendingUpload) -> io::Result<()> {
    item.updated_at_ms = now_ms();
    let path = queue_path(app_data);
    let mut q = load(&path);
    if let Some(existing) = q.items.iter_mut().find(|x| x.upload_id == item.upload_id) {
        *existing = item;
    } else {
        q.items.push(item);
    }
    save(&path, &q)
}

/// 上传成功或 purge 后移除。
pub fn remove(app_data: &Path, upload_id: &str) -> io::Result<()> {
    let path = queue_path(app_data);
    let mut q = load(&path);
    let before = q.items.len();
    q.items.retain(|x| x.upload_id != upload_id);
    if q.items.len() != before {
        save(&path, &q)?;
    }
    Ok(())
}

pub fn list(app_data: &Path) -> Vec<PendingUpload> {
    load(&queue_path(app_data)).items
}

pub fn get(app_data: &Path, upload_id: &str) -> Option<PendingUpload> {
    list(app_data).into_iter().find(|x| x.upload_id == upload_id)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn upsert_list_remove_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let item = PendingUpload {
            meeting_id: "mtg_1".into(),
            upload_id: "up_1".into(),
            gateway_base: "http://127.0.0.1:18080".into(),
            spool_dir: dir.path().join("spool").to_string_lossy().into(),
            encrypted_path: dir.path().join("mic.pcm.enc").to_string_lossy().into(),
            updated_at_ms: 0,
        };
        upsert(dir.path(), item.clone()).unwrap();
        upsert(
            dir.path(),
            PendingUpload {
                gateway_base: "http://127.0.0.1:18081".into(),
                ..item.clone()
            },
        )
        .unwrap();
        let items = list(dir.path());
        assert_eq!(items.len(), 1);
        assert_eq!(items[0].gateway_base, "http://127.0.0.1:18081");
        remove(dir.path(), "up_1").unwrap();
        assert!(list(dir.path()).is_empty());
    }
}
