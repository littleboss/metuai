import type { ReactNode } from 'react'

type EmptyStateProps = {
  title: string
  description?: string
  action?: ReactNode
}

/** 列表空态 / 无转写空态。 */
export function EmptyState({ title, description, action }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-start gap-3 rounded-lg border border-dashed border-border bg-surface p-6">
      <h2 className="text-lg font-semibold tracking-tight text-text">{title}</h2>
      {description ? <p className="text-sm text-secondary max-w-md">{description}</p> : null}
      {action}
    </div>
  )
}
