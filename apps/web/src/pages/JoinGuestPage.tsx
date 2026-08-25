import { type FormEvent, useState } from 'react'
import { ackRecording, guestSession, livekitToken } from '../lib/api'

type JoinGuestPageProps = {
  meetingId: string
}

export function JoinGuestPage({ meetingId }: JoinGuestPageProps) {
  const [displayName, setDisplayName] = useState('')
  const [password, setPassword] = useState('')
  const [recordingAccepted, setRecordingAccepted] = useState(false)
  const [error, setError] = useState('')
  const [isJoining, setIsJoining] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    setIsJoining(true)

    try {
      // 嘉宾会话令牌先完成录音确认，再交换短时 LiveKit 入会令牌。
      const session = await guestSession(meetingId, password, displayName.trim())
      await ackRecording(meetingId, session.token)
      const credentials = await livekitToken(meetingId, session.token)
      sessionStorage.setItem('lkToken', credentials.token)
      sessionStorage.setItem('lkUrl', credentials.livekit_url)
      window.location.assign('/room')
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '加入会议失败')
      setIsJoining(false)
    }
  }

  return (
    <main className="shell guest-shell">
      <header className="brand">
        <a href="/" className="brand-home" aria-label="返回员工首页">
          <span className="brand-mark" aria-hidden="true">M</span>
          <span>METUAI / GUEST ACCESS</span>
        </a>
        <span className="status-pill muted">INVITED</span>
      </header>

      <section className="guest-layout">
        <div className="guest-aside">
          <p className="eyebrow">访客会议入口 · 02</p>
          <h1>你被邀请<br />加入对话。</h1>
          <p className="meeting-reference">ROOM / {meetingId}</p>
          <div className="privacy-note">
            <span aria-hidden="true">◎</span>
            <p>
              本场会议包含录音。只有确认知悉后，系统才会签发入会凭证。
            </p>
          </div>
        </div>

        <div className="panel guest-panel">
          <div className="panel-heading">
            <div>
              <p className="eyebrow">VERIFY IDENTITY</p>
              <h2>嘉宾验证</h2>
            </div>
            <span className="step-number">02</span>
          </div>

          <form onSubmit={handleSubmit}>
            <label>
              <span>显示名称</span>
              <input
                value={displayName}
                onChange={(event) => setDisplayName(event.target.value)}
                placeholder="会议中其他人看到的名字"
                autoComplete="name"
                required
              />
            </label>

            <label>
              <span>会议密码</span>
              <input
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                placeholder="输入邀请方提供的密码"
                autoComplete="current-password"
                required
              />
            </label>

            <label className="consent">
              <input
                type="checkbox"
                checked={recordingAccepted}
                onChange={(event) => setRecordingAccepted(event.target.checked)}
                required
              />
              <span className="checkmark" aria-hidden="true">✓</span>
              <span>
                <strong>我已知悉并同意会议录音</strong>
                <small>录音用于会议纪要与后续回顾。</small>
              </span>
            </label>

            <button
              className="primary-button accent"
              type="submit"
              disabled={!recordingAccepted || isJoining}
            >
              {isJoining ? '正在验证并连接…' : '确认并进入会议'}
              <span aria-hidden="true">→</span>
            </button>
          </form>

          {error && <p className="error-message" role="alert">{error}</p>}
        </div>
      </section>
    </main>
  )
}
