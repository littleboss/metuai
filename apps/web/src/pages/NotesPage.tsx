import { useEffect, useState } from 'react'
import { AppShell } from '../aura/AppShell'
import { Banner } from '../aura/Banner'
import { parseApiError } from '../aura/parseApiError'
import { Button } from '../aura/Button'
import { NotesPanel } from '../aura/NotesPanel'
import {
  completeActionItem,
  generateSummary,
  getSummary,
  type ActionItem,
  type MeetingSummary,
} from '../lib/api'

type NotesPageProps = {
  meetingId: string
  authToken: string
}

function sleep(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms))
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
        // 已有纪要则直接展示。
        try {
          const existing = await getSummary(meetingId, authToken)
          if (cancelled) return
          setSummary(existing)
          setMode('ready')
          return
        } catch (error) {
          const parsed = parseApiError(error)
          if (parsed.error !== 'summary_not_ready') {
            if (cancelled) return
            setErr(parsed)
            if (parsed.error === 'no_transcript') {
              setMode('no-transcript')
            } else {
              setMode('no-transcript')
            }
            return
          }
        }

        // 未就绪：组织者触发私有 LLM 生成（非组织者可能 403，保持 generating/空态）。
        const generated = await generateSummary(meetingId, authToken)
        if (cancelled) return
        if ('accepted' in generated && generated.accepted) {
          const deadline = Date.now() + 60_000
          while (Date.now() < deadline) {
            await sleep(500)
            if (cancelled) return
            try {
              const sum = await getSummary(meetingId, authToken)
              if (cancelled) return
              setSummary(sum)
              setMode('ready')
              return
            } catch {
              // keep polling
            }
          }
          setErr({ error: 'summary_not_ready', message: '纪要仍在生成，请稍后刷新。' })
          setMode('generating')
          return
        }
        setSummary(generated as MeetingSummary)
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
        } else if (parsed.error === 'forbidden' || parsed.error === undefined) {
          // 非组织者无法触发生成：保持空态提示尚未就绪。
          setMode('no-transcript')
          setErr({
            error: parsed.error,
            message: parsed.message ?? '纪要尚未生成，请联系组织者。',
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
