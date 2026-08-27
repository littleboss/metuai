import { type FormEvent, useState } from 'react'
import { AppShell } from '../aura/AppShell'
import { Banner } from '../aura/Banner'
import { parseApiError } from '../aura/parseApiError'
import { Button } from '../aura/Button'
import { SecretField } from '../aura/TextField'
import { listEmployeeMeetings } from '../lib/api'

type AuthPageProps = {
  onAuthenticated: (token: string) => void
}

/** 仅粘贴企业 JWT +「进入」，无邮箱/密码本地账号。 */
export function AuthPage({ onAuthenticated }: AuthPageProps) {
  const [token, setToken] = useState(() => sessionStorage.getItem('employeeToken') ?? '')
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<{ error?: string; message?: string }>({})

  async function handleSubmit(event: FormEvent) {
    event.preventDefault()
    const value = token.trim()
    if (!value) {
      setErr({ error: 'unauthorized', message: '请粘贴员工 JWT' })
      return
    }
    setLoading(true)
    setErr({})
    try {
      // 用会议列表探测 JWT 是否被网关接受（401 → Banner）。
      await listEmployeeMeetings(value)
      sessionStorage.setItem('employeeToken', value)
      onAuthenticated(value)
    } catch (error) {
      const parsed = parseApiError(error)
      if (!parsed.error) parsed.error = 'unauthorized'
      setErr(parsed)
    } finally {
      setLoading(false)
    }
  }

  return (
    <AppShell title="METUAI">
      <div className="mx-auto w-full max-w-md space-y-6">
        <div className="space-y-2">
          <h1 className="text-lg font-semibold tracking-tight">员工入口</h1>
          <p className="text-sm text-secondary">
            粘贴企业签发的员工 JWT（PoC：`go run ./cmd/devtoken`）。本系统不提供注册/登录。
          </p>
        </div>
        <Banner error={err.error} message={err.message} />
        <form className="space-y-4" onSubmit={(e) => void handleSubmit(e)}>
          <SecretField
            label="员工 JWT"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder="eyJ..."
            autoComplete="off"
          />
          <Button type="submit" loading={loading} className="w-full">
            进入
          </Button>
        </form>
      </div>
    </AppShell>
  )
}
