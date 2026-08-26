import { type FormEvent, useState } from 'react'
import { ackRecording, livekitToken } from '../lib/api'
import { isTauriRuntime } from '../lib/client'
import { startLocalRecording } from '../lib/localRecording'

type JoinEmployeePageProps = {
  meetingId: string
}

function saveRoomSession(params: {
  lkToken: string
  lkUrl: string
  meetingId: string
  authToken: string
  canManageMeeting: boolean
  password?: string
}): void {
  sessionStorage.setItem('lkToken', params.lkToken)
  sessionStorage.setItem('lkUrl', params.lkUrl)
  sessionStorage.setItem('meetingId', params.meetingId)
  sessionStorage.setItem('authToken', params.authToken)
  sessionStorage.setItem('isOrganizer', params.canManageMeeting ? '1' : '0')
  sessionStorage.setItem('principalKind', 'employee')
  if (params.password) {
    sessionStorage.setItem('roomPassword', params.password)
  }
}

export function JoinEmployeePage({ meetingId }: JoinEmployeePageProps) {
  const [employeeToken, setEmployeeToken] = useState(
    () => sessionStorage.getItem('employeeToken') ?? '',
  )
  const [password, setPassword] = useState('')
  const [passwordRequired, setPasswordRequired] = useState(false)
  const [recordingAccepted, setRecordingAccepted] = useState(false)
  const [error, setError] = useState('')
  const [isJoining, setIsJoining] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!recordingAccepted) return

    const token = employeeToken.trim()
    if (!token) {
      setError('请输入员工 JWT')
      return
    }

    setError('')
    setIsJoining(true)
    try {
      await ackRecording(meetingId, token, password.trim() || undefined)
      if (isTauriRuntime()) {
        const local = await startLocalRecording(meetingId, token)
        if (!local.mic.active) {
          throw new Error(`本机麦克风备份未启动：${local.mic.error || '未知错误'}`)
        }
      }
      const credentials = await livekitToken(meetingId, token)
      sessionStorage.setItem('employeeToken', token)
      saveRoomSession({
        lkToken: credentials.token,
        lkUrl: credentials.livekit_url,
        meetingId,
        authToken: token,
        canManageMeeting: credentials.is_organizer,
        password: password.trim(),
      })
      window.location.assign('/room')
    } catch (requestError) {
      const message = requestError instanceof Error ? requestError.message : '加入会议失败'
      if (message.includes('meeting_password_required')) {
        setPasswordRequired(true)
        setError('这场会议未向当前员工发出邀请，请输入会议密码。')
      } else {
        setError(message)
      }
      setIsJoining(false)
    }
  }

  return (
    <main className="shell guest-shell">
      <header className="brand">
        <a href="/" className="brand-home" aria-label="返回员工首页">
          <span className="brand-mark" aria-hidden="true">
            M
          </span>
          <span>METUAI / EMPLOYEE ACCESS</span>
        </a>
        <span className="status-pill muted">INTERNAL</span>
      </header>

      <section className="guest-layout">
        <div className="guest-aside">
          <p className="eyebrow">员工会议入口 · 02</p>
          <h1>
            确认录音，
            <br />
            进入会议。
          </h1>
          <p className="meeting-reference">ROOM / {meetingId}</p>
          <div className="privacy-note">
            <span aria-hidden="true">◎</span>
            <p>本场会议将被企业服务器录音和转写。员工桌面端会在入会前自动启动本机麦克风备份。</p>
          </div>
        </div>

        <div className="panel guest-panel">
          <div className="panel-heading">
            <div>
              <p className="eyebrow">VERIFY ACCESS</p>
              <h2>员工入会</h2>
            </div>
            <span className="step-number">02</span>
          </div>

          <form onSubmit={handleSubmit}>
            <label>
              <span>员工 JWT</span>
              <textarea
                value={employeeToken}
                onChange={(event) => setEmployeeToken(event.target.value)}
                placeholder="粘贴员工身份令牌"
                rows={5}
                required
                spellCheck={false}
              />
            </label>

            {passwordRequired && (
              <label>
                <span>会议密码</span>
                <input
                  type="password"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  placeholder="未受邀员工需要会议密码"
                  autoComplete="current-password"
                  required
                />
              </label>
            )}

            <label className="consent">
              <input
                type="checkbox"
                checked={recordingAccepted}
                onChange={(event) => setRecordingAccepted(event.target.checked)}
                required
              />
              <span className="checkmark" aria-hidden="true">
                ✓
              </span>
              <span>
                <strong>我已知悉本会将被企业服务器录音与转写</strong>
                <small>本机麦克风备份仅适用于员工 Tauri 客户端，暂停不会停止服务端录制。</small>
              </span>
            </label>

            <button
              className="primary-button accent"
              type="submit"
              disabled={!recordingAccepted || isJoining}
            >
              {isJoining ? '正在确认并连接…' : '确认并进入会议'}
              <span aria-hidden="true">→</span>
            </button>
          </form>

          {error && (
            <p className="error-message" role="alert">
              {error}
            </p>
          )}
        </div>
      </section>
    </main>
  )
}
