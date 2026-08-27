import type { ReactNode } from 'react'
import { Mic, MicOff, MonitorUp, PhoneOff, Video, VideoOff } from 'lucide-react'

type ControlBarProps = {
  micOn: boolean
  camOn: boolean
  onToggleMic: () => void
  onToggleCam: () => void
  onShare?: () => void
  onLeave: () => void
  endMeeting?: ReactNode
}

/** 底部居中控制条：Mic / Cam / Share / Leave；EndMeeting 由调用方按 is_organizer 注入。 */
export function ControlBar({
  micOn,
  camOn,
  onToggleMic,
  onToggleCam,
  onShare,
  onLeave,
  endMeeting,
}: ControlBarProps) {
  const iconBtn =
    'inline-flex size-10 items-center justify-center rounded-lg border border-border bg-elevated text-text hover:bg-surface focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent'

  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-6 z-20 flex justify-center px-4">
      <div className="pointer-events-auto flex items-center gap-2 rounded-lg border border-border bg-surface/95 px-3 py-2 shadow-lg backdrop-blur-sm">
        <button type="button" className={iconBtn} aria-label={micOn ? '关闭麦克风' : '打开麦克风'} onClick={onToggleMic}>
          {micOn ? <Mic className="size-4" /> : <MicOff className="size-4" />}
        </button>
        <button type="button" className={iconBtn} aria-label={camOn ? '关闭摄像头' : '打开摄像头'} onClick={onToggleCam}>
          {camOn ? <Video className="size-4" /> : <VideoOff className="size-4" />}
        </button>
        <button type="button" className={iconBtn} aria-label="屏幕共享" onClick={onShare} disabled={!onShare}>
          <MonitorUp className="size-4" />
        </button>
        <button
          type="button"
          className="inline-flex size-10 items-center justify-center rounded-lg bg-danger/20 text-danger hover:bg-danger/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          aria-label="离开会议"
          onClick={onLeave}
        >
          <PhoneOff className="size-4" />
        </button>
        {endMeeting}
      </div>
    </div>
  )
}
