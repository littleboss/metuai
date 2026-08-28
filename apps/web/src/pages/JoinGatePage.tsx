import { type FormEvent, useEffect, useState } from 'react'
import { AppShell } from '../aura/AppShell'
import { Banner } from '../aura/Banner'
import { parseApiError } from '../aura/parseApiError'
import { Button } from '../aura/Button'
import { EmptyState } from '../aura/EmptyState'
import { SecretField, TextField } from '../aura/TextField'
import { ackRecording, guestSession, livekitToken } from '../lib/api'
import { formatCountdown } from '../lib/meetingSchedule'
import { AuthPage } from './AuthPage'

type JoinGatePageProps = {
  meetingId: string
  /** employee = 员工链接入会；guest = 嘉宾链接 */
  mode?: 'guest' | 'employee'
}

/**
 * 入会门：显示名 → 房间密码 → 录音确认勾选。
 * 未勾选禁用加入；错误密码展示 403 invalid_password；未确认录音不得进入网格。
 * 员工模式依赖已登录会话（/v1/auth/login|register）；无会话时展示 AuthPage。
 */
export function JoinGatePage({ meetingId, mode = 'guest' }: JoinGatePageProps) {
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [recordingAccepted, setRecordingAccepted] = useState(false)
  const employeeToken = sessionStorage.getItem('employeeToken') ?? ''
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<{ error?: string; message?: string }>({})
  const [notStartedAt, setNotStartedAt] = useState<string | null>(null)
  const [nowMs, setNowMs] = useState(() => Date.now())

  useEffect(() => {
    if (!notStartedAt) return
    const timer = window.setInterval(() => setNowMs(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [notStartedAt])

  if (mode === 'employee' && !employeeToken.trim()) {
    return (
      <AuthPage
        onAuthenticated={(tok, user) => {
          sessionStorage.setItem('employeeToken', tok)
          sessionStorage.setItem('employeeUser', JSON.stringify(user))
          window.location.reload()
        }}
      />
    )
  }

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    if (!recordingAccepted) return

    setLoading(true)
    setErr({})
    setNotStartedAt(null)
    try {
      if (mode === 'employee') {
        const token = employeeToken.trim()
        await ackRecording(meetingId, token, password || undefined)
        const credentials = await livekitToken(meetingId, token)
        sessionStorage.setItem('lkToken', credentials.token)
        sessionStorage.setItem('lkUrl', credentials.livekit_url)
        sessionStorage.setItem('meetingId', meetingId)
        sessionStorage.setItem('authToken', token)
        sessionStorage.setItem('isOrganizer', credentials.is_organizer ? '1' : '0')
        sessionStorage.setItem('principalKind', 'employee')
        window.location.assign('/room')
        return
      }

      const session = await guestSession(meetingId, password, displayName.trim())
      await ackRecording(meetingId, session.token)
      const credentials = await livekitToken(meetingId, session.token)
      sessionStorage.setItem('lkToken', credentials.token)
      sessionStorage.setItem('lkUrl', credentials.livekit_url)
      sessionStorage.setItem('meetingId', meetingId)
      sessionStorage.setItem('authToken', session.token)
      sessionStorage.setItem('isOrganizer', '0')
      sessionStorage.setItem('principalKind', 'guest')
      window.location.assign('/room')
    } catch (error) {
      const parsed = parseApiError(error)
      setErr(parsed)
      if (parsed.error === 'meeting_not_started') {
        try {
          const body = JSON.parse((error as Error).message) as { starts_at?: string }
          if (body.starts_at) setNotStartedAt(body.starts_at)
        } catch {
          /* starts_at 可选 */
        }
      }
      setLoading(false)
    }
  }

  if (err.error === 'meeting_not_started') {
    return (
      <AppShell title="METUAI / 入会">
        <div className="mx-auto flex w-full max-w-md flex-col gap-4">
          <EmptyState
            title="未到开始时间"
            description={
              notStartedAt
                ? `会议将于 ${new Date(notStartedAt).toLocaleString(undefined, {
                    dateStyle: 'medium',
                    timeStyle: 'short',
                  })} 开始。`
                : '会议尚未开始，请稍后再试。'
            }
            action={
              notStartedAt ? (
                <p className="font-mono text-2xl font-semibold text-accent">
                  {formatCountdown(notStartedAt, nowMs)}
                </p>
              ) : null
            }
          />
          <Banner error={err.error} message="会议尚未开始，暂无法进入会场。" />
        </div>
      </AppShell>
    )
  }

  return (
    <AppShell title="METUAI / 入会">
      <div className="mx-auto flex w-full max-w-md flex-col gap-2">
        <div className="space-y-2">
          <h1 className="text-lg font-semibold tracking-tight">
            {mode === 'employee' ? '员工入会' : '嘉宾入会'}
          </h1>
          <p className="font-mono text-xs tracking-[0.2em] text-secondary">{meetingId}</p>
          <p className="text-sm text-secondary">确认房间密码与录音须知后进入会场。</p>
        </div>

        <Banner error={err.error} message={err.message} />

        <form className="flex flex-col gap-2" onSubmit={(e) => void handleSubmit(e)}>
          {mode === 'guest' ? (
            <TextField
              label="显示名称"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              required
              autoComplete="name"
            />
          ) : null}

          <SecretField
            label="房间密码"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required={mode === 'guest'}
            autoComplete="current-password"
            hint={mode === 'employee' ? '受邀员工可不填；未受邀员工必须填写' : undefined}
          />

          <label className="flex items-start gap-2 rounded-lg border border-border bg-elevated p-3 text-sm">
            <input
              type="checkbox"
              className="mt-1 size-4 rounded border-border accent-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
              checked={recordingAccepted}
              onChange={(e) => setRecordingAccepted(e.target.checked)}
            />
            <span>
              <span className="font-medium text-text">我已知悉并同意会议录音与转写</span>
              <span className="mt-1 block text-secondary">
                未勾选无法加入。嘉宾端无本机麦克风备份。
              </span>
            </span>
          </label>

          <Button type="submit" className="w-full" loading={loading} disabled={!recordingAccepted}>
            加入会议
          </Button>
        </form>
      </div>
    </AppShell>
  )
}
