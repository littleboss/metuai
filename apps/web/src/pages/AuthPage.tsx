import { AppShell } from '../aura/AppShell'

type AuthPageProps = {
  /** 占位：Nexus 发布 register/login 契约前不签发会话。 */
  onAuthenticated?: (token: string) => void
}

/**
 * Auth 占位页。产品将提供 register+login（登录签发 JWT）；
 * 契约由 Nexus 发布后再接。本页不做企业 JWT 粘贴，也不实现注册/登录。
 */
export function AuthPage(_props: AuthPageProps) {
  return (
    <AppShell title="METUAI">
      <div className="mx-auto flex w-full max-w-md flex-col gap-2">
        <h1 className="text-lg font-semibold tracking-tight">登录 / 注册</h1>
        <p className="text-sm text-secondary">
          身份入口待 Nexus 发布 register/login API 契约后接入。登录成功后将由服务端签发 JWT。
        </p>
        <p className="rounded-lg border border-border bg-surface px-3 py-2 text-sm text-secondary">
          AuthPage stub — 本 PR 不实现注册或登录。
        </p>
      </div>
    </AppShell>
  )
}
