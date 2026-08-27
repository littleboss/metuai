type BannerProps = {
  error?: string
  message?: string
}

/** 仅展示 {error,message}，避免把原始 HTML/堆栈塞进 UI。 */
export function Banner({ error, message }: BannerProps) {
  if (!error && !message) return null
  return (
    <div
      role="alert"
      className="rounded-lg border border-danger/40 bg-danger/10 px-3 py-2 text-sm text-danger"
    >
      {error ? <p className="font-medium">{error}</p> : null}
      {message ? <p className="text-danger/90">{message}</p> : null}
    </div>
  )
}
