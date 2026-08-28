import { useEffect, useMemo, useState } from 'react'
import { AppShell } from '../aura/AppShell'
import { Banner } from '../aura/Banner'
import { parseApiError } from '../aura/parseApiError'
import { Button } from '../aura/Button'
import { EmptyState } from '../aura/EmptyState'
import { TextField } from '../aura/TextField'
import {
  createMeeting,
  listEmployeeMeetings,
  type CreatedMeeting,
  type EmployeeMeeting,
} from '../lib/api'
import {
  datetimeLocalToRFC3339,
  isScheduleCreateValid,
  meetingStatusBadgeClass,
  meetingStatusBadgeText,
  resolveMeetingStatus,
} from '../lib/meetingSchedule'

type MeetingListPageProps = {
  employeeToken: string
  displayName?: string
  onLogout: () => void
  onCreated: (meeting: CreatedMeeting) => void
  onOpenMeeting: (meetingId: string) => void
}

export function MeetingListPage({
  employeeToken,
  displayName,
  onLogout,
  onCreated,
  onOpenMeeting,
}: MeetingListPageProps) {
  const [meetings, setMeetings] = useState<EmployeeMeeting[]>([])
  const [title, setTitle] = useState('即时会议')
  const [startsAtLocal, setStartsAtLocal] = useState('')
  const [endsAtLocal, setEndsAtLocal] = useState('')
  const [loading, setLoading] = useState(false)
  const [creating, setCreating] = useState(false)
  const [err, setErr] = useState<{ error?: string; message?: string }>({})

  const canCreate = useMemo(
    () => isScheduleCreateValid(startsAtLocal, endsAtLocal),
    [startsAtLocal, endsAtLocal],
  )

  async function refresh() {
    setLoading(true)
    setErr({})
    try {
      setMeetings(await listEmployeeMeetings(employeeToken))
    } catch (error) {
      setErr(parseApiError(error))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [employeeToken])

  async function handleCreate() {
    if (!canCreate) return
    setCreating(true)
    setErr({})
    try {
      const startsAt = datetimeLocalToRFC3339(startsAtLocal)
      const endsAt = datetimeLocalToRFC3339(endsAtLocal)
      const meeting = await createMeeting(
        employeeToken,
        title.trim() || '即时会议',
        [],
        [],
        startsAt,
        endsAt,
      )
      onCreated(meeting)
    } catch (error) {
      setErr(parseApiError(error))
    } finally {
      setCreating(false)
    }
  }

  return (
    <AppShell
      actions={
        <div className="flex items-center gap-2">
          {displayName ? <span className="text-sm text-secondary">{displayName}</span> : null}
          <Button variant="ghost" onClick={onLogout}>
            退出
          </Button>
        </div>
      }
    >
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="space-y-1">
          <h1 className="text-lg font-semibold tracking-tight">我的会议</h1>
          <p className="text-sm text-secondary">
            {displayName ? `你好，${displayName}。` : ''}
            创建会议后进入大厅，复制链接邀请嘉宾。
          </p>
        </div>
        <div className="flex flex-wrap items-end gap-2">
          <TextField label="标题" value={title} onChange={(e) => setTitle(e.target.value)} />
          <label className="flex flex-col gap-1 text-sm">
            <span className="text-secondary">开始时间（可选）</span>
            <input
              type="datetime-local"
              value={startsAtLocal}
              onChange={(e) => setStartsAtLocal(e.target.value)}
              className="rounded-lg border border-border bg-elevated px-3 py-2 text-sm text-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
            />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            <span className="text-secondary">结束时间（可选）</span>
            <input
              type="datetime-local"
              value={endsAtLocal}
              onChange={(e) => setEndsAtLocal(e.target.value)}
              className="rounded-lg border border-border bg-elevated px-3 py-2 text-sm text-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
            />
          </label>
          <Button loading={creating} disabled={!canCreate} onClick={() => void handleCreate()}>
            创建会议
          </Button>
        </div>
      </div>

      <Banner error={err.error} message={err.message} />

      {meetings.length === 0 && !loading ? (
        <EmptyState
          title="还没有会议"
          description="点击「创建会议」立即开始一场协作。"
          action={
            <Button loading={creating} disabled={!canCreate} onClick={() => void handleCreate()}>
              创建会议
            </Button>
          }
        />
      ) : (
        <ul className="divide-y divide-border rounded-lg border border-border bg-surface">
          {meetings.map((m) => {
            const status = resolveMeetingStatus(m)
            return (
              <li key={m.id} className="flex items-center justify-between gap-3 px-4 py-3">
                <div className="min-w-0">
                  <p className="truncate text-sm font-medium">{m.title}</p>
                  <p className="font-mono text-xs tracking-[0.2em] text-secondary">{m.id}</p>
                </div>
                <div className="flex items-center gap-2">
                  <span className={`text-xs font-medium ${meetingStatusBadgeClass(status)}`}>
                    {meetingStatusBadgeText(status)}
                    {status === 'live' && m.locked ? ' · 已锁定' : ''}
                  </span>
                  <Button
                    variant="ghost"
                    onClick={() =>
                      status === 'ended'
                        ? onOpenMeeting(m.id)
                        : window.location.assign(`/lobby/${encodeURIComponent(m.id)}`)
                    }
                  >
                    {status === 'ended' ? '纪要' : '进入'}
                  </Button>
                </div>
              </li>
            )
          })}
        </ul>
      )}

      {loading ? <p className="text-sm text-secondary">加载中…</p> : null}
    </AppShell>
  )
}
