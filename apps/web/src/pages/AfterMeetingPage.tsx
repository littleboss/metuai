import { useEffect, useState } from 'react'
import {
  applyBreakGlass,
  approveBreakGlass,
  confirmGuestEmailVerification,
  downloadArtifact,
  getAudit,
  getMedia,
  getPipeline,
  getSummary,
  getTranscript,
  listBreakGlass,
  reindexMeetingKnowledge,
  requestGuestEmailVerification,
  runAsrStub,
  runFakePipeline,
  searchKnowledge,
  type AuditEvent,
  type BreakGlassRequest,
  type KnowledgeHit,
  type MediaArtifact,
  type MeetingSummary,
  type PipelineStatus,
  type TranscriptSegment,
} from '../lib/api'

type AfterMeetingPageProps = {
  meetingId: string
}

/** 把 kind / status / detail 翻译成会后页可读的一行说明。 */
function describeMedia(item: MediaArtifact): {
  kindLabel: string
  statusLabel: string
  statusClass: string
  storageLabel: string
} {
  const kindLabel =
    (
      {
        room_audio: '房间混音',
        room_video: '房间画面',
        participant_track: '参会人独立音轨',
        local_mic: '本机麦克风备份',
      } as Record<string, string>
    )[item.kind] ?? item.kind

  const statusClass =
    (
      {
        ready: 'ready',
        failed: 'failed',
        started: 'started',
        pending: 'pending',
      } as Record<string, string>
    )[item.status] ?? 'muted'

  const statusLabel =
    (
      {
        ready: '就绪',
        failed: '失败',
        started: '录制中/待收尾',
        pending: '待启动',
      } as Record<string, string>
    )[item.status] ?? item.status

  let storageLabel = '位置未知'
  if (item.detail.includes('s3_error=')) {
    storageLabel = '云上传失败（本地 spool 仍在）'
  } else if (item.detail.includes('s3=') || item.object_key.startsWith('metuai-media/')) {
    storageLabel = item.kind === 'local_mic' ? '已写入 MinIO' : '对象存储（Egress→MinIO）'
  } else if (item.detail.includes('path=')) {
    storageLabel = '仅本地 spool'
  } else if (item.status === 'pending') {
    storageLabel = '尚未落盘'
  } else if (item.egress_id) {
    storageLabel = 'LiveKit Egress'
  }

  return { kindLabel, statusLabel, statusClass, storageLabel }
}

export function AfterMeetingPage({ meetingId }: AfterMeetingPageProps) {
  const [authToken, setAuthToken] = useState(() => sessionStorage.getItem('authToken') ?? '')
  const isGuest = sessionStorage.getItem('principalKind') === 'guest'
  const [pipeline, setPipeline] = useState<PipelineStatus | null>(null)
  const [segments, setSegments] = useState<TranscriptSegment[]>([])
  const [summary, setSummary] = useState<MeetingSummary | null>(null)
  const [media, setMedia] = useState<MediaArtifact[]>([])
  const [audits, setAudits] = useState<AuditEvent[]>([])
  const [error, setError] = useState(() => (authToken ? '' : '缺少会议访问令牌，请重新进入会议。'))
  const [busy, setBusy] = useState(false)
  const [searchQ, setSearchQ] = useState('')
  const [searchHits, setSearchHits] = useState<KnowledgeHit[]>([])
  const [searchBackend, setSearchBackend] = useState('')
  const [breakReason, setBreakReason] = useState('')
  const [breakReqs, setBreakReqs] = useState<BreakGlassRequest[]>([])
  const [guestEmail, setGuestEmail] = useState('')
  const [guestCode, setGuestCode] = useState('')
  const [verificationRequested, setVerificationRequested] = useState(false)
  const [verifiedGuestEmail, setVerifiedGuestEmail] = useState('')

  async function refresh(token = authToken) {
    if (!token) return
    const status = await getPipeline(meetingId, token)
    setPipeline(status)
    setMedia(await getMedia(meetingId, token))
    try {
      setAudits(await getAudit(meetingId, token))
    } catch {
      setAudits([])
    }
    try {
      setBreakReqs(await listBreakGlass(meetingId, token))
    } catch {
      /* ignore */
    }
    const stage = status.pipeline_stage
    if (
      stage === 'READY' ||
      stage === 'TRANSCRIPT_READY' ||
      stage === 'EXTRACTING_ARTIFACTS' ||
      stage === 'INDEXING'
    ) {
      setSegments(await getTranscript(meetingId, token))
    }
    if (stage === 'READY') {
      setSummary(await getSummary(meetingId, token))
    }
  }

  useEffect(() => {
    if (!authToken) return
    let cancelled = false
    void (async () => {
      try {
        const status = await getPipeline(meetingId, authToken)
        if (cancelled) return
        setPipeline(status)
        const nextMedia = await getMedia(meetingId, authToken)
        let nextAudits: AuditEvent[] = []
        try {
          nextAudits = await getAudit(meetingId, authToken)
        } catch {
          // 会议内容权限不等于审计管理员权限。
        }
        if (cancelled) return
        setMedia(nextMedia)
        setAudits(nextAudits)
        try {
          setBreakReqs(await listBreakGlass(meetingId, authToken))
        } catch {
          setBreakReqs([])
        }
        const stage = status.pipeline_stage
        if (
          stage === 'READY' ||
          stage === 'TRANSCRIPT_READY' ||
          stage === 'EXTRACTING_ARTIFACTS' ||
          stage === 'INDEXING'
        ) {
          const nextSegments = await getTranscript(meetingId, authToken)
          if (cancelled) return
          setSegments(nextSegments)
        }
        if (stage === 'READY') {
          const nextSummary = await getSummary(meetingId, authToken)
          if (cancelled) return
          setSummary(nextSummary)
        }
      } catch (requestError) {
        if (!cancelled) {
          setError(requestError instanceof Error ? requestError.message : '加载失败')
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [meetingId, authToken])

  async function handleRunFake() {
    setBusy(true)
    setError('')
    try {
      await runFakePipeline(meetingId, authToken)
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '假流水线失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleRunAsrStub() {
    setBusy(true)
    setError('')
    try {
      await runAsrStub(meetingId, authToken)
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : 'ASR stub 失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleSearch() {
    if (!authToken || !searchQ.trim()) return
    setBusy(true)
    setError('')
    try {
      const result = await searchKnowledge(searchQ.trim(), authToken)
      setSearchHits(result.hits)
      setSearchBackend(result.backend)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '知识检索失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleReindex() {
    setBusy(true)
    setError('')
    try {
      await reindexMeetingKnowledge(meetingId, authToken)
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '重新索引失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleDownload(kind: 'transcript' | 'summary' | 'media') {
    setBusy(true)
    setError('')
    try {
      await downloadArtifact(meetingId, authToken, kind)
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '下载审计失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleBreakApply() {
    if (!breakReason.trim()) return
    setBusy(true)
    setError('')
    try {
      await applyBreakGlass(meetingId, authToken, breakReason.trim())
      setBreakReason('')
      setBreakReqs(await listBreakGlass(meetingId, authToken))
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '破窗申请失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleBreakApprove(reqId: string) {
    setBusy(true)
    setError('')
    try {
      await approveBreakGlass(meetingId, reqId, authToken)
      setBreakReqs(await listBreakGlass(meetingId, authToken))
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '破窗批准失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleRequestGuestVerification() {
    if (!guestEmail.trim()) return
    setBusy(true)
    setError('')
    try {
      await requestGuestEmailVerification(meetingId, authToken, guestEmail.trim())
      setVerificationRequested(true)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '验证码发送失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleConfirmGuestVerification() {
    if (!guestEmail.trim() || !guestCode.trim()) return
    setBusy(true)
    setError('')
    try {
      const verified = await confirmGuestEmailVerification(
        meetingId,
        authToken,
        guestEmail.trim(),
        guestCode.trim(),
      )
      sessionStorage.setItem('authToken', verified.access_token)
      setAuthToken(verified.access_token)
      setVerifiedGuestEmail(verified.email)
      setGuestCode('')
      await refresh(verified.access_token)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '验证码确认失败')
    } finally {
      setBusy(false)
    }
  }

  const recordingAudits = audits.filter(
    (e) =>
      e.action.startsWith('local_recording_') ||
      e.action.startsWith('egress_') ||
      e.action === 'artifact_download' ||
      e.action.startsWith('break_glass_') ||
      e.action === 'knowledge_search' ||
      e.action === 'index_upserted',
  )
  const readyCount = media.filter((m) => m.status === 'ready').length
  const failedCount = media.filter((m) => m.status === 'failed').length

  return (
    <main className="shell">
      <header className="brand">
        <a href="/" className="brand-home" aria-label="返回首页">
          <span className="brand-mark" aria-hidden="true">
            M
          </span>
          <span>METUAI / AFTER MEETING</span>
        </a>
        <span className="status-pill muted">{pipeline?.pipeline_stage || 'LOADING'}</span>
      </header>

      <section className="panel" style={{ maxWidth: 920, margin: '0 auto', borderLeft: 0 }}>
        <div className="panel-heading">
          <div>
            <p className="eyebrow">POST PROCESS</p>
            <h2>{pipeline?.title || meetingId}</h2>
          </div>
        </div>

        {!isGuest && <p className="lede">
          会后流水线权威状态在网关。可先跑「stub ASR」只到转写，或跑「假流水线」直到 READY（假纪要）。
          真 FunASR 请用 Worker：<code>python -m worker.main --mode asr</code>。
          若有本机麦克风备份，转写可能标 <code>local_fallback</code>。
        </p>}

        {!isGuest && <div className="organizer-actions" style={{ gap: 12, flexWrap: 'wrap' }}>
          <button
            className="primary-button"
            type="button"
            disabled={busy || !authToken || pipeline?.ended === false}
            onClick={() => void handleRunAsrStub()}
          >
            {busy ? '处理中…' : '运行 stub ASR → 转写'}
          </button>
          <button
            className="primary-button accent"
            type="button"
            disabled={busy || !authToken || pipeline?.ended === false}
            onClick={() => void handleRunFake()}
          >
            {busy ? '流水线运行中…' : '运行假流水线 → READY'}
            <span aria-hidden="true">⚙</span>
          </button>
          <button
            className="primary-button"
            type="button"
            disabled={busy || !authToken}
            onClick={() => void handleReindex()}
          >
            重新索引知识库
          </button>
        </div>}
        {pipeline && !pipeline.ended && (
          <p className="chat-hint">会议尚未结束，无法跑会后流水线。</p>
        )}

        {error && (
          <p className="error-message" role="alert">
            {error}
          </p>
        )}

        {isGuest && (
          <section className="meeting-card" style={{ marginTop: 28 }}>
            <div className="meeting-card-title">嘉宾邮箱验证</div>
            {verifiedGuestEmail ? (
              <p className="chat-hint">已验证 {verifiedGuestEmail}，当前会后令牌仅可访问本场会议。</p>
            ) : (
              <>
                <p className="chat-hint">验证邮箱后可查看、下载和检索本场会后产物。</p>
                <div className="organizer-actions" style={{ gap: 8, flexWrap: 'wrap' }}>
                  <input
                    type="email"
                    value={guestEmail}
                    onChange={(event) => setGuestEmail(event.target.value)}
                    placeholder="name@example.com"
                    aria-label="嘉宾邮箱"
                    autoComplete="email"
                  />
                  <button
                    className="primary-button"
                    type="button"
                    disabled={busy || !guestEmail.trim()}
                    onClick={() => void handleRequestGuestVerification()}
                  >
                    发送验证码
                  </button>
                  {verificationRequested && (
                    <>
                      <input
                        inputMode="numeric"
                        value={guestCode}
                        onChange={(event) => setGuestCode(event.target.value)}
                        placeholder="6 位验证码"
                        aria-label="邮箱验证码"
                        maxLength={6}
                      />
                      <button
                        className="primary-button accent"
                        type="button"
                        disabled={busy || !guestCode.trim()}
                        onClick={() => void handleConfirmGuestVerification()}
                      >
                        确认邮箱
                      </button>
                    </>
                  )}
                </div>
              </>
            )}
          </section>
        )}

        {!isGuest && <section className="meeting-card" style={{ marginTop: 28 }}>
          <div className="meeting-card-title">破窗访问</div>
          <p className="chat-hint">
            非组织者可申请临时访问；另一员工批准后 1 小时内可下载/检索。申请人不能批自己。
          </p>
          <div className="organizer-actions" style={{ gap: 8, flexWrap: 'wrap' }}>
            <input
              value={breakReason}
              onChange={(e) => setBreakReason(e.target.value)}
              placeholder="申请原因，如：合规抽查"
              aria-label="破窗原因"
              style={{ flex: 1, minWidth: 180 }}
            />
            <button
              className="primary-button"
              type="button"
              disabled={busy || !authToken || !breakReason.trim()}
              onClick={() => void handleBreakApply()}
            >
              提交破窗申请
            </button>
          </div>
          {breakReqs.length > 0 && (
            <ul className="audit-compact-list" style={{ marginTop: 12 }}>
              {breakReqs.map((req) => (
                <li key={req.id}>
                  <code>{req.status}</code> · {req.applicant} · {req.reason}
                  {req.status === 'pending' ? (
                    <>
                      {' '}
                      <button
                        className="primary-button"
                        type="button"
                        disabled={busy}
                        onClick={() => void handleBreakApprove(req.id)}
                      >
                        批准
                      </button>
                    </>
                  ) : null}
                </li>
              ))}
            </ul>
          )}
        </section>}

        <section className="meeting-card" style={{ marginTop: 28 }}>
          <div className="meeting-card-title">
            知识检索{searchBackend ? ` · ${searchBackend}` : ''}
          </div>
          <p className="chat-hint">
            假流水线 READY 后会把纪要/转写入索引；检索按组织者/参会人/嘉宾邮箱 ACL 过滤。
          </p>
          <div className="organizer-actions" style={{ gap: 8, flexWrap: 'wrap' }}>
            <input
              type="search"
              value={searchQ}
              onChange={(e) => setSearchQ(e.target.value)}
              placeholder="关键词，如：假纪要"
              aria-label="知识检索关键词"
              style={{ flex: 1, minWidth: 180 }}
            />
            <button
              className="primary-button"
              type="button"
              disabled={busy || !authToken || !searchQ.trim()}
              onClick={() => void handleSearch()}
            >
              搜索
            </button>
          </div>
          {searchHits.length > 0 && (
            <ul className="audit-compact-list" style={{ marginTop: 12 }}>
              {searchHits.map((hit) => (
                <li key={`${hit.document.meeting_id}-${hit.document.source_type}`}>
                  <strong>{hit.document.title}</strong>
                  {' · '}
                  <code>{hit.document.source_type}</code>
                  {' · '}
                  {hit.snippet}
                </li>
              ))}
            </ul>
          )}
        </section>

        {media.length > 0 && (
          <section className="meeting-card" style={{ marginTop: 28 }}>
            <div className="meeting-card-title">
              媒体产物
              <span className="media-summary">
                {' '}
                · 就绪 {readyCount} · 失败 {failedCount} · 共 {media.length}
              </span>
            </div>
            <ul className="media-artifact-list">
              {media.map((item) => {
                const d = describeMedia(item)
                return (
                  <li key={item.id} className="media-artifact-item">
                    <div className="media-artifact-head">
                      <strong>{d.kindLabel}</strong>
                      <span className={`status-pill ${d.statusClass}`}>{d.statusLabel}</span>
                    </div>
                    <p className="media-artifact-meta">
                      存储：{d.storageLabel}
                      {item.egress_id ? (
                        <>
                          {' '}
                          · egress <code>{item.egress_id}</code>
                        </>
                      ) : null}
                    </p>
                    {item.object_key ? (
                      <p className="media-artifact-key">
                        对象键 <code>{item.object_key}</code>
                      </p>
                    ) : null}
                    {item.detail ? (
                      <p className="media-artifact-detail">
                        <code>{item.detail}</code>
                      </p>
                    ) : null}
                  </li>
                )
              })}
            </ul>
          </section>
        )}

        {recordingAudits.length > 0 && (
          <section className="meeting-card" style={{ marginTop: 20 }}>
            <div className="meeting-card-title">录制相关审计</div>
            <ul className="audit-compact-list">
              {recordingAudits.map((item) => (
                <li key={item.id}>
                  <code>{item.action}</code>
                  {item.detail ? <> · {item.detail}</> : null}
                </li>
              ))}
            </ul>
          </section>
        )}

        {summary && (
          <section className="meeting-card" style={{ marginTop: 28 }}>
            <div className="meeting-card-title">纪要</div>
            <p>{summary.summary}</p>
            <div className="organizer-actions" style={{ marginBottom: 12 }}>
              <button
                className="primary-button"
                type="button"
                disabled={busy || !authToken}
                onClick={() => void handleDownload('summary')}
              >
                下载纪要（记审计）
              </button>
            </div>
            <h3>决策</h3>
            <ul>
              {summary.decisions.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ul>
            <h3>待办</h3>
            <ul>
              {summary.action_items.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ul>
          </section>
        )}

        {segments.length > 0 && (
          <section className="meeting-card" style={{ marginTop: 20 }}>
            <div className="meeting-card-title">转写片段</div>
            <div className="organizer-actions" style={{ marginBottom: 12 }}>
              <button
                className="primary-button"
                type="button"
                disabled={busy || !authToken}
                onClick={() => void handleDownload('transcript')}
              >
                下载转写（记审计）
              </button>
            </div>
            <div className="chat-log">
              {segments.map((seg) => (
                <article key={seg.id}>
                  <header>
                    <strong>{seg.speaker_display_name}</strong>
                    <span>
                      {' '}
                      {seg.start_ms}–{seg.end_ms}ms · {seg.source}
                    </span>
                  </header>
                  <p>{seg.text}</p>
                </article>
              ))}
            </div>
          </section>
        )}
      </section>
    </main>
  )
}
