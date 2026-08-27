import { useState } from 'react'
import { Button } from './Button'

type JoinCodeBlockProps = {
  code: string
  label?: string
}

/** 入会码展示：等宽字距，复制成功提示约 2 秒。 */
export function JoinCodeBlock({ code, label = '会议密码' }: JoinCodeBlockProps) {
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(code)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 2000)
    } catch {
      setCopied(false)
    }
  }

  return (
    <div className="rounded-lg border border-border bg-elevated p-3">
      <p className="text-xs text-secondary mb-2">{label}</p>
      <div className="flex items-center gap-2">
        <code className="flex-1 font-mono text-sm tracking-[0.2em] text-text">{code}</code>
        <Button variant="ghost" onClick={() => void copy()} aria-label="复制入会码">
          {copied ? '已复制' : '复制'}
        </Button>
      </div>
    </div>
  )
}
