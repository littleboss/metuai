import { useEffect, useState } from 'react'
import { AppShell } from '../aura/AppShell'
import { Banner } from '../aura/Banner'
import { parseApiError } from '../aura/parseApiError'
import { Button } from '../aura/Button'
import { NotesPanel } from '../aura/NotesPanel'
import { TranscriptPanel } from '../aura/TranscriptPanel'
import {
  completeActionItem,
  generateSummary,
  generateTranscript,
  getSummary,
  getTranscript,
  type ActionItem,
  type MeetingSummary,
  type TranscriptSegment,
} from '../lib/api'

type NotesPageProps = {
  meetingId: string
  authToken: string
}

function sleep(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

/** 会后纪要页：转写三态 + 纪要三态（Aura）。 */
export function NotesPage({ meetingId, authToken }: NotesPageProps) {
  const [transcriptMode, setTranscriptMode] = useState<'generating' | 'no-audio' | 'ready'>('generating')
  const [segments, setSegments] = useState<TranscriptSegment[]>([])
  const [notesMode, setNotesMode] = useState<'generating' | 'no-transcript' | 'ready'>('generating')
  const [summary, setSummary] = useState<MeetingSummary | null>(null)
  const [err, setErr] = useState<{ error?: string; message?: string }>({})

  async function loadTranscript() {
    setTranscriptMode('generating')
    try {
      const existing = await getTranscript(meetingId, authToken)
      if (existing.length > 0) {
        setSegments(existing)
        setTranscriptMode('ready')
        return
      }
    } catch {
      // 空列表或尚未生成时继续尝试 generate。
    }

    try {
      const generated = await generateTranscript(meetingId, authToken)
      setSegments(generated.segments ?? [])
      setTranscriptMode('ready')
    } catch (error) {
      const parsed = parseApiError(error)
      if (parsed.error === 'no_audio') {
        setTranscriptMode('no-audio')
        return
      }
      if (parsed.error === 'ASR_NOT_CONFIGURED') {
        setTranscriptMode('no-audio')
        setErr({
          error: 'ASR_NOT_CONFIGURED',
          message: parsed.message ?? '私有 ASR 未配置；会议仍可使用，不会生成假转写。',
        })
        return
      }
      if (parsed.error === 'forbidden' || parsed.error === 'organizer_required') {
        setTranscriptMode('no-audio')
        setErr({
          error: parsed.error,
          message: parsed.message ?? '转写尚未生成，请联系组织者。',
        })
        return
      }
      setTranscriptMode('no-audio')
      setErr(parsed)
    }
  }

  useEffect(() => {
    let cancelled = false
    void (async () => {
      setErr({})
      await loadTranscript()
      if (cancelled) return

      setNotesMode('generating')
      try {
        try {
          const existing = await getSummary(meetingId, authToken)
          if (cancelled) return
          setSummary(existing)
          setNotesMode('ready')
          return
        } catch (error) {
          const parsed = parseApiError(error)
          if (parsed.error !== 'summary_not_ready') {
            if (cancelled) return
            setErr((prev) => ({ ...prev, ...parsed }))
            if (parsed.error === 'no_transcript') {
              setNotesMode('no-transcript')
            } else {
              setNotesMode('no-transcript')
            }
            return
          }
        }

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
              setNotesMode('ready')
              return
            } catch {
              // keep polling
            }
          }
          setErr((prev) => ({
            ...prev,
            error: prev.error ?? 'summary_not_ready',
            message: prev.message ?? '纪要仍在生成，请稍后刷新。',
          }))
          setNotesMode('generating')
          return
        }
        setSummary(generated as MeetingSummary)
        setNotesMode('ready')
      } catch (error) {
        if (cancelled) return
        const parsed = parseApiError(error)
        setErr((prev) => ({ ...prev, ...parsed }))
        if (parsed.error === 'no_transcript') {
          setNotesMode('no-transcript')
        } else if (parsed.error === 'AI_NOT_CONFIGURED') {
          setNotesMode('no-transcript')
          setErr((prev) => ({
            ...prev,
            error: 'AI_NOT_CONFIGURED',
            message: parsed.message ?? '私有 LLM 未配置；会议仍可使用，不会发明待办。',
          }))
        } else if (parsed.error === 'forbidden' || parsed.error === undefined) {
          setNotesMode('no-transcript')
          setErr((prev) => ({
            ...prev,
            error: prev.error ?? parsed.error,
            message: prev.message ?? parsed.message ?? '纪要尚未生成，请联系组织者。',
          }))
        } else {
          setNotesMode('no-transcript')
        }
      }
    })()
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- load once per meeting/token
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
      <div className="space-y-4">
        <section className="rounded-lg border border-border bg-surface p-4">
          <div className="mb-3 flex items-center justify-between gap-2">
            <h2 className="text-lg font-semibold tracking-tight">转写</h2>
            {transcriptMode === 'no-audio' ? (
              <Button variant="ghost" onClick={() => void loadTranscript()}>
                生成转写
              </Button>
            ) : null}
          </div>
          <TranscriptPanel
            mode={transcriptMode}
            segments={segments}
            onGenerate={() => void loadTranscript()}
          />
        </section>
        <section className="rounded-lg border border-border bg-surface p-4">
          <h2 className="mb-3 text-lg font-semibold tracking-tight">纪要</h2>
          <NotesPanel
            mode={notesMode}
            summary={summary?.summary}
            actionItems={summary?.action_items}
            onToggleDone={authToken ? (i) => void toggleDone(i) : undefined}
          />
        </section>
      </div>
    </AppShell>
  )
}
