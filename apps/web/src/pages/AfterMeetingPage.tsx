import { useEffect, useState } from 'react'
import {
  addSharedReader,
  applyBreakGlass,
  approveBreakGlass,
  completeActionItem,
  confirmGuestEmailVerification,
  confirmGuestMagicLink,
  confirmSharedReaderVerification,
  denyBreakGlass,
  downloadArtifact,
  getAudit,
  getMedia,
  getPipeline,
  getRetentionPolicy,
  getSummary,
  getTranscript,
  issueInRoomGuestCode,
  listBreakGlass,
  listGuestParticipants,
  listSharedReaders,
  patchSummary,
  putRetentionPolicy,
  reindexMeetingKnowledge,
  removeSharedReader,
  requestGuestEmailVerification,
  requestSharedReaderVerification,
  revokeBreakGlass,
  retryPipeline,
  runAsrStub,
  runFakePipeline,
  searchKnowledge,
  type ActionItem,
  type AuditEvent,
  type BreakGlassRequest,
  type CitedItem,
  type GuestParticipant,
  type KnowledgeHit,
  type MediaArtifact,
  type MeetingSummary,
  type PipelineStatus,
  type RetentionPolicy,
  type SharedReader,
  type TranscriptSegment,
} from '../lib/api'

type AfterMeetingPageProps = {
  meetingId: string
}

function saveJSONFile(filename: string, data: unknown) {
  const blob = new Blob([JSON.stringify(data, null, 2)], {
    type: 'application/json;charset=utf-8',
  })
  const href = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = href
  link.download = filename
  link.click()
  URL.revokeObjectURL(href)
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
        purged: 'muted',
      } as Record<string, string>
    )[item.status] ?? 'muted'

  const statusLabel =
    (
      {
        ready: '就绪',
        failed: '失败',
        started: '录制中/待收尾',
        pending: '待启动',
        purged: '已按保留策略删除',
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

function citedLabel(item: CitedItem): string {
  const refs = item.source_segment_ids?.length ? ` · 来源 ${item.source_segment_ids.join(', ')}` : ''
  return `${item.text}${refs}`
}

function actionLabel(item: ActionItem): string {
  const owner = item.owner_user_id ? ` · 负责人 ${item.owner_user_id}` : ' · 负责人未指定'
  const refs = item.source_segment_ids?.length ? ` · 来源 ${item.source_segment_ids.join(', ')}` : ''
  const done = item.completed_at ? ' · 已完成' : ''
  return `${item.task}${owner}${refs}${done}`
}

function daysFromSeconds(seconds: number): string {
  if (!seconds) return '0'
  return String(Math.round(seconds / 86400))
}

function secondsFromDays(days: string): number {
  const n = Number(days)
  if (!Number.isFinite(n) || n < 0) return 0
  return Math.round(n * 86400)
}

export function AfterMeetingPage({ meetingId }: AfterMeetingPageProps) {
  const [authToken, setAuthToken] = useState(() => sessionStorage.getItem('authToken') ?? '')
  const isGuest = sessionStorage.getItem('principalKind') === 'guest'
  const [pipeline, setPipeline] = useState<PipelineStatus | null>(null)
  const [segments, setSegments] = useState<TranscriptSegment[]>([])
  const [summary, setSummary] = useState<MeetingSummary | null>(null)
  const [media, setMedia] = useState<MediaArtifact[]>([])
  const [audits, setAudits] = useState<AuditEvent[]>([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [searchQ, setSearchQ] = useState('')
  const [searchHits, setSearchHits] = useState<KnowledgeHit[]>([])
  const [searchBackend, setSearchBackend] = useState('')
  const [breakReason, setBreakReason] = useState('')
  const [breakReqs, setBreakReqs] = useState<BreakGlassRequest[]>([])
  const [shares, setShares] = useState<SharedReader[]>([])
  const [canManageShares, setCanManageShares] = useState(false)
  const [shareEmail, setShareEmail] = useState('')
  const [shareCode, setShareCode] = useState('')
  const [shareMagicURL, setShareMagicURL] = useState('')
  const [claimEmail, setClaimEmail] = useState('')
  const [claimCode, setClaimCode] = useState('')
  const [claimRequested, setClaimRequested] = useState(false)
  const [guestEmail, setGuestEmail] = useState('')
  const [guestCode, setGuestCode] = useState('')
  const [inRoomEmail, setInRoomEmail] = useState('')
  const [inRoomGuestID, setInRoomGuestID] = useState('')
  const [inRoomGuests, setInRoomGuests] = useState<GuestParticipant[]>([])
  const [inRoomIssued, setInRoomIssued] = useState<{ code: string; magic_url: string } | null>(null)
  const [verificationRequested, setVerificationRequested] = useState(false)
  const [verifiedGuestEmail, setVerifiedGuestEmail] = useState('')
  const [editing, setEditing] = useState(false)
  const [draftSummary, setDraftSummary] = useState('')
  const [draftDecisions, setDraftDecisions] = useState('')
  const [draftActions, setDraftActions] = useState('')
  const [draftOwner, setDraftOwner] = useState('')
  const [retention, setRetention] = useState<RetentionPolicy | null>(null)
  const [mediaDays, setMediaDays] = useState('')
  const [videoDays, setVideoDays] = useState('')
  const [knowledgeDays, setKnowledgeDays] = useState('')

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
    try {
      const readers = await listSharedReaders(meetingId, token)
      setShares(readers)
      setCanManageShares(true)
      try {
        setInRoomGuests(await listGuestParticipants(meetingId, token))
      } catch {
        setInRoomGuests([])
      }
    } catch {
      setCanManageShares(false)
    }
    try {
      const policy = await getRetentionPolicy(token)
      setRetention(policy)
      setMediaDays(daysFromSeconds(policy.media_ttl_seconds))
      setVideoDays(daysFromSeconds(policy.video_ttl_seconds))
      setKnowledgeDays(daysFromSeconds(policy.knowledge_ttl_seconds))
    } catch {
      setRetention(null)
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
    const magic = new URLSearchParams(window.location.search).get('verify_token')
    if (!magic) return
    let cancelled = false
    void (async () => {
      setBusy(true)
      setError('')
      try {
        const verified = await confirmGuestMagicLink(meetingId, magic)
        if (cancelled) return
        sessionStorage.setItem('authToken', verified.access_token)
        sessionStorage.setItem('principalKind', 'guest')
        setAuthToken(verified.access_token)
        setVerifiedGuestEmail(verified.email)
        window.history.replaceState({}, '', `/meeting/${meetingId}`)
      } catch (requestError) {
        if (!cancelled) {
          setError(requestError instanceof Error ? requestError.message : '魔法链接确认失败')
        }
      } finally {
        if (!cancelled) setBusy(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [meetingId])

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
        try {
          const readers = await listSharedReaders(meetingId, authToken)
          if (cancelled) return
          setShares(readers)
          setCanManageShares(true)
          try {
            const guests = await listGuestParticipants(meetingId, authToken)
            if (!cancelled) setInRoomGuests(guests)
          } catch {
            if (!cancelled) setInRoomGuests([])
          }
        } catch {
          if (!cancelled) setCanManageShares(false)
        }
        try {
          const policy = await getRetentionPolicy(authToken)
          if (cancelled) return
          setRetention(policy)
          setMediaDays(daysFromSeconds(policy.media_ttl_seconds))
          setVideoDays(daysFromSeconds(policy.video_ttl_seconds))
          setKnowledgeDays(daysFromSeconds(policy.knowledge_ttl_seconds))
        } catch {
          if (!cancelled) setRetention(null)
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

  async function handleRetryPipeline() {
    setBusy(true)
    setError('')
    try {
      await retryPipeline(meetingId, authToken)
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '重新排队失败')
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

  async function handleDownload(
    kind: 'transcript' | 'summary' | 'media' | 'export',
    artifactId?: string,
  ) {
    setBusy(true)
    setError('')
    try {
      const result = await downloadArtifact(meetingId, authToken, kind, artifactId)
      if (kind === 'media') {
        const url = result.artifacts?.find((item) => item.download_url)?.download_url
        if (url) {
          window.open(url, '_blank', 'noopener,noreferrer')
        }
      } else {
        saveJSONFile(`meeting-${meetingId}-${kind}.json`, result)
      }
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '下载审计失败')
    } finally {
      setBusy(false)
    }
  }

  function startEdit() {
    if (!summary) return
    setDraftSummary(summary.summary)
    setDraftDecisions(summary.decisions.map((item) => item.text).join('\n'))
    setDraftActions(summary.action_items.map((item) => item.task).join('\n'))
    setDraftOwner(summary.action_items[0]?.owner_user_id ?? '')
    setEditing(true)
  }

  async function handleSaveSummary() {
    if (!summary) return
    setBusy(true)
    setError('')
    try {
      const decisions = draftDecisions
        .split('\n')
        .map((text) => text.trim())
        .filter(Boolean)
        .map((text, index) => ({
          text,
          source_segment_ids: summary.decisions[index]?.source_segment_ids ?? [],
        }))
      const actionItems = draftActions
        .split('\n')
        .map((task) => task.trim())
        .filter(Boolean)
        .map((task, index) => ({
          task,
          owner_user_id: draftOwner.trim() || summary.action_items[index]?.owner_user_id || undefined,
          source_segment_ids: summary.action_items[index]?.source_segment_ids ?? [],
          source_message_ids: summary.action_items[index]?.source_message_ids ?? [],
          completed_at: summary.action_items[index]?.completed_at,
        }))
      const next = await patchSummary(meetingId, authToken, {
        summary: draftSummary,
        decisions,
        action_items: actionItems,
        risks: summary.risks,
        open_questions: summary.open_questions,
      })
      setSummary(next)
      setEditing(false)
      await refresh()
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '修订纪要失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleCompleteAction(index: number) {
    setBusy(true)
    setError('')
    try {
      setSummary(await completeActionItem(meetingId, authToken, index))
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '完成待办失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleSaveRetention() {
    setBusy(true)
    setError('')
    try {
      const policy = await putRetentionPolicy(authToken, {
        media_ttl_seconds: secondsFromDays(mediaDays),
        video_ttl_seconds: secondsFromDays(videoDays),
        knowledge_ttl_seconds: secondsFromDays(knowledgeDays),
      })
      setRetention(policy)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '保存保留策略失败')
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

  async function handleBreakDeny(reqId: string) {
    setBusy(true)
    setError('')
    try {
      await denyBreakGlass(meetingId, reqId, authToken)
      setBreakReqs(await listBreakGlass(meetingId, authToken))
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '破窗拒绝失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleBreakRevoke(reqId: string) {
    setBusy(true)
    setError('')
    try {
      await revokeBreakGlass(meetingId, reqId, authToken)
      setBreakReqs(await listBreakGlass(meetingId, authToken))
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '破窗撤销失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleAddShare() {
    if (!shareEmail.trim()) return
    setBusy(true)
    setError('')
    try {
      const added = await addSharedReader(meetingId, authToken, shareEmail.trim())
      setShares(added.readers)
      setShareCode(added.code ?? '')
      setShareMagicURL(added.magic_url ?? '')
      setShareEmail('')
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '添加分享失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleRemoveShare(email: string) {
    setBusy(true)
    setError('')
    try {
      setShares(await removeSharedReader(meetingId, authToken, email))
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '取消分享失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleClaimShare() {
    if (!claimEmail.trim()) return
    setBusy(true)
    setError('')
    try {
      await requestSharedReaderVerification(meetingId, claimEmail.trim())
      setClaimRequested(true)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '验证码发送失败')
    } finally {
      setBusy(false)
    }
  }

  async function handleConfirmShare() {
    if (!claimEmail.trim() || !claimCode.trim()) return
    setBusy(true)
    setError('')
    try {
      const verified = await confirmSharedReaderVerification(
        meetingId,
        claimEmail.trim(),
        claimCode.trim(),
      )
      sessionStorage.setItem('authToken', verified.access_token)
      sessionStorage.setItem('principalKind', 'guest')
      setAuthToken(verified.access_token)
      setVerifiedGuestEmail(verified.email)
      setClaimRequested(false)
      await refresh(verified.access_token)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '领取分享失败')
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
      const message = requestError instanceof Error ? requestError.message : '验证码发送失败'
      setVerificationRequested(true)
      setError(
        message.includes('guest_email_verification_unavailable')
          ? '当前没有配置邮件。请向组织者索取会中验证码，或打开他们复制给你的魔法链接。'
          : message,
      )
    } finally {
      setBusy(false)
    }
  }

  async function handleIssueInRoomCode() {
    if (!inRoomEmail.trim()) return
    setBusy(true)
    setError('')
    try {
      const issued = await issueInRoomGuestCode(
        meetingId,
        authToken,
        inRoomEmail.trim(),
        inRoomGuestID.trim(),
      )
      setInRoomIssued({ code: issued.code, magic_url: issued.magic_url })
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '出示验证码失败')
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
      e.action === 'artifact_view' ||
      e.action === 'meeting_left' ||
      e.action === 'summary_revised' ||
      e.action === 'pipeline_manual_review' ||
      e.action === 'pipeline_retry' ||
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
          缺独立音轨且无本机备份时会进入 <code>MANUAL_REVIEW</code>，不会静默生成不完整纪要。
        </p>}
        {pipeline?.pipeline_stage === 'MANUAL_REVIEW' && (
          <p className="error-message" role="status">
            本场会缺少权威音源（独立音轨或员工本机备份），已进入人工复核。房间混音不会被拿来顶上转写。
            组织者可以点「重新排队」让 Worker 再领一次任务。
          </p>
        )}
        {(pipeline?.pipeline_stage === 'RETRYABLE_ERROR') && (
          <p className="error-message" role="status">
            会后任务暂时失败，已记为可重试。点「重新排队」或等 Worker 按租约再领。
          </p>
        )}

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
          {(pipeline?.pipeline_stage === 'MANUAL_REVIEW' ||
            pipeline?.pipeline_stage === 'RETRYABLE_ERROR') && (
            <button
              className="primary-button"
              type="button"
              disabled={busy || !authToken}
              onClick={() => void handleRetryPipeline()}
            >
              重新排队会后任务
            </button>
          )}
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

        {!authToken && (
          <section className="meeting-card" style={{ marginTop: 28 }}>
            <div className="meeting-card-title">领取会外分享</div>
            <p className="chat-hint">
              组织者把你的邮箱加成读者后，在这里验证邮箱即可查看本场产物，无需进过会。
            </p>
            <div className="organizer-actions" style={{ gap: 8, flexWrap: 'wrap' }}>
              <input
                type="email"
                value={claimEmail}
                onChange={(event) => setClaimEmail(event.target.value)}
                placeholder="被分享的邮箱"
                aria-label="被分享的邮箱"
                autoComplete="email"
              />
              <button
                className="primary-button"
                type="button"
                disabled={busy || !claimEmail.trim()}
                onClick={() => void handleClaimShare()}
              >
                发送验证码
              </button>
              {claimRequested && (
                <>
                  <input
                    inputMode="numeric"
                    value={claimCode}
                    onChange={(event) => setClaimCode(event.target.value)}
                    placeholder="6 位验证码"
                    aria-label="分享验证码"
                    maxLength={6}
                  />
                  <button
                    className="primary-button accent"
                    type="button"
                    disabled={busy || !claimCode.trim()}
                    onClick={() => void handleConfirmShare()}
                  >
                    领取访问
                  </button>
                </>
              )}
            </div>
          </section>
        )}

        {isGuest && (
          <section className="meeting-card" style={{ marginTop: 28 }}>
            <div className="meeting-card-title">嘉宾邮箱验证</div>
            {verifiedGuestEmail ? (
              <p className="chat-hint">已验证 {verifiedGuestEmail}，当前会后令牌仅可访问本场会议。</p>
            ) : (
              <>
                <p className="chat-hint">
                  验证邮箱后可查看、下载和检索本场会后产物。有邮件时会同时收到 6 位码和魔法链接；没有邮件时请向组织者索取会中验证码。
                </p>
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

        {canManageShares && (
          <section className="meeting-card" style={{ marginTop: 28 }}>
            <div className="meeting-card-title">会外邮箱分享</div>
            <p className="chat-hint">
              组织者可把任意邮箱加成读者。添加后会出示验证码和魔法链接，可复制发给对方；有 SMTP 时对方也可以自己点「发送验证码」。
            </p>
            <div className="organizer-actions" style={{ gap: 8, flexWrap: 'wrap' }}>
              <input
                type="email"
                value={shareEmail}
                onChange={(event) => setShareEmail(event.target.value)}
                placeholder="reader@example.com"
                aria-label="分享邮箱"
              />
              <button
                className="primary-button"
                type="button"
                disabled={busy || !shareEmail.trim()}
                onClick={() => void handleAddShare()}
              >
                加入白名单
              </button>
            </div>
            {shareCode && (
              <p className="chat-hint" role="status">
                请把验证码 <code>{shareCode}</code> 或链接
                {' '}
                <code>{shareMagicURL}</code>
                {' '}
                发给对方。没有邮件时这就是领取方式。
              </p>
            )}
            {shares.length > 0 && (
              <ul className="audit-compact-list" style={{ marginTop: 12 }}>
                {shares.map((share) => (
                  <li key={share.email}>
                    <code>{share.email}</code>
                    {share.verified ? ' · 已验证' : ' · 待验证'}
                    {' '}
                    <button
                      className="primary-button"
                      type="button"
                      disabled={busy}
                      onClick={() => void handleRemoveShare(share.email)}
                    >
                      取消分享
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>
        )}

        {canManageShares && (
          <section className="meeting-card" style={{ marginTop: 28 }}>
            <div className="meeting-card-title">会中验证码</div>
            <p className="chat-hint">
              没有企业邮箱时，组织者可当场出示 6 位码，或把魔法链接复制给嘉宾。嘉宾入会后才会出现在下方名单里。
            </p>
            <div className="organizer-actions" style={{ gap: 8, flexWrap: 'wrap' }}>
              <input
                type="email"
                value={inRoomEmail}
                onChange={(event) => setInRoomEmail(event.target.value)}
                placeholder="嘉宾要绑定的邮箱"
                aria-label="会中验证邮箱"
              />
              <select
                value={inRoomGuestID}
                onChange={(event) => setInRoomGuestID(event.target.value)}
                aria-label="会中嘉宾"
              >
                <option value="">选择已入会嘉宾，或填写分享邮箱</option>
                {inRoomGuests.map((guest) => (
                  <option key={guest.guest_id} value={guest.guest_id}>
                    {guest.display_name || guest.guest_id}
                  </option>
                ))}
              </select>
              <button
                className="primary-button"
                type="button"
                disabled={busy || !inRoomEmail.trim()}
                onClick={() => void handleIssueInRoomCode()}
              >
                出示验证码
              </button>
            </div>
            {inRoomIssued && (
              <p className="chat-hint" role="status">
                验证码 <code>{inRoomIssued.code}</code>
                {' · '}
                链接 <code>{inRoomIssued.magic_url}</code>
              </p>
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
                      {' '}
                      <button
                        className="primary-button"
                        type="button"
                        disabled={busy}
                        onClick={() => void handleBreakDeny(req.id)}
                      >
                        拒绝
                      </button>
                    </>
                  ) : null}
                  {req.status === 'approved' ? (
                    <>
                      {' '}
                      <button
                        className="primary-button"
                        type="button"
                        disabled={busy}
                        onClick={() => void handleBreakRevoke(req.id)}
                      >
                        撤销
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
                <li key={`${hit.document.meeting_id}-${hit.document.source_type}-${hit.document.source_id ?? hit.snippet}`}>
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

        {retention && (
          <section className="meeting-card" style={{ marginTop: 28 }}>
            <div className="meeting-card-title">保留策略</div>
            <p className="chat-hint">
              媒体和知识是两套时钟。画面可以更短。填 0 表示该时钟不自动过期。到期后会删对象/索引，不能只删文件留下可检索块。
            </p>
            <div className="organizer-actions" style={{ gap: 8, flexWrap: 'wrap' }}>
              <label>
                媒体（天）
                <input
                  inputMode="numeric"
                  value={mediaDays}
                  onChange={(event) => setMediaDays(event.target.value)}
                  aria-label="媒体保留天数"
                />
              </label>
              <label>
                画面（天）
                <input
                  inputMode="numeric"
                  value={videoDays}
                  onChange={(event) => setVideoDays(event.target.value)}
                  aria-label="画面保留天数"
                />
              </label>
              <label>
                知识（天）
                <input
                  inputMode="numeric"
                  value={knowledgeDays}
                  onChange={(event) => setKnowledgeDays(event.target.value)}
                  aria-label="知识保留天数"
                />
              </label>
              <button
                className="primary-button"
                type="button"
                disabled={busy}
                onClick={() => void handleSaveRetention()}
              >
                保存策略
              </button>
            </div>
          </section>
        )}

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
                    {item.status === 'ready' && item.download_url ? (
                      <div className="media-player">
                        {item.kind === 'room_video' ? (
                          <video controls src={item.download_url} preload="metadata">
                            你的浏览器不支持视频回放。
                          </video>
                        ) : (
                          <audio controls src={item.download_url} preload="metadata">
                            你的浏览器不支持音频回放。
                          </audio>
                        )}
                      </div>
                    ) : null}
                    {item.status === 'ready' && item.object_key ? (
                      <button
                        className="primary-button"
                        type="button"
                        disabled={busy || !authToken}
                        onClick={() => void handleDownload('media', item.id)}
                      >
                        下载/回放（记审计）
                      </button>
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
            {editing ? (
              <>
                <textarea
                  value={draftSummary}
                  onChange={(event) => setDraftSummary(event.target.value)}
                  rows={5}
                  aria-label="修订摘要"
                  style={{ width: '100%' }}
                />
                <h3>决策（每行一条）</h3>
                <textarea
                  value={draftDecisions}
                  onChange={(event) => setDraftDecisions(event.target.value)}
                  rows={4}
                  aria-label="修订决策"
                  style={{ width: '100%' }}
                />
                <h3>待办（每行一条）</h3>
                <input
                  value={draftOwner}
                  onChange={(event) => setDraftOwner(event.target.value)}
                  placeholder="内部负责人员工 ID，可留空"
                  aria-label="待办负责人"
                />
                <textarea
                  value={draftActions}
                  onChange={(event) => setDraftActions(event.target.value)}
                  rows={4}
                  aria-label="修订待办"
                  style={{ width: '100%', marginTop: 8 }}
                />
                <div className="organizer-actions" style={{ marginTop: 12 }}>
                  <button className="primary-button accent" type="button" disabled={busy} onClick={() => void handleSaveSummary()}>
                    保存修订
                  </button>
                  <button className="primary-button" type="button" disabled={busy} onClick={() => setEditing(false)}>
                    取消
                  </button>
                </div>
              </>
            ) : (
              <>
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
                  <button
                    className="primary-button"
                    type="button"
                    disabled={busy || !authToken}
                    onClick={() => void handleDownload('export')}
                  >
                    导出转写和纪要
                  </button>
                  {!isGuest && (
                    <button className="primary-button" type="button" disabled={busy} onClick={startEdit}>
                      修订纪要
                    </button>
                  )}
                </div>
                <h3>决策</h3>
                <ul>
                  {summary.decisions.map((item, index) => (
                    <li key={`${item.text}-${index}`}>{citedLabel(item)}</li>
                  ))}
                </ul>
                <h3>待办</h3>
                <ul>
                  {summary.action_items.map((item, index) => (
                    <li key={`${item.task}-${index}`}>
                      {actionLabel(item)}
                      {!isGuest && !item.completed_at ? (
                        <>
                          {' '}
                          <button
                            className="primary-button"
                            type="button"
                            disabled={busy}
                            onClick={() => void handleCompleteAction(index)}
                          >
                            完成
                          </button>
                        </>
                      ) : null}
                    </li>
                  ))}
                </ul>
                {summary.risks.length > 0 && (
                  <>
                    <h3>风险</h3>
                    <ul>
                      {summary.risks.map((item, index) => (
                        <li key={`${item.text}-${index}`}>{citedLabel(item)}</li>
                      ))}
                    </ul>
                  </>
                )}
                {summary.original_json ? (
                  <details style={{ marginTop: 12 }}>
                    <summary>查看 AI 原稿</summary>
                    <pre className="media-artifact-detail">{summary.original_json}</pre>
                  </details>
                ) : null}
              </>
            )}
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
