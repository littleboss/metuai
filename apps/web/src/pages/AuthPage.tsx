import { type FormEvent, useState } from 'react'
import { AppShell } from '../aura/AppShell'
import { Banner } from '../aura/Banner'
import { parseApiError } from '../aura/parseApiError'
import { Button } from '../aura/Button'
import { SecretField, TextField } from '../aura/TextField'
import { loginAccount, registerAccount } from '../lib/api'

type AuthPageProps = {
  onAuthenticated: (token: string) => void
}

type Mode = 'login' | 'register'

/** Aura 登录/注册：邮箱+密码；成功后保存 access_token 并进入会议列表。无 JWT 粘贴。 */
export function AuthPage({ onAuthenticated }: AuthPageProps) {
  const [mode, setMode] = useState<Mode>('login')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [name, setName] = useState('')
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<{ error?: string; message?: string }>({})

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    setLoading(true)
    setErr({})
    try {
      const tokens =
        mode === 'register'
          ? await registerAccount(email.trim(), password, name.trim() || undefined)
          : await loginAccount(email.trim(), password)
      sessionStorage.setItem('employeeToken', tokens.access_token)
      onAuthenticated(tokens.access_token)
    } catch (error) {
      setErr(parseApiError(error))
    } finally {
      setLoading(false)
    }
  }

  return (
    <AppShell title="METUAI">
      <div className="mx-auto flex w-full max-w-md flex-col gap-2">
        <div className="space-y-2">
          <h1 className="text-lg font-semibold tracking-tight">
            {mode === 'login' ? '登录' : '注册'}
          </h1>
          <p className="text-sm text-secondary">
            使用邮箱与密码{mode === 'login' ? '登录' : '创建账号'}。登录成功后由服务端签发会话令牌。
          </p>
        </div>

        <Banner error={err.error} message={err.message} />

        <form className="flex flex-col gap-2" onSubmit={(e) => void handleSubmit(e)}>
          {mode === 'register' ? (
            <TextField
              label="显示名称"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoComplete="name"
              placeholder="可选"
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
            minLength={8}
            autoComplete={mode === 'login' ? 'current-password' : 'new-password'}
            hint="至少 8 个字符"
          />
          <Button type="submit" loading={loading} className="w-full">
            {mode === 'login' ? '登录' : '注册'}
          </Button>
        </form>

        <p className="text-sm text-secondary">
          {mode === 'login' ? (
            <>
              还没有账号？{' '}
              <button
                type="button"
                className="text-accent hover:text-accent-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent rounded-lg"
                onClick={() => {
                  setMode('register')
                  setErr({})
                }}
              >
                注册
              </button>
            </>
          ) : (
            <>
              已有账号？{' '}
              <button
                type="button"
                className="text-accent hover:text-accent-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent rounded-lg"
                onClick={() => {
                  setMode('login')
                  setErr({})
                }}
              >
                登录
              </button>
            </>
          )}
        </p>
      </div>
    </AppShell>
  )
}
