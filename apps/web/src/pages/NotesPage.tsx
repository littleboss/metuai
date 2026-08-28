import { useEffect, useState } from 'react'
import { AppShell } from '../aura/AppShell'
import { Banner } from '../aura/Banner'
import { parseApiError } from '../aura/parseApiError'
import { Button } from '../aura/Button'
import { NotesPanel } from '../aura/NotesPanel'
import { pickPlayableArtifact, ReplayPanel } from '../aura/ReplayPanel'
import { TranscriptPanel } from '../aura/TranscriptPanel'
import {
  completeActionItem,
  generateSummary,
  generateTranscript,
  getMedia,
  getMeeting,
  getSummary,
  getTranscript,
  type ActionItem,
  type MediaArtifact,
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

function parseSub(token: string): string {
  try {
    const payload = JSON.parse(atob(token.split('.')[1] ?? '')) as { sub?: string }
    return payload.sub ?? ''
  } catch {
    return ''
  }
}

/** 会后纪要页：转写三态叠在上方；纪要不自动跟转写联调 LLM。 */
export function NotesPage({ meetingId, authToken }: NotesPageProps) {
  const [isOrganizer, setIsOrganizer] = useState(
    () => sessionStorage.getItem('isOrganizer') === '1',
  )
  const [transcriptMode, setTranscriptMode] = useState<'generating' | 'no-audio' | 'ready'>('ready')
  const [segments, setSegments] = useState<TranscriptSegment[]>([])
  const [notesMode, setNotesMode] = useState<'generating' | 'no-transcript' | 'ready'>('no-transcript')
  const [summary, setSummary] = useState<MeetingSummary | null>(null)
  const [err, setErr] = useState<{ error?: string; message?: string }>({})
  const [hasTranscript, setHasTranscript] = useState(false)
  const [recordingMode, setRecordingMode] = useState<'loading' | 'empty' | 'ready'>('loading')
  const [playableArtifact, setPlayableArtifact] = useState<MediaArtifact | null>(null)

  useEffect(() => {
    let cancelled = false
    void (async () => {
      setErr({})
      setRecordingMode('loading')
      setPlayableArtifact(null)
      try {
        const info = await getMeeting(meetingId, authToken)
        if (cancelled) return
        const fromSession = sessionStorage.getItem('isOrganizer') === '1'
        const fromMeeting = info.organizer_id === parseSub(authToken)
        setIsOrganizer(fromSession || fromMeeting)
      } catch {
        // 保留 sessionStorage 判定。
      }

      try {
        const existing = await getTranscript(meetingId, authToken)
        if (cancelled) return
        if (existing.length > 0) {
          setSegments(existing)
          setTranscriptMode('ready')
          setHasTranscript(true)
        } else {
          setSegments([])
          setTranscriptMode('ready')
          setHasTranscript(false)
        }
      } catch (error) {
        if (cancelled) return
        const parsed = parseApiError(error)
        setErr(parsed)
        setSegments([])
        setTranscriptMode('ready')
        setHasTranscript(false)
      }

      try {
        const existingSummary = await getSummary(meetingId, authToken)
        if (cancelled) return
        setSummary(existingSummary)
        setNotesMode('ready')
      } catch (error) {
        if (cancelled) return
        const parsed = parseApiError(error)
        if (parsed.error && parsed.error !== 'summary_not_ready' && parsed.error !== 'no_transcript') {
          setErr((prev) => ({ ...prev, ...parsed }))
        }
        setSummary(null)
        setNotesMode('no-transcript')
      }

      try {
        const artifacts = await getMedia(meetingId, authToken)
        if (cancelled) return
        const playable = pickPlayableArtifact(artifacts)
        setPlayableArtifact(playable)
        setRecordingMode(playable ? 'ready' : 'empty')
      } catch (error) {
        if (cancelled) return
        const parsed = parseApiError(error)
        setErr((prev) => ({ ...prev, ...parsed }))
        setPlayableArtifact(null)
        setRecordingMode('empty')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [meetingId, authToken])

  async function handleGenerateTranscript() {
    setErr({})
    setTranscriptMode('generating')
    try {
      const generated = await generateTranscript(meetingId, authToken)
      const next = generated.segments ?? []
      setSegments(next)
      setHasTranscript(next.length > 0)
      setTranscriptMode('ready')
      // 有转写后纪要仍保持空态，等组织者手动点「生成纪要」（不自动调 LLM）。
      if (next.length > 0 && notesMode !== 'ready') {
        setNotesMode('no-transcript')
      }
    } catch (error) {
      const parsed = parseApiError(error)
      if (parsed.error === 'no_audio') {
        setSegments([])
        setHasTranscript(false)
        setTranscriptMode('no-audio')
        return
      }
      if (
        parsed.error === 'unauthorized' ||
        parsed.error === 'organizer_required' ||
        parsed.error === 'forbidden' ||
        parsed.error === 'meeting_not_ended' ||
        parsed.error === 'ASR_NOT_CONFIGURED'
      ) {
        setErr({
          error: parsed.error === 'forbidden' ? 'organizer_required' : parsed.error,
          message: parsed.message,
        })
        setTranscriptMode('ready')
        return
      }
      setErr(parsed)
      setTranscriptMode('ready')
    }
  }

  async function handleGenerateNotes() {
    if (!hasTranscript) return
    setErr({})
    setNotesMode('generating')
    try {
      const generated = await generateSummary(meetingId, authToken)
      if ('accepted' in generated && generated.accepted) {
        const deadline = Date.now() + 60_000
        while (Date.now() < deadline) {
          await sleep(500)
          try {
            const sum = await getSummary(meetingId, authToken)
            setSummary(sum)
            setNotesMode('ready')
            return
          } catch {
            // keep polling
          }
        }
        setErr({
          error: 'summary_not_ready',
          message: '纪要仍在生成，请稍后刷新。',
        })
        setNotesMode('no-transcript')
        return
      }
      setSummary(generated as MeetingSummary)
      setNotesMode('ready')
    } catch (error) {
      const parsed = parseApiError(error)
      setErr(parsed)
      if (parsed.error === 'no_transcript') {
        setNotesMode('no-transcript')
      } else if (parsed.error === 'AI_NOT_CONFIGURED') {
        setNotesMode('no-transcript')
        setErr({
          error: 'AI_NOT_CONFIGURED',
          message: parsed.message ?? '私有 LLM 未配置；会议仍可使用，不会发明待办。',
        })
      } else {
        setNotesMode('no-transcript')
      }
    }
  }

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

  const showGenerateTranscript =
    isOrganizer && (transcriptMode === 'ready' || transcriptMode === 'no-audio') && !hasTranscript

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
            {showGenerateTranscript ? (
              <Button
                variant="ghost"
                onClick={() => void handleGenerateTranscript()}
                data-testid="generate-transcript"
              >
                生成转写
              </Button>
            ) : null}
          </div>
          <TranscriptPanel mode={transcriptMode} segments={segments} />
        </section>
        <section className="rounded-lg border border-border bg-surface p-4">
          <div className="mb-3 flex items-center justify-between gap-2">
            <h2 className="text-lg font-semibold tracking-tight">纪要</h2>
          </div>
          <NotesPanel
            mode={notesMode}
            summary={summary?.summary}
            actionItems={summary?.action_items}
            onToggleDone={authToken ? (i) => void toggleDone(i) : undefined}
            showGenerate={isOrganizer && notesMode === 'no-transcript'}
            onGenerate={() => void handleGenerateNotes()}
            generateDisabled={!hasTranscript}
          />
        </section>
        <section className="rounded-lg border border-border bg-surface p-4">
          <div className="mb-3 flex items-center justify-between gap-2">
            <h2 className="text-lg font-semibold tracking-tight">录像</h2>
          </div>
          <ReplayPanel mode={recordingMode} artifact={playableArtifact} />
        </section>
      </div>
    </AppShell>
  )
}
