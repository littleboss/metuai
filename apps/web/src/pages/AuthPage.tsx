import { type FormEvent, useState } from 'react'
import { Banner } from '../aura/Banner'
import { parseApiError } from '../aura/parseApiError'
import { Button } from '../aura/Button'
import { SecretField, TextField } from '../aura/TextField'
import { loginAccount, registerAccount } from '../lib/api'

type AuthPageProps = {
  onAuthenticated: (token: string) => void
}

type Mode = 'login' | 'register'

const MIN_PASSWORD = 8

/** Aura 登录/注册卡片：切换「登录 / 注册」；成功后携带 access_token 进入会议列表。 */
export function AuthPage({ onAuthenticated }: AuthPageProps) {
  const [mode, setMode] = useState<Mode>('login')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<{ error?: string; message?: string }>({})

  const passwordTooShort = password.length > 0 && password.length < MIN_PASSWORD
  const canSubmit =
    email.trim().length > 0 && password.length >= MIN_PASSWORD && !loading

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    if (!canSubmit) return
    setLoading(true)
    setErr({})
    try {
      const tokens =
        mode === 'register'
          ? await registerAccount(email.trim(), password, displayName.trim() || undefined)
          : await loginAccount(email.trim(), password)
      sessionStorage.setItem('employeeToken', tokens.access_token)
      onAuthenticated(tokens.access_token)
    } catch (error) {
      setErr(parseApiError(error))
    } finally {
      setLoading(false)
    }
  }

  function switchMode(next: Mode) {
    setMode(next)
    setErr({})
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-bg-app px-4 py-8 text-text">
      <div className="w-full max-w-md rounded-lg border border-border bg-surface p-6">
        <div className="mb-4 flex gap-2 rounded-lg bg-elevated p-1">
          <button
            type="button"
            className={[
              'flex-1 rounded-lg py-2 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent',
              mode === 'login' ? 'bg-accent text-white' : 'text-secondary hover:text-text',
            ].join(' ')}
            onClick={() => switchMode('login')}
          >
            登录
          </button>
          <button
            type="button"
            className={[
              'flex-1 rounded-lg py-2 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent',
              mode === 'register' ? 'bg-accent text-white' : 'text-secondary hover:text-text',
            ].join(' ')}
            onClick={() => switchMode('register')}
          >
            注册
          </button>
        </div>

        <h1 className="mb-1 text-lg font-semibold tracking-tight">
          {mode === 'login' ? '登录 METUAI' : '创建账号'}
        </h1>
        <p className="mb-4 text-sm text-secondary">邮箱与密码；服务端签发会话令牌。</p>

        <Banner error={err.error} message={err.message} />

        <form className="mt-4 flex flex-col gap-2" onSubmit={(e) => void handleSubmit(e)}>
          {mode === 'register' ? (
            <TextField
              label="显示名称"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              autoComplete="name"
              placeholder="会议中显示的名字"
            />
          ) : null}
          <TextField
            label="邮箱"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            autoComplete="email"
          />
          <SecretField
            label="密码"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
            hint={
              passwordTooShort
                ? '密码至少 8 个字符'
                : password.length === 0
                  ? '至少 8 个字符'
                  : undefined
            }
          />
          <Button type="submit" loading={loading} disabled={!canSubmit} className="w-full">
            {mode === 'login' ? '登录' : '注册'}
          </Button>
        </form>
      </div>
    </div>
  )
}
