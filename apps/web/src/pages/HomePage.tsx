import { type FormEvent, useState } from 'react'
import {
  ackRecording,
  createMeeting,
  livekitToken,
  type CreatedMeeting,
} from '../lib/api'

function saveRoomCredentials(token: string, url: string): void {
  // 房间凭证仅保存在当前浏览器标签会话中，关闭标签后自动清除。
  sessionStorage.setItem('lkToken', token)
  sessionStorage.setItem('lkUrl', url)
}

export function HomePage() {
  const [employeeToken, setEmployeeToken] = useState('')
  const [title, setTitle] = useState('')
  const [meeting, setMeeting] = useState<CreatedMeeting | null>(null)
  const [error, setError] = useState('')
  const [isCreating, setIsCreating] = useState(false)
  const [isJoining, setIsJoining] = useState(false)

  async function handleCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    setIsCreating(true)

    try {
      const created = await createMeeting(employeeToken.trim(), title.trim())
      setMeeting(created)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '创建会议失败')
    } finally {
      setIsCreating(false)
    }
  }

  async function handleJoin() {
    if (!meeting) return

    setError('')
    setIsJoining(true)

    try {
      await ackRecording(meeting.id, employeeToken.trim())
      const credentials = await livekitToken(meeting.id, employeeToken.trim())
      saveRoomCredentials(credentials.token, credentials.livekit_url)
      window.location.assign('/room')
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '进入会议失败')
      setIsJoining(false)
    }
  }

  return (
    <main className="shell">
      <header className="brand">
        <span className="brand-mark" aria-hidden="true">
          M
        </span>
        <span>METUAI / MEETING DESK</span>
        <span className="status-pill">
          <i aria-hidden="true" /> DEV ONLINE
        </span>
      </header>

      <section className="hero-grid">
        <div className="intro">
          <p className="eyebrow">员工会议入口 · 01</p>
          <h1>开一场<br />清醒的会议。</h1>
          <p className="lede">
            创建受密码保护的房间，确认录音告知后，通过 LiveKit 安全入会。
          </p>
          <div className="signal" aria-label="服务链路">
            <span>GATEWAY</span><b>→</b><span>LIVEKIT</span><b>→</b><span>ROOM</span>
          </div>
        </div>

        <div className="panel">
          <div className="panel-heading">
            <div>
              <p className="eyebrow">CREATE SESSION</p>
              <h2>新建会议</h2>
            </div>
            <span className="step-number">01</span>
          </div>

          <form onSubmit={handleCreate}>
            <label>
              <span>员工 JWT</span>
              <textarea
                value={employeeToken}
                onChange={(event) => setEmployeeToken(event.target.value)}
                placeholder="粘贴 go run ./cmd/devtoken 生成的令牌"
                rows={5}
                required
                spellCheck={false}
              />
            </label>

            <label>
              <span>会议标题</span>
              <input
                value={title}
                onChange={(event) => setTitle(event.target.value)}
                placeholder="例如：产品方案评审"
                required
              />
            </label>

            <button className="primary-button" type="submit" disabled={isCreating}>
              {isCreating ? '正在创建…' : '创建会议'}
              <span aria-hidden="true">↗</span>
            </button>
          </form>

          {error && <p className="error-message" role="alert">{error}</p>}

          {meeting && (
            <section className="meeting-card" aria-live="polite">
              <div className="meeting-card-title">
                <span>会议已就绪</span>
                <i aria-hidden="true" />
              </div>
              <dl>
                <div>
                  <dt>会议 ID</dt>
                  <dd>{meeting.id}</dd>
                </div>
                <div>
                  <dt>嘉宾密码</dt>
                  <dd>{meeting.password}</dd>
                </div>
              </dl>
              <p className="share-link">
                嘉宾入口：<code>{`${window.location.origin}/join/${meeting.id}`}</code>
              </p>
              <button
                className="primary-button accent"
                type="button"
                onClick={handleJoin}
                disabled={isJoining}
              >
                {isJoining ? '正在连接…' : '确认录音并入会'}
                <span aria-hidden="true">●</span>
              </button>
            </section>
          )}
        </div>
      </section>
    </main>
  )
}
