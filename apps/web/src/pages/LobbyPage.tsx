import { useEffect, useState } from 'react'
import { AppShell } from '../aura/AppShell'
import { Banner } from '../aura/Banner'
import { parseApiError } from '../aura/parseApiError'
import { Button } from '../aura/Button'
import { JoinCodeBlock } from '../aura/JoinCodeBlock'
import { VideoTile } from '../aura/VideoTile'
import { ackRecording, getMeeting, livekitToken, type CreatedMeeting } from '../lib/api'
import { formatCountdown, hasMeetingStarted } from '../lib/meetingSchedule'
import { startLocalRecording } from '../lib/localRecording'
import { isTauriRuntime } from '../lib/client'

type LobbyPageProps = {
  meetingId: string
  employeeToken: string
  initial?: CreatedMeeting | null
}

/** 大厅：入会码、复制链接、设备预览、等待入会。 */
export function LobbyPage({ meetingId, employeeToken, initial }: LobbyPageProps) {
  const [password] = useState(initial?.password ?? sessionStorage.getItem('roomPassword') ?? '')
  const [title, setTitle] = useState(initial?.title ?? '')
  const [startsAt, setStartsAt] = useState<string | null>(initial?.starts_at ?? null)
  const [isOrganizer, setIsOrganizer] = useState(true)
  const [previewDenied, setPreviewDenied] = useState(false)
  const [stream, setStream] = useState<MediaStream | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<{ error?: string; message?: string }>({})
  const [linkCopied, setLinkCopied] = useState(false)
  const [nowMs, setNowMs] = useState(() => Date.now())

  const guestLink = `${window.location.origin}/join/${encodeURIComponent(meetingId)}`
  const waitingToStart = Boolean(startsAt && !hasMeetingStarted(startsAt, nowMs))
  const countdownText = startsAt ? formatCountdown(startsAt, nowMs) : ''

  useEffect(() => {
    if (initial?.password) {
      sessionStorage.setItem('roomPassword', initial.password)
      sessionStorage.setItem('meetingId', meetingId)
    }
    void (async () => {
      try {
        const info = await getMeeting(meetingId, employeeToken)
        setTitle(info.title)
        setStartsAt(info.starts_at ?? null)
        setIsOrganizer(info.organizer_id === parseSub(employeeToken))
      } catch {
        /* 列表进入时可能尚无详情字段 */
      }
    })()
  }, [meetingId, employeeToken, initial])

  useEffect(() => {
    if (!startsAt || hasMeetingStarted(startsAt)) return
    const timer = window.setInterval(() => setNowMs(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [startsAt])

  useEffect(() => {
    let active = true
    void navigator.mediaDevices
      ?.getUserMedia({ audio: true, video: true })
      .then((media) => {
        if (!active) {
          media.getTracks().forEach((t) => t.stop())
          return
        }
        setStream(media)
        setPreviewDenied(false)
      })
      .catch(() => {
        if (active) setPreviewDenied(true)
      })
    return () => {
      active = false
      stream?.getTracks().forEach((t) => t.stop())
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function copyLink() {
    await navigator.clipboard.writeText(guestLink)
    setLinkCopied(true)
    window.setTimeout(() => setLinkCopied(false), 2000)
  }

  async function enterRoom() {
    if (waitingToStart) return
    setLoading(true)
    setErr({})
    try {
      await ackRecording(meetingId, employeeToken, password || undefined)
      if (isTauriRuntime()) {
        try {
          await startLocalRecording(meetingId, employeeToken)
        } catch {
          /* 本机录音失败不阻断入会：与 cam/mic denied 同策略 */
        }
      }
      const credentials = await livekitToken(meetingId, employeeToken)
      sessionStorage.setItem('lkToken', credentials.token)
      sessionStorage.setItem('lkUrl', credentials.livekit_url)
      sessionStorage.setItem('meetingId', meetingId)
      sessionStorage.setItem('authToken', employeeToken)
      sessionStorage.setItem('isOrganizer', credentials.is_organizer || isOrganizer ? '1' : '0')
      sessionStorage.setItem('principalKind', 'employee')
      if (password) sessionStorage.setItem('roomPassword', password)
      window.location.assign('/room')
    } catch (error) {
      const parsed = parseApiError(error)
      setErr(parsed)
      setLoading(false)
    }
  }

  return (
    <AppShell>
      <div className="space-y-1">
        <h1 className="text-lg font-semibold tracking-tight">{title || '会议大厅'}</h1>
        <p className="text-sm text-secondary">
          {waitingToStart
            ? '会议尚未开始，可先复制链接与房间密码，到点后进入会场。'
            : '确认设备后进入会场，或复制链接邀请嘉宾。'}
        </p>
      </div>

      <Banner error={err.error} message={err.message} />

      {waitingToStart && startsAt ? (
        <div className="rounded-lg border border-border bg-elevated p-6 text-center">
          <p className="text-sm text-secondary">距离开始还有</p>
          <p className="mt-2 font-mono text-4xl font-semibold tracking-wide text-accent">{countdownText}</p>
          <p className="mt-3 text-xs text-secondary">
            {new Date(startsAt).toLocaleString(undefined, {
              dateStyle: 'medium',
              timeStyle: 'short',
            })}
          </p>
        </div>
      ) : null}

      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-3">
          {password ? <JoinCodeBlock code={password} label="房间密码" /> : null}
          <div className="rounded-lg border border-border bg-elevated p-3 space-y-2">
            <p className="text-xs text-secondary">嘉宾链接</p>
            <p className="break-all font-mono text-xs tracking-wide text-text">{guestLink}</p>
            <Button variant="ghost" onClick={() => void copyLink()} aria-label="复制嘉宾链接">
              {linkCopied ? '已复制' : '复制链接'}
            </Button>
          </div>
          <Button
            loading={loading}
            disabled={waitingToStart}
            onClick={() => void enterRoom()}
            className="w-full"
          >
            {waitingToStart ? '未到开始时间' : '进入会场'}
          </Button>
        </div>

        <VideoTile
          name="设备预览"
          state={previewDenied ? 'permission-denied' : stream ? 'streaming' : 'cam-off'}
        >
          {stream ? (
            <video
              className="h-full w-full object-cover"
              autoPlay
              muted
              playsInline
              ref={(el) => {
                if (el && stream) el.srcObject = stream
              }}
            />
          ) : null}
        </VideoTile>
      </div>
    </AppShell>
  )
}

function parseSub(token: string): string {
  try {
    const payload = JSON.parse(atob(token.split('.')[1] ?? '')) as { sub?: string }
    return payload.sub ?? ''
  } catch {
    return ''
  }
}
