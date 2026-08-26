/**
 * 客户端类型：员工桌面端应声明 tauri，便于网关在
 * DEV_ALLOW_EMPLOYEE_WEB=false 时放行入会。
 */

/** 是否在 Tauri WebView 里跑（有本地录音能力）。 */
export function isTauriRuntime(): boolean {
  return (
    typeof window !== 'undefined' &&
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    Boolean((window as any).__TAURI_INTERNALS__ || (window as any).__TAURI__)
  )
}

export function metuaiClientHeaders(): Record<string, string> {
  if (!isTauriRuntime()) {
    return {}
  }
  return { 'X-Metuai-Client': 'tauri' }
}
