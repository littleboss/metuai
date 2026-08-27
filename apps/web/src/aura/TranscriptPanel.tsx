import type { TranscriptSegment } from '../lib/api'
import { EmptyState } from './EmptyState'

type TranscriptPanelProps = {
  mode: 'generating' | 'no-audio' | 'ready'
  segments?: TranscriptSegment[]
}

function GeneratingSkeleton() {
  return (
    <div className="space-y-3" aria-busy aria-label="正在生成转写">
      <div className="h-4 w-1/2 animate-pulse rounded-lg bg-elevated" />
      <div className="h-4 w-full animate-pulse rounded-lg bg-elevated" />
      <div className="h-4 w-4/5 animate-pulse rounded-lg bg-elevated" />
      <div className="mt-2 h-16 animate-pulse rounded-lg bg-elevated" />
    </div>
  )
}

/** 把 start_ms 显示为说话时间戳（t_ms → mm:ss）。 */
function formatTMs(tMs: number) {
  const totalSec = Math.max(0, Math.floor(tMs / 1000))
  const min = Math.floor(totalSec / 60)
  const sec = totalSec % 60
  return `${min}:${sec.toString().padStart(2, '0')}`
}

function SegmentList({ segments }: { segments: TranscriptSegment[] }) {
  if (segments.length === 0) {
    return <p className="text-sm text-secondary">暂无转写片段</p>
  }
  return (
    <ul className="space-y-3">
      {segments.map((seg) => (
        <li
          key={seg.id || `${seg.start_ms}-${seg.speaker_display_name}`}
          className="rounded-lg border border-border bg-elevated px-3 py-2 text-sm"
        >
          <div className="mb-1 flex flex-wrap items-baseline gap-x-2 gap-y-0.5 text-xs text-secondary">
            <span className="font-medium text-text">{seg.speaker_display_name || '说话人'}</span>
            <span className="font-mono" data-t-ms={seg.start_ms}>
              {formatTMs(seg.start_ms)}
            </span>
          </div>
          <p className="text-text whitespace-pre-wrap">{seg.text}</p>
        </li>
      ))}
    </ul>
  )
}

/** 转写面板三态：GeneratingSkeleton | NoAudioEmpty | SegmentList。 */
export function TranscriptPanel({ mode, segments = [] }: TranscriptPanelProps) {
  if (mode === 'generating') {
    return <GeneratingSkeleton />
  }
  if (mode === 'no-audio') {
    return (
      <EmptyState
        title="暂无可用音频"
        description="没有 participant_track 或 local_mic 时无法生成转写；房间混音不算权威音源。"
      />
    )
  }
  return <SegmentList segments={segments} />
}
