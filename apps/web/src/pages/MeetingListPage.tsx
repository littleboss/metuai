import { useEffect, useState } from 'react'
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
  const [loading, setLoading] = useState(false)
  const [creating, setCreating] = useState(false)
  const [err, setErr] = useState<{ error?: string; message?: string }>({})

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
    setCreating(true)
    setErr({})
    try {
      const meeting = await createMeeting(employeeToken, title.trim() || '即时会议')
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
          <Button loading={creating} onClick={() => void handleCreate()}>
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
            <Button loading={creating} onClick={() => void handleCreate()}>
              创建会议
            </Button>
          }
        />
      ) : (
        <ul className="divide-y divide-border rounded-lg border border-border bg-surface">
          {meetings.map((m) => (
            <li key={m.id} className="flex items-center justify-between gap-3 px-4 py-3">
              <div className="min-w-0">
                <p className="truncate text-sm font-medium">{m.title}</p>
                <p className="font-mono text-xs tracking-[0.2em] text-secondary">{m.id}</p>
              </div>
              <div className="flex items-center gap-2">
                <span className="text-xs text-secondary">{m.ended ? '已结束' : m.locked ? '已锁定' : '进行中'}</span>
                <Button
                  variant="ghost"
                  onClick={() =>
                    m.ended ? onOpenMeeting(m.id) : window.location.assign(`/lobby/${encodeURIComponent(m.id)}`)
                  }
                >
                  {m.ended ? '纪要' : '进入'}
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}

      {loading ? <p className="text-sm text-secondary">加载中…</p> : null}
    </AppShell>
  )
}
