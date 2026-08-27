import { type FormEvent, useState } from 'react'
import { AppShell } from '../aura/AppShell'
import { Banner } from '../aura/Banner'
import { parseApiError } from '../aura/parseApiError'
import { Button } from '../aura/Button'
import { SecretField, TextField } from '../aura/TextField'
import { ackRecording, guestSession, livekitToken } from '../lib/api'

type JoinGatePageProps = {
  meetingId: string
  /** employee = 员工链接入会；guest = 嘉宾链接 */
  mode?: 'guest' | 'employee'
}

/**
 * 入会门：显示名 → 房间密码 → 录音确认勾选。
 * 未勾选禁用加入；错误密码展示 403 invalid_password；未确认录音不得进入网格。
 */
export function JoinGatePage({ meetingId, mode = 'guest' }: JoinGatePageProps) {
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [recordingAccepted, setRecordingAccepted] = useState(false)
  const [employeeToken, setEmployeeToken] = useState(
    () => sessionStorage.getItem('employeeToken') ?? '',
  )
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<{ error?: string; message?: string }>({})

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    if (!recordingAccepted) return

    setLoading(true)
    setErr({})
    try {
      if (mode === 'employee') {
        const token = employeeToken.trim()
        if (!token) {
          setErr({ error: 'unauthorized', message: '请粘贴员工 JWT' })
          setLoading(false)
          return
        }
        await ackRecording(meetingId, token, password || undefined)
        const credentials = await livekitToken(meetingId, token)
        sessionStorage.setItem('employeeToken', token)
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
      setErr(parseApiError(error))
      setLoading(false)
    }
  }

  return (
    <AppShell title="METUAI / 入会">
      <div className="mx-auto w-full max-w-md space-y-6">
        <div className="space-y-2">
          <h1 className="text-lg font-semibold tracking-tight">
            {mode === 'employee' ? '员工入会' : '嘉宾入会'}
          </h1>
          <p className="font-mono text-xs tracking-[0.2em] text-secondary">{meetingId}</p>
          <p className="text-sm text-secondary">确认显示名、房间密码与录音须知后进入会场。</p>
        </div>

        <Banner error={err.error} message={err.message} />

        <form className="space-y-4" onSubmit={(e) => void handleSubmit(e)}>
          {mode === 'guest' ? (
            <TextField
              label="显示名称"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              required
              autoComplete="name"
            />
          ) : (
            <SecretField
              label="员工 JWT"
              value={employeeToken}
              onChange={(e) => setEmployeeToken(e.target.value)}
              required
            />
          )}

          <SecretField
            label="房间密码"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required={mode === 'guest'}
            autoComplete="current-password"
            hint={mode === 'employee' ? '受邀员工可不填；未受邀员工必须填写' : undefined}
          />

          <label className="flex items-start gap-3 rounded-lg border border-border bg-elevated p-3 text-sm">
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
