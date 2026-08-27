import { useEffect, useState } from 'react'
import { AppShell } from '../aura/AppShell'
import { Banner } from '../aura/Banner'
import { parseApiError } from '../aura/parseApiError'
import { Button } from '../aura/Button'
import { NotesPanel } from '../aura/NotesPanel'
import {
  completeActionItem,
  getSummary,
  type ActionItem,
  type MeetingSummary,
} from '../lib/api'

type NotesPageProps = {
  meetingId: string
  authToken: string
}

/** 会后纪要页：GeneratingSkeleton | NoTranscriptEmpty | Summary+TodoList。 */
export function NotesPage({ meetingId, authToken }: NotesPageProps) {
  const [mode, setMode] = useState<'generating' | 'no-transcript' | 'ready'>('generating')
  const [summary, setSummary] = useState<MeetingSummary | null>(null)
  const [err, setErr] = useState<{ error?: string; message?: string }>({})

  useEffect(() => {
    let cancelled = false
    void (async () => {
      setMode('generating')
      setErr({})
      try {
        const sum = await getSummary(meetingId, authToken)
        if (cancelled) return
        setSummary(sum)
        setMode('ready')
      } catch (error) {
        if (cancelled) return
        const parsed = parseApiError(error)
        setErr(parsed)
        if (parsed.error === 'no_transcript') {
          setMode('no-transcript')
        } else if (parsed.error === 'AI_NOT_CONFIGURED') {
          setMode('no-transcript')
          setErr({
            error: 'AI_NOT_CONFIGURED',
            message: parsed.message ?? '私有 LLM 未配置；会议仍可使用，不会发明待办。',
          })
        } else {
          setMode('no-transcript')
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [meetingId, authToken])

  async function toggleDone(index: number) {
    if (!summary) return
    const item: ActionItem | undefined = summary.action_items[index]
    if (!item || item.completed_at) return
    try {
      const next = await completeActionItem(meetingId, authToken, index)
      setSummary(next)
    } catch (error) {
      setErr(parseApiError(error))
    }
  }

  return (
    <AppShell
      actions={
        <Button variant="ghost" onClick={() => window.location.assign('/')}>
          返回列表
        </Button>
      }
    >
      <div className="space-y-1">
        <h1 className="text-lg font-semibold tracking-tight">会议纪要</h1>
        <p className="font-mono text-xs tracking-[0.2em] text-secondary">{meetingId}</p>
      </div>
      <Banner error={err.error} message={err.message} />
      <div className="rounded-lg border border-border bg-surface p-4">
        <NotesPanel
          mode={mode}
          summary={summary?.summary}
          actionItems={summary?.action_items}
          onToggleDone={authToken ? (i) => void toggleDone(i) : undefined}
        />
      </div>
    </AppShell>
  )
}
