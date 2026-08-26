import { type FormEvent, useEffect, useState } from 'react'
import {
  ackRecording,
  createMeeting,
  listDirectoryEmployees,
  listEmployeeMeetings,
  listManualReview,
  livekitToken,
  recordEmployeeLogin,
  recordEmployeeLogout,
  type CreatedMeeting,
  type DirectoryEmployee,
  type EmployeeMeeting,
} from '../lib/api'
import { resumeAllPendingUploads, startLocalRecording } from '../lib/localRecording'
import { isTauriRuntime } from '../lib/client'

function saveRoomSession(params: {
  lkToken: string
  lkUrl: string
  meetingId: string
  authToken: string
  isOrganizer: boolean
  password?: string
}): void {
  // 房间与组织者控制所需凭证只存在于当前标签会话。
  sessionStorage.setItem('lkToken', params.lkToken)
  sessionStorage.setItem('lkUrl', params.lkUrl)
  sessionStorage.setItem('meetingId', params.meetingId)
  sessionStorage.setItem('authToken', params.authToken)
  sessionStorage.setItem('isOrganizer', params.isOrganizer ? '1' : '0')
  // 员工身份：桌面端才允许本机麦克风备份。
  sessionStorage.setItem('principalKind', 'employee')
  if (params.password) {
    sessionStorage.setItem('roomPassword', params.password)
  }
}

function employeeIDs(value: string): string[] {
  return [...new Set(value.split(/[\s,]+/).map((id) => id.trim()).filter(Boolean))]
}

function toggleListedID(current: string, userID: string): string {
  const ids = new Set(employeeIDs(current))
  if (ids.has(userID)) {
    ids.delete(userID)
  } else {
    ids.add(userID)
  }
  return [...ids].join('\n')
}

export function HomePage() {
  const [employeeToken, setEmployeeToken] = useState('')
  const [title, setTitle] = useState('')
  const [invitedEmployeeIDs, setInvitedEmployeeIDs] = useState('')
  const [coOrganizerIDs, setCoOrganizerIDs] = useState('')
  const [meeting, setMeeting] = useState<CreatedMeeting | null>(null)
  const [meetings, setMeetings] = useState<EmployeeMeeting[]>([])
  const [directory, setDirectory] = useState<DirectoryEmployee[]>([])
  const [reviews, setReviews] = useState<EmployeeMeeting[]>([])
  const [recordingAccepted, setRecordingAccepted] = useState(false)
  const [error, setError] = useState('')
  const [isCreating, setIsCreating] = useState(false)
  const [isJoining, setIsJoining] = useState(false)
  const [isLoadingMeetings, setIsLoadingMeetings] = useState(false)
  const [resumeNote, setResumeNote] = useState('')

  useEffect(() => {
    const stored = sessionStorage.getItem('employeeToken') ?? ''
    if (stored && !employeeToken) {
      setEmployeeToken(stored)
    }
    const token = stored.trim()
    if (!token || !isTauriRuntime()) {
      return
    }
    void resumeAllPendingUploads(token)
      .then((count) => {
        if (count > 0) {
          setResumeNote(`已自动恢复 ${count} 条待传本机录音`)
        }
      })
      .catch(() => {
        setResumeNote('发现待传录音，但自动恢复失败，请进会后手动点「恢复上传」。')
      })
    // 仅在桌面端启动时尝试一次，避免把 token 变化当成新的恢复任务。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function loadMeetings() {
    const token = employeeToken.trim()
    if (!token) {
      setError('请先输入员工 JWT')
      return
    }
    setError('')
    setIsLoadingMeetings(true)
    try {
      const listed = await listEmployeeMeetings(token)
      sessionStorage.setItem('employeeToken', token)
      setMeetings(listed)
      if (sessionStorage.getItem('sessionLoginAudited') !== '1') {
        try {
          await recordEmployeeLogin(token)
          sessionStorage.setItem('sessionLoginAudited', '1')
        } catch {
          /* 登录审计失败不阻断列表 */
        }
      }
      try {
        setDirectory(await listDirectoryEmployees(token))
      } catch {
        setDirectory([])
      }
      try {
        setReviews(await listManualReview(token))
      } catch {
        setReviews([])
      }
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '无法加载会议')
    } finally {
      setIsLoadingMeetings(false)
    }
  }

  async function handleCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    setIsCreating(true)

    try {
      const token = employeeToken.trim()
      const created = await createMeeting(
        token,
        title.trim(),
        employeeIDs(invitedEmployeeIDs),
        employeeIDs(coOrganizerIDs),
      )
      sessionStorage.setItem('employeeToken', token)
      setMeeting(created)
      setRecordingAccepted(false)
      setMeetings((current) => [
        {
          id: created.id,
          title: created.title,
          organizer_id: created.organizer_id,
          locked: false,
          ended: false,
          pipeline_stage: '',
          created_at: new Date().toISOString(),
        },
        ...current.filter((item) => item.id !== created.id),
      ])
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : '创建会议失败')
    } finally {
      setIsCreating(false)
    }
  }

  async function handleLogout() {
    const token = employeeToken.trim()
    if (token) {
      try {
        await recordEmployeeLogout(token)
      } catch {
        /* 仍清理本地会话 */
      }
    }
    sessionStorage.removeItem('employeeToken')
    sessionStorage.removeItem('sessionLoginAudited')
    sessionStorage.removeItem('authToken')
    setEmployeeToken('')
    setMeetings([])
    setDirectory([])
    setReviews([])
    setMeeting(null)
  }

  function openEmployeeMeeting(meetingId: string) {
    const token = employeeToken.trim()
    if (!token) {
      setError('请先输入员工 JWT')
      return
    }
    sessionStorage.setItem('employeeToken', token)
    window.location.assign(`/employee-join/${encodeURIComponent(meetingId)}`)
  }

  async function handleJoin() {
    if (!meeting || !recordingAccepted) return

    setError('')
    setIsJoining(true)

    try {
      await ackRecording(meeting.id, employeeToken.trim())
      const credentials = await livekitToken(meeting.id, employeeToken.trim())
      sessionStorage.setItem('principalKind', 'employee')
      if (isTauriRuntime()) {
        const local = await startLocalRecording(meeting.id, employeeToken.trim())
        if (!local.mic.active) {
          throw new Error(`本机麦克风备份未启动：${local.mic.error || '未知错误'}`)
        }
      }
      saveRoomSession({
        lkToken: credentials.token,
        lkUrl: credentials.livekit_url,
        meetingId: meeting.id,
        authToken: employeeToken.trim(),
        isOrganizer: credentials.is_organizer,
        password: meeting.password,
      })
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
        {employeeToken.trim() && (
          <button className="brand-home" type="button" onClick={() => void handleLogout()}>
            退出登录
          </button>
        )}
      </header>

      <section className="hero-grid">
        <div className="intro">
          <p className="eyebrow">员工会议入口 · 01</p>
          <h1>
            开一场
            <br />
            清醒的会议。
          </h1>
          <p className="lede">
            创建受密码保护的房间，确认录音告知后，通过 LiveKit 安全入会。员工暂停的是本机备份，不是服务端录制。
          </p>
          <div className="signal" aria-label="服务链路">
            <span>GATEWAY</span>
            <b>→</b>
            <span>LIVEKIT</span>
            <b>→</b>
            <span>ROOM</span>
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

            <label>
              <span>受邀员工 ID</span>
              <textarea
                value={invitedEmployeeIDs}
                onChange={(event) => setInvitedEmployeeIDs(event.target.value)}
                placeholder="多个员工 ID 用空格、逗号或换行分隔；也可从下方目录点选"
                rows={2}
              />
            </label>
            {directory.length > 0 && (
              <div className="directory-picks" role="group" aria-label="最近一起开会的同事">
                {directory.map((person) => {
                  const selected = employeeIDs(invitedEmployeeIDs).includes(person.user_id)
                  return (
                    <button
                      key={person.user_id}
                      type="button"
                      aria-pressed={selected}
                      onClick={() =>
                        setInvitedEmployeeIDs((current) => toggleListedID(current, person.user_id))
                      }
                    >
                      {person.display_name}
                      <small>{person.user_id}</small>
                    </button>
                  )
                })}
              </div>
            )}

            <label>
              <span>共同组织者 ID</span>
              <textarea
                value={coOrganizerIDs}
                onChange={(event) => setCoOrganizerIDs(event.target.value)}
                placeholder="共同组织者会自动加入内部邀请"
                rows={2}
              />
            </label>

            <button className="primary-button" type="submit" disabled={isCreating}>
              {isCreating ? '正在创建…' : '创建会议'}
              <span aria-hidden="true">↗</span>
            </button>
          </form>

          {error && (
            <p className="error-message" role="alert">
              {error}
            </p>
          )}
          {resumeNote && (
            <p className="chat-hint" role="status">
              {resumeNote}
            </p>
          )}

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
                嘉宾入口：
                <code>{`${window.location.origin}/join/${meeting.id}`}</code>
              </p>
              <p className="share-link">
                员工入口（复制发给受邀同事）：
                <code>{`${window.location.origin}/employee-join/${meeting.id}`}</code>
              </p>

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
                  <small>
                    可能进入知识库。员工本机麦克风备份可暂停，暂停的是本地备份，不是服务端录制。
                  </small>
                </span>
              </label>

              <button
                className="primary-button accent"
                type="button"
                onClick={handleJoin}
                disabled={!recordingAccepted || isJoining}
              >
                {isJoining ? '正在连接…' : '确认录音并入会'}
                <span aria-hidden="true">●</span>
              </button>
            </section>
          )}

          <section className="meeting-card" aria-live="polite">
            <div className="meeting-card-title">我的会议</div>
            <button
              className="primary-button"
              type="button"
              onClick={() => void loadMeetings()}
              disabled={isLoadingMeetings}
            >
              {isLoadingMeetings ? '正在加载…' : '加载会议列表'}
              <span aria-hidden="true">↓</span>
            </button>
            {meetings.length > 0 && (
              <ul className="employee-meeting-list">
                {meetings.map((item) => (
                  <li key={item.id}>
                    <div>
                      <strong>{item.title}</strong>
                      <small>
                        {item.id} · {item.ended ? '已结束' : item.locked ? '已锁定' : '可加入'}
                      </small>
                    </div>
                    <button
                      type="button"
                      disabled={item.ended}
                      onClick={() => openEmployeeMeeting(item.id)}
                    >
                      进入
                    </button>
                    {item.ended && (
                      <a className="after-link" href={`/meeting/${item.id}`}>
                        会后
                      </a>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>

          {reviews.length > 0 && (
            <section className="meeting-card" aria-live="polite">
              <div className="meeting-card-title">待人工复核</div>
              <p className="chat-hint">缺权威音源或任务多次失败的会会出现在这里，点进去可以重新排队。</p>
              <ul className="employee-meeting-list">
                {reviews.map((item) => (
                  <li key={item.id}>
                    <div>
                      <strong>{item.title}</strong>
                      <small>{item.id} · MANUAL_REVIEW</small>
                    </div>
                    <a className="after-link" href={`/meeting/${item.id}`}>
                      处理
                    </a>
                  </li>
                ))}
              </ul>
            </section>
          )}
        </div>
      </section>
    </main>
  )
}
