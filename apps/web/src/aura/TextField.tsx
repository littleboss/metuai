import type { InputHTMLAttributes } from 'react'

type FieldProps = InputHTMLAttributes<HTMLInputElement> & {
  label: string
  hint?: string
}

/** 普通文本输入。 */
export function TextField({ label, hint, id, className = '', ...rest }: FieldProps) {
  const fieldId = id ?? rest.name ?? label
  return (
    <label className="flex flex-col gap-2 text-sm" htmlFor={fieldId}>
      <span className="text-secondary">{label}</span>
      <input
        id={fieldId}
        className={[
          'rounded-lg border border-border bg-elevated px-3 py-2 text-text placeholder:text-secondary/70',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent',
          className,
        ].join(' ')}
        {...rest}
      />
      {hint ? <span className="text-xs text-secondary">{hint}</span> : null}
    </label>
  )
}

/** 密码/密钥输入（type=password）。 */
export function SecretField(props: FieldProps) {
  return <TextField {...props} type="password" autoComplete={props.autoComplete ?? 'off'} />
}
