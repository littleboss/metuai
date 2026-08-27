import { AppShell } from '../aura/AppShell'
import { Banner } from '../aura/Banner'
import { Button } from '../aura/Button'

export function Error401({ message }: { message?: string }) {
  return (
    <AppShell>
      <h1 className="text-lg font-semibold tracking-tight">未授权</h1>
      <Banner error="unauthorized" message={message ?? '请先登录后再试。'} />
      <Button onClick={() => window.location.assign('/')}>返回</Button>
    </AppShell>
  )
}

export function Error403({ message }: { message?: string }) {
  return (
    <AppShell>
      <h1 className="text-lg font-semibold tracking-tight">禁止访问</h1>
      <Banner error="forbidden" message={message ?? '你没有权限执行此操作。'} />
      <Button variant="ghost" onClick={() => window.location.assign('/')}>
        返回
      </Button>
    </AppShell>
  )
}

export function Error503({
  message,
  missing,
}: {
  message?: string
  missing?: string[]
}) {
  return (
    <AppShell>
      <h1 className="text-lg font-semibold tracking-tight">服务未就绪</h1>
      <Banner error="not_ready" message={message ?? '网关尚未就绪，请稍后重试。'} />
      {missing && missing.length > 0 ? (
        <ul className="flex flex-col gap-2 rounded-lg border border-border bg-surface p-3 text-sm text-secondary">
          {missing.map((item) => (
            <li key={item} className="font-mono">
              {item}
            </li>
          ))}
        </ul>
      ) : null}
      <Button variant="ghost" onClick={() => window.location.reload()}>
        刷新
      </Button>
    </AppShell>
  )
}
