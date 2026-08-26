import { LiveKitRoom, VideoConference, useParticipants } from '@livekit/components-react'
import '@livekit/components-styles'
import { type FormEvent, useEffect, useRef, useState } from 'react'
import {
  endMeeting,
  heartbeat,
  kickParticipant,
  listChat,
  lockMeeting,
  postChat,
  resetPassword,
  unlockMeeting,
  type ChatMessage,
} from '../lib/api'
import {
  canUseLocalRecording,
  flushRecordingAudit,
  listPendingUploads,
  pauseLocalRecording,
  purgeLocalRecording,
  recordingState,
  resumeLocalRecording,
  resumePendingUpload,
  startLocalRecording,
  stopAndUploadLocalRecording,
  type PendingUpload,
} from '../lib/localRecording'

type RoomPageProps = {
  url: string
  token: string
  meetingId: string
  authToken: string
  isOrganizer: boolean
  initialPassword: string
}

function OrganizerToolbar({
  meetingId,
  authToken,
  initialPassword,
  onEnded,
}: {
  meetingId: string
  authToken: string
  initialPassword: string
  onEnded: () => Promise<void>
}) {
  const participants = useParticipants()
  const [locked, setLocked] = useState(false)
  const [password, setPassword] = useState(initialPassword)
  const [status, setStatus] = useState('')
  const [busy, setBusy] = useState(false)

  async function run(action: () => Promise<void>, okMessage: string) {
    setBusy(true)
    setStatus('')
    try {
      await action()
      setStatus(okMessage)
    } catch (error) {
      setStatus(error instanceof Error ? error.message : '操作失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <aside className="organizer-panel" aria-label="组织者控制">
      <p className="eyebrow">ORGANIZER</p>
      <h2>会中控制</h2>
      <div className="organizer-actions">
        <button
          type="button"
          disabled={busy}
          onClick={() =>
            run(async () => {
              if (locked) {
                await unlockMeeting(meetingId, authToken)
                setLocked(false)
              } else {
                await lockMeeting(meetingId, authToken)
                setLocked(true)
              }
            }, locked ? '已解锁' : '已锁定')
          }
        >
          {locked ? '解锁会议' : '锁定会议'}
        </button>
        <button
          type="button"
          disabled={busy}
          onClick={() =>
            run(async () => {
              const result = await resetPassword(meetingId, authToken)
              setPassword(result.password)
              sessionStorage.setItem('roomPassword', result.password)
            }, '密码已重置')
          }
        >
          重置密码
        </button>
        <button
          type="button"
          className="danger"
          disabled={busy}
          onClick={() =>
            run(async () => {
              await endMeeting(meetingId, authToken)
              await onEnded()
              window.location.assign(`/meeting/${meetingId}`)
            }, '会议已结束')
          }
        >
          全员结束
        </button>
      </div>

      {password && (
        <p className="organizer-password">
          当前密码：<code>{password}</code>
        </p>
      )}

      <p className="eyebrow">KICK</p>
      <ul className="kick-list">
        {participants
          .filter((p) => !p.isLocal)
          .map((p) => (
            <li key={p.identity}>
              <span>{p.name || p.identity}</span>
              <button
                type="button"
                disabled={busy}
                onClick={() =>
                  run(
                    () => kickParticipant(meetingId, authToken, p.identity),
                    `已踢出 ${p.name || p.identity}`,
                  )
                }
              >
                踢出
              </button>
            </li>
          ))}
      </ul>
      {status && <p className="organizer-status">{status}</p>}
    </aside>
  )
}

/**
 * 员工桌面端：本机麦克风备份面板。
 * 录的是「你自己的麦」，不是系统声；上传失败时本地 spool 仍会保留。
 */
function LocalRecordingPanel({
  meetingId,
  authToken,
}: {
  meetingId: string
  authToken: string
}) {
  const [state, setState] = useState('IDLE')
  const [status, setStatus] = useState('正在读取本机录音状态…')
  const [busy, setBusy] = useState(false)
  const [spoolDir, setSpoolDir] = useState('')
  const [checksum, setChecksum] = useState('')
  const [pending, setPending] = useState<PendingUpload[]>([])

  async function refreshPending() {
    try {
      setPending(await listPendingUploads())
    } catch {
      setPending([])
    }
  }

  useEffect(() => {
    void listPendingUploads().then(setPending, () => setPending([]))
    void recordingState()
      .then((current) => {
        setState(current)
        setStatus(current === 'RECORDING' ? '已在入会前自动开始备份' : `当前状态：${current}`)
      })
      .catch((error: unknown) => {
        setStatus(error instanceof Error ? error.message : '无法读取本机录音状态')
      })
  }, [])

  async function run(action: () => Promise<void>) {
    setBusy(true)
    try {
      await action()
    } catch (error) {
      setStatus(error instanceof Error ? error.message : '操作失败')
    } finally {
      setBusy(false)
    }
  }

  const recording = state === 'RECORDING'
  const paused = state === 'PAUSED'
  const acked = state === 'ACKED'
  const idleLike = state === 'IDLE' || state === 'PURGED' || state === 'UPLOAD_FAILED'

  return (
    <aside className="local-recording-panel" aria-label="本机麦克风备份">
      <p className="eyebrow">LOCAL MIC BACKUP</p>
      <h2>本机录音</h2>
      <p className="chat-hint">
        只录本机麦克风（缺轨时可做 local_fallback）。当前状态：
        <code>{state}</code>
      </p>
      <div className="organizer-actions">
        <button
          type="button"
          disabled={busy || !idleLike}
          onClick={() =>
            run(async () => {
              const started = await startLocalRecording(meetingId, authToken)
              setState(started.state)
              setSpoolDir(started.spool_dir)
              setChecksum('')
              if (started.mic.active) {
                setStatus(
                  `录音中（${started.mic.sample_rate} Hz / ${started.mic.channels} ch）`,
                )
              } else {
                setStatus(
                  `会话已建，但麦克风未打开：${started.mic.error || 'unknown'}`,
                )
              }
            })
          }
        >
          开始备份
        </button>
        <button
          type="button"
          disabled={busy || !recording}
          onClick={() =>
            run(async () => {
              const next = await pauseLocalRecording()
              setState(next)
              setStatus('已暂停（文件里会出现静音缺口，供审计对齐）')
            })
          }
        >
          暂停
        </button>
        <button
          type="button"
          disabled={busy || !paused}
          onClick={() =>
            run(async () => {
              const next = await resumeLocalRecording()
              setState(next)
              setStatus('已恢复录音')
            })
          }
        >
          恢复
        </button>
        <button
          type="button"
          disabled={busy || (!recording && !paused)}
          onClick={() =>
            run(async () => {
              setStatus('正在停止并上传…（可能需要几秒）')
              const done = await stopAndUploadLocalRecording()
              setState(done.state)
              setChecksum(done.checksum)
              const where =
                done.stored_in === 's3'
                  ? `已写入 MinIO${done.object_key ? `（${done.object_key}）` : ''}`
                  : done.stored_in === 'local_spool_only'
                    ? '云上传失败，本地 spool 已保留'
                    : done.stored_in === 'local_spool'
                      ? '仅本地 spool（未配 S3）'
                      : '落点见会后页媒体'
              setStatus(
                `上传完成：${done.parts} 块 / ${done.bytes} 字节，checksum 已校验；${where}；审计回传 ${done.audit_flushed} 条`,
              )
              // stop 里已尽力 flush；若为 0 再手动补一次（网关短暂不可达时）。
              if (done.audit_flushed === 0) {
                try {
                  const n = await flushRecordingAudit()
                  if (n > 0) {
                    setStatus((prev) => `${prev}（补传审计 ${n} 条）`)
                  }
                } catch {
                  // 审计补传失败不挡主流程；会后页可对照网关审计。
                }
              }
            })
          }
        >
          停止并上传
        </button>
        <button
          type="button"
          disabled={busy || !acked}
          onClick={() =>
            run(async () => {
              const next = await purgeLocalRecording()
              setState(next)
              setSpoolDir('')
              setStatus('本地副本已按策略删除')
            })
          }
        >
          清理本地副本
        </button>
      </div>
      {spoolDir && (
        <p className="organizer-status">
          本地目录：<code>{spoolDir}</code>
        </p>
      )}
      {checksum && (
        <p className="organizer-status">
          checksum：<code>{checksum.slice(0, 16)}…</code>
        </p>
      )}
      {pending.length > 0 && (
        <div className="pending-uploads" style={{ marginTop: 12 }}>
          <p className="chat-hint">待恢复上传（崩溃/失败后留下的加密 spool）：</p>
          <ul>
            {pending.map((item) => (
              <li key={item.upload_id}>
                <code>{item.upload_id}</code>
                <button
                  type="button"
                  disabled={busy}
                  style={{ marginLeft: 8 }}
                  onClick={() =>
                    run(async () => {
                      setStatus(`正在恢复上传 ${item.upload_id}…`)
                      const done = await resumePendingUpload(item.upload_id, authToken)
                      setState(done.state)
                      setChecksum(done.checksum)
                      setStatus(
                        `恢复完成：${done.parts} 块，stored_in=${done.stored_in || 'n/a'}`,
                      )
                      await refreshPending()
                    })
                  }
                >
                  恢复上传
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}
      {status && <p className="organizer-status">{status}</p>}
    </aside>
  )
}

function PersistChat({
  meetingId,
  authToken,
}: {
  meetingId: string
  authToken: string
}) {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [draft, setDraft] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    let cancelled = false

    async function refresh() {
      try {
        const next = await listChat(meetingId, authToken)
        if (!cancelled) {
          setMessages(next)
        }
      } catch {
        // 轮询失败时保持现有消息，避免抖动。
      }
    }

    void refresh()
    const timer = window.setInterval(() => {
      void refresh()
    }, 3000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [meetingId, authToken])

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const body = draft.trim()
    if (!body) return
    setError('')
    try {
      const created = await postChat(meetingId, authToken, body)
      setMessages((prev) => [...prev, created])
      setDraft('')
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '发送失败')
    }
  }

  return (
    <aside className="persist-chat" aria-label="落库聊天">
      <p className="eyebrow">PERSISTED CHAT</p>
      <h2>会中留言</h2>
      <p className="chat-hint">消息写入网关数据库，供会后纪要引用（不只依赖 LiveKit 短暂聊天）。</p>
      <div className="chat-log">
        {messages.length === 0 && <p className="chat-empty">暂无消息</p>}
        {messages.map((message) => (
          <article key={message.id}>
            <header>
              <strong>{message.display_name || message.sender_key}</strong>
            </header>
            <p>{message.body}</p>
          </article>
        ))}
      </div>
      <form onSubmit={handleSubmit}>
        <input
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          placeholder="输入并发送到服务器…"
          maxLength={2000}
        />
        <button type="submit">发送</button>
      </form>
      {error && <p className="error-message">{error}</p>}
    </aside>
  )
}

export function RoomPage({
  url,
  token,
  meetingId,
  authToken,
  isOrganizer,
  initialPassword,
}: RoomPageProps) {
  const showLocalRecording = canUseLocalRecording()
  const finalizingRef = useRef<Promise<void> | null>(null)

  function finalizeLocalRecording(): Promise<void> {
    if (!showLocalRecording) return Promise.resolve()
    if (finalizingRef.current) return finalizingRef.current
    finalizingRef.current = recordingState()
      .then(async (current) => {
        if (current === 'RECORDING' || current === 'PAUSED') {
          await stopAndUploadLocalRecording()
        }
      })
      .finally(() => {
        finalizingRef.current = null
      })
    return finalizingRef.current
  }

  useEffect(() => {
    // 会中心跳刷新 last_active_at，避免空闲自动结束误杀仍在开会的房间。
    const tick = () => {
      void heartbeat(meetingId, authToken).catch(() => {
        // 网络抖动时忽略；连续失败后由空闲策略结束会议。
      })
    }
    tick()
    const timer = window.setInterval(tick, 30_000)
    return () => window.clearInterval(timer)
  }, [meetingId, authToken])

  return (
    <main className="room-page" data-lk-theme="default">
      <LiveKitRoom
        serverUrl={url}
        token={token}
        connect
        audio
        video
        onDisconnected={() => {
          void finalizeLocalRecording().finally(() => window.location.assign(`/meeting/${meetingId}`))
        }}
      >
        <div className="room-layout">
          <div className="room-stage">
            {/* LiveKit 提供设备选择、参与者画面和离会控制。 */}
            <VideoConference />
          </div>
          <div className="room-side">
            {isOrganizer && (
              <OrganizerToolbar
                meetingId={meetingId}
                authToken={authToken}
                initialPassword={initialPassword}
                onEnded={finalizeLocalRecording}
              />
            )}
            {showLocalRecording && (
              <LocalRecordingPanel meetingId={meetingId} authToken={authToken} />
            )}
            <PersistChat meetingId={meetingId} authToken={authToken} />
          </div>
        </div>
      </LiveKitRoom>
    </main>
  )
}
