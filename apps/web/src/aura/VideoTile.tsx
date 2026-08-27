import type { ReactNode } from 'react'

type VideoTileState = 'streaming' | 'muted' | 'cam-off' | 'permission-denied' | 'speaking'

type VideoTileProps = {
  name: string
  state?: VideoTileState
  children?: ReactNode
}

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[1][0]).toUpperCase()
}

/** 参会画面砖：支持 streaming / muted / cam-off / permission-denied / speaking。 */
export function VideoTile({ name, state = 'streaming', children }: VideoTileProps) {
  const speaking = state === 'speaking'
  const showAvatar = state === 'cam-off' || state === 'permission-denied' || !children

  return (
    <div
      className={[
        'relative aspect-video overflow-hidden rounded-lg border border-border bg-elevated',
        speaking ? 'ring-2 ring-accent' : '',
      ].join(' ')}
    >
      {showAvatar ? (
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-2">
          <span className="flex size-14 items-center justify-center rounded-lg bg-surface text-lg font-semibold tracking-tight">
            {initials(name)}
          </span>
          <div className="flex flex-wrap justify-center gap-2 px-2">
            {state === 'cam-off' || state === 'permission-denied' ? (
              <span className="rounded-lg bg-surface px-2 py-0.5 text-xs text-secondary">无画面</span>
            ) : null}
            {state === 'muted' || state === 'permission-denied' ? (
              <span className="rounded-lg bg-surface px-2 py-0.5 text-xs text-secondary">无声</span>
            ) : null}
          </div>
        </div>
      ) : (
        children
      )}
      <div className="absolute inset-x-0 bottom-0 flex items-center justify-between gap-2 bg-bg-app/80 px-2 py-1.5">
        <span className="truncate text-xs text-text">{name}</span>
        {state === 'muted' ? <span className="text-xs text-secondary">静音</span> : null}
      </div>
    </div>
  )
}
