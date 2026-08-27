/** 从 fetch 错误正文里尽量解析网关 {error,message}。 */
export function parseApiError(raw: unknown): { error?: string; message?: string } {
  if (!(raw instanceof Error)) {
    return { message: String(raw ?? '请求失败') }
  }
  const text = raw.message
  try {
    const parsed = JSON.parse(text) as { error?: string; message?: string }
    if (parsed && (parsed.error || parsed.message)) {
      return { error: parsed.error, message: parsed.message ?? parsed.error }
    }
  } catch {
    /* 非 JSON */
  }
  if (text.includes('401') || /unauthorized/i.test(text)) {
    return { error: 'unauthorized', message: '未授权，请粘贴有效的员工 JWT' }
  }
  return { message: text || '请求失败' }
}
