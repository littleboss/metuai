import type { MediaArtifact } from '../lib/api'
import { EmptyState } from './EmptyState'

type ReplayPanelProps = {
  mode: 'loading' | 'empty' | 'ready'
  artifact?: MediaArtifact | null
}

/** 可播放 kind 优先级：房间合成视频 > 参会人轨 > 房间混音 > 本机麦。 */
const PLAYABLE_KIND_PRIORITY = [
  'room_video',
  'participant_track',
  'room_audio',
  'local_mic',
] as const

const VIDEO_KINDS = new Set(['room_video', 'participant_track'])

function LoadingSkeleton() {
  return (
    <div className="space-y-3" aria-busy aria-label="正在加载录像">
      <div className="aspect-video w-full animate-pulse rounded-lg bg-elevated" />
      <div className="h-4 w-1/3 animate-pulse rounded-lg bg-elevated" />
    </div>
  )
}

/** 从 media 列表里选第一个 ready 且带 download_url 的可播放产物。 */
export function pickPlayableArtifact(artifacts: MediaArtifact[]): MediaArtifact | null {
  for (const kind of PLAYABLE_KIND_PRIORITY) {
    const found = artifacts.find(
      (a) => a.kind === kind && a.status === 'ready' && Boolean(a.download_url),
    )
    if (found) return found
  }
  return null
}

/** 录像回放三态：LoadingSkeleton | EmptyState | video/audio 播放器。 */
export function ReplayPanel({ mode, artifact }: ReplayPanelProps) {
  if (mode === 'loading') {
    return <LoadingSkeleton />
  }
  if (mode === 'empty' || !artifact?.download_url) {
    return <EmptyState title="暂无录像" description="录制尚未就绪或本场未产生可播放的录像。" />
  }

  const useVideo = VIDEO_KINDS.has(artifact.kind)
  if (useVideo) {
    return (
      <video
        controls
        className="aspect-video w-full rounded-lg border border-border bg-[#0A0D12]"
        src={artifact.download_url}
        data-kind={artifact.kind}
      />
    )
  }
  return (
    <audio
      controls
      className="w-full"
      src={artifact.download_url}
      data-kind={artifact.kind}
    />
  )
}
