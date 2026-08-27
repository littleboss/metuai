import type { ReactNode } from 'react'

export function AppShell({
  title = 'METUAI',
  children,
  actions,
}: {
  title?: string
  children: ReactNode
  actions?: ReactNode
}) {
  return (
    <div className="min-h-screen bg-bg-app text-text">
      <header className="flex items-center justify-between gap-4 border-b border-border px-4 py-3">
        <a href="/" className="text-lg font-semibold tracking-tight focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent rounded-lg">
          {title}
        </a>
        {actions}
      </header>
      <main className="mx-auto flex w-full max-w-5xl flex-col gap-6 px-4 py-6">{children}</main>
    </div>
  )
}
