//! 员工本机麦克风备份状态机（架构文档 §5.1）。
//!
//! 只描述「状态」和「允许的迁移」，不碰音频设备、文件、网络。
//! 这样上层（capture / session / uploader）怎么变，这里都能单测。

use std::time::SystemTime;

/// 录音会话状态。暂停必须写审计缺口；静音不是暂停。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RecordingState {
    Idle,
    Recording,
    Paused,
    Finalizing,
    Queued,
    Uploading,
    Verifying,
    Acked,
    PurgePending,
    Purged,
    RetryWait,
    UploadFailed,
}

impl RecordingState {
    /// 给 UI / 审计用的稳定名字（与架构文档里的大写状态名一致）。
    /// 不要用 `format!("{:?}")` 当协议，那个跟 Rust 标识符绑死了。
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Idle => "IDLE",
            Self::Recording => "RECORDING",
            Self::Paused => "PAUSED",
            Self::Finalizing => "FINALIZING",
            Self::Queued => "QUEUED",
            Self::Uploading => "UPLOADING",
            Self::Verifying => "VERIFYING",
            Self::Acked => "ACKED",
            Self::PurgePending => "PURGE_PENDING",
            Self::Purged => "PURGED",
            Self::RetryWait => "RETRY_WAIT",
            Self::UploadFailed => "UPLOAD_FAILED",
        }
    }

    /// 开始录音：只能从 IDLE 起步（防止重复 start 覆盖正在录的会话）。
    pub fn start(self) -> Option<Self> {
        match self {
            Self::Idle => Some(Self::Recording),
            _ => None,
        }
    }

    /// 用户点击暂停：仅 Recording → Paused。
    pub fn pause(self) -> Option<Self> {
        match self {
            Self::Recording => Some(Self::Paused),
            _ => None,
        }
    }

    /// 用户恢复录音。
    pub fn resume(self) -> Option<Self> {
        match self {
            Self::Paused => Some(Self::Recording),
            _ => None,
        }
    }

    /// 停止采集，收尾落盘（算整文件 checksum、关文件句柄）。
    pub fn finalize(self) -> Option<Self> {
        match self {
            Self::Recording | Self::Paused => Some(Self::Finalizing),
            _ => None,
        }
    }

    /// 收尾完成，进上传队列。进程重启后也是从 QUEUED 恢复。
    pub fn queue(self) -> Option<Self> {
        match self {
            Self::Finalizing | Self::UploadFailed => Some(Self::Queued),
            _ => None,
        }
    }

    /// 开始/继续传分块。RETRY_WAIT 退避结束后回到这里。
    pub fn upload(self) -> Option<Self> {
        match self {
            Self::Queued | Self::RetryWait | Self::Uploading => Some(Self::Uploading),
            _ => None,
        }
    }

    /// 分块全部传完，等服务端合并后回报整文件 checksum。
    pub fn verify(self) -> Option<Self> {
        match self {
            Self::Uploading => Some(Self::Verifying),
            _ => None,
        }
    }

    /// 服务端 checksum 与本地一致。
    pub fn ack(self) -> Option<Self> {
        match self {
            Self::Verifying => Some(Self::Acked),
            _ => None,
        }
    }

    /// 可重试的失败（网络抖动、5xx）：退避后重来。
    pub fn retry_wait(self) -> Option<Self> {
        match self {
            Self::Uploading | Self::Verifying => Some(Self::RetryWait),
            _ => None,
        }
    }

    /// 不可恢复或重试次数用尽。本地文件必须保留。
    pub fn fail(self) -> Option<Self> {
        match self {
            Self::Queued | Self::Uploading | Self::Verifying | Self::RetryWait => {
                Some(Self::UploadFailed)
            }
            _ => None,
        }
    }

    /// 已确认，按管理员策略等待清理。
    pub fn request_purge(self) -> Option<Self> {
        match self {
            Self::Acked => Some(Self::PurgePending),
            _ => None,
        }
    }

    /// 本地副本已删除。
    pub fn purge(self) -> Option<Self> {
        match self {
            Self::PurgePending => Some(Self::Purged),
            _ => None,
        }
    }

    /// 结清的会话归零，让下一场会可以重新开始录。
    ///
    /// 只允许从「服务端已确认」的状态回到 IDLE。UPLOAD_FAILED 不在其中 ——
    /// 那样会把还没传上去的录音悄悄丢掉，必须先重传或显式放弃。
    pub fn reset(self) -> Option<Self> {
        match self {
            Self::Acked | Self::PurgePending | Self::Purged => Some(Self::Idle),
            _ => None,
        }
    }

    /// 麦克风流是否还挂着（Paused 也算会话存续，见架构 §5：静音不是暂停）。
    pub fn is_capturing(self) -> bool {
        matches!(self, Self::Recording | Self::Paused)
    }

    /// 还有数据没送到服务端吗？这几个状态下不能开新录音，否则会丢掉旧的。
    pub fn has_pending_upload(self) -> bool {
        matches!(
            self,
            Self::Finalizing
                | Self::Queued
                | Self::Uploading
                | Self::Verifying
                | Self::RetryWait
                | Self::UploadFailed
        )
    }

    /// 本地文件是否必须留着：没拿到服务端确认前一律不能删。
    pub fn must_keep_local(self) -> bool {
        !matches!(self, Self::Idle | Self::Purged)
    }
}

/// 审计事件（架构 §5.2：开始/暂停/恢复/停止/上传/确认/删除都要写）。
/// 这里只在客户端内存里排队，由上层同步给网关审计流。
#[derive(Debug, Clone)]
pub struct AuditEntry {
    pub action: &'static str,
    pub detail: String,
    pub at: SystemTime,
}

impl AuditEntry {
    pub fn new(action: &'static str, detail: impl Into<String>) -> Self {
        Self {
            action,
            detail: detail.into(),
            at: SystemTime::now(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pause_resume_roundtrip() {
        assert_eq!(RecordingState::Recording.pause(), Some(RecordingState::Paused));
        assert_eq!(RecordingState::Paused.resume(), Some(RecordingState::Recording));
        assert_eq!(RecordingState::Idle.pause(), None);
    }

    #[test]
    fn pause_is_not_allowed_twice() {
        let paused = RecordingState::Recording.pause().unwrap();
        assert_eq!(paused.pause(), None, "重复暂停应该被拒绝，不能悄悄成功");
        assert_eq!(RecordingState::Recording.resume(), None);
    }

    #[test]
    fn start_only_from_idle() {
        assert_eq!(RecordingState::Idle.start(), Some(RecordingState::Recording));
        assert_eq!(RecordingState::Recording.start(), None);
        assert_eq!(RecordingState::Uploading.start(), None);
    }

    #[test]
    fn happy_path_walks_the_documented_chain() {
        let mut s = RecordingState::Idle;
        for step in [
            RecordingState::start,
            RecordingState::finalize,
            RecordingState::queue,
            RecordingState::upload,
            RecordingState::verify,
            RecordingState::ack,
            RecordingState::request_purge,
            RecordingState::purge,
        ] {
            s = step(s).expect("documented transition must exist");
        }
        assert_eq!(s, RecordingState::Purged);
    }

    #[test]
    fn finalize_works_from_paused_too() {
        let paused = RecordingState::Paused;
        assert_eq!(paused.finalize(), Some(RecordingState::Finalizing));
    }

    #[test]
    fn retry_loop_returns_to_uploading() {
        let uploading = RecordingState::Uploading;
        let waiting = uploading.retry_wait().unwrap();
        assert_eq!(waiting, RecordingState::RetryWait);
        assert_eq!(waiting.upload(), Some(RecordingState::Uploading));
        // VERIFYING 也可以退避重来（架构 §5.1 失败路径）
        assert_eq!(
            RecordingState::Verifying.retry_wait(),
            Some(RecordingState::RetryWait)
        );
    }

    #[test]
    fn upload_failed_can_be_requeued_and_keeps_local_file() {
        let failed = RecordingState::Uploading.fail().unwrap();
        assert_eq!(failed, RecordingState::UploadFailed);
        assert!(failed.must_keep_local(), "失败必须保留本地副本");
        assert_eq!(failed.queue(), Some(RecordingState::Queued));
    }

    #[test]
    fn acked_still_keeps_local_until_purged() {
        assert!(RecordingState::Acked.must_keep_local());
        assert!(RecordingState::PurgePending.must_keep_local());
        assert!(!RecordingState::Purged.must_keep_local());
    }

    #[test]
    fn settled_sessions_can_be_reset_for_the_next_meeting() {
        // 开完一场会还能再开一场：确认过的会话可以归零
        for state in [
            RecordingState::Acked,
            RecordingState::PurgePending,
            RecordingState::Purged,
        ] {
            assert_eq!(state.reset(), Some(RecordingState::Idle), "{state:?}");
            assert_eq!(state.reset().unwrap().start(), Some(RecordingState::Recording));
        }
    }

    #[test]
    fn reset_never_discards_unsent_audio() {
        // 这几个状态下本地还有没传上去的东西，归零就等于悄悄丢数据
        for state in [
            RecordingState::Recording,
            RecordingState::Paused,
            RecordingState::Finalizing,
            RecordingState::Queued,
            RecordingState::Uploading,
            RecordingState::Verifying,
            RecordingState::RetryWait,
            RecordingState::UploadFailed,
        ] {
            assert_eq!(state.reset(), None, "{state:?} 不该允许归零");
        }
    }

    #[test]
    fn pending_upload_covers_every_unsettled_state() {
        assert!(RecordingState::Queued.has_pending_upload());
        assert!(RecordingState::Uploading.has_pending_upload());
        assert!(RecordingState::Verifying.has_pending_upload());
        assert!(RecordingState::RetryWait.has_pending_upload());
        assert!(RecordingState::UploadFailed.has_pending_upload());
        // 已确认的不算待上传，否则下一场会永远开不起来
        assert!(!RecordingState::Acked.has_pending_upload());
        assert!(!RecordingState::Idle.has_pending_upload());
        // 还在录的算 capturing，不算 pending upload，两个判断不重叠
        assert!(!RecordingState::Recording.has_pending_upload());
    }

    #[test]
    fn capturing_covers_paused() {
        assert!(RecordingState::Recording.is_capturing());
        assert!(RecordingState::Paused.is_capturing());
        assert!(!RecordingState::Queued.is_capturing());
    }

    #[test]
    fn state_names_match_architecture_doc() {
        assert_eq!(RecordingState::RetryWait.as_str(), "RETRY_WAIT");
        assert_eq!(RecordingState::UploadFailed.as_str(), "UPLOAD_FAILED");
        assert_eq!(RecordingState::Idle.as_str(), "IDLE");
    }
}
