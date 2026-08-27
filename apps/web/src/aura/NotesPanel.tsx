import type { ActionItem } from '../lib/api'
import { Button } from './Button'
import { EmptyState } from './EmptyState'

type NotesPanelProps = {
  mode: 'generating' | 'no-transcript' | 'ready'
  summary?: string
  actionItems?: ActionItem[]
  onToggleDone?: (index: number) => void
  /** 组织者触发生成纪要；无转写时应禁用。 */
  onGenerate?: () => void
  generateDisabled?: boolean
  showGenerate?: boolean
}

function GeneratingSkeleton() {
  return (
    <div className="space-y-3" aria-busy aria-label="正在生成纪要">
      <div className="h-4 w-2/3 animate-pulse rounded-lg bg-elevated" />
      <div className="h-4 w-full animate-pulse rounded-lg bg-elevated" />
      <div className="h-4 w-5/6 animate-pulse rounded-lg bg-elevated" />
      <div className="mt-4 h-20 animate-pulse rounded-lg bg-elevated" />
    </div>
  )
}

function TodoList({
  items,
  onToggleDone,
}: {
  items: ActionItem[]
  onToggleDone?: (index: number) => void
}) {
  if (items.length === 0) {
    return <p className="text-sm text-secondary">暂无待办</p>
  }
  return (
    <ul className="space-y-2">
      {items.map((item, index) => {
        const done = Boolean(item.completed_at)
        return (
          <li
            key={`${item.task}-${index}`}
            className="flex items-start gap-2 rounded-lg border border-border bg-elevated px-3 py-2 text-sm"
          >
            <button
              type="button"
              className="mt-0.5 size-4 shrink-0 rounded border border-border focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
              aria-label={done ? '标记为未完成' : '标记为完成'}
              onClick={() => onToggleDone?.(index)}
              disabled={!onToggleDone || done}
            >
              <span className={done ? 'block size-full bg-success' : ''} />
            </button>
            <span className={done ? 'text-secondary line-through' : 'text-text'}>{item.task}</span>
            <span className="ml-auto text-xs text-secondary">{done ? 'done' : 'open'}</span>
          </li>
        )
      })}
    </ul>
  )
}

function GenerateNotesButton({
  onGenerate,
  disabled,
}: {
  onGenerate?: () => void
  disabled?: boolean
}) {
  if (!onGenerate) return null
  return (
    <Button
      variant="ghost"
      onClick={onGenerate}
      disabled={disabled}
      data-testid="generate-notes"
    >
      生成纪要
    </Button>
  )
}

/** 纪要面板：GeneratingSkeleton | NoTranscriptEmpty | Summary+TodoList。 */
export function NotesPanel({
  mode,
  summary,
  actionItems = [],
  onToggleDone,
  onGenerate,
  generateDisabled,
  showGenerate,
}: NotesPanelProps) {
  if (mode === 'generating') {
    return <GeneratingSkeleton />
  }
  if (mode === 'no-transcript') {
    return (
      <EmptyState
        title="暂无转写"
        description="没有转写时不会生成纪要，也不会发明人名或待办。"
        action={
          showGenerate ? (
            <GenerateNotesButton onGenerate={onGenerate} disabled={generateDisabled ?? true} />
          ) : null
        }
      />
    )
  }
  return (
    <div className="space-y-6">
      <section className="space-y-2">
        <h2 className="text-lg font-semibold tracking-tight">摘要</h2>
        <p className="text-sm text-text whitespace-pre-wrap">{summary}</p>
      </section>
      <section className="space-y-2">
        <h2 className="text-lg font-semibold tracking-tight">待办</h2>
        <TodoList items={actionItems} onToggleDone={onToggleDone} />
      </section>
    </div>
  )
}
