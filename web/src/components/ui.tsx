import { useId, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactElement, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react';
import { cloneElement } from 'react';
import { Link } from 'react-router-dom';
import { ApiError } from '../lib/api/client';
import { fieldReason } from '../lib/api/errors';
import type { Money, Page } from '../lib/api/types';
import { formatMoney } from '../lib/format';
import type { StatusLabel, Tone } from '../lib/labels';
import { Icon, type IconName } from './Icon';

type Variant = 'primary' | 'dark' | 'outline' | 'ghost' | 'danger';
type Size = 'sm' | 'md' | 'lg';

export function btn(variant: Variant = 'primary', size: Size = 'md', extra = ''): string {
  return `btn btn-${variant}${size === 'md' ? '' : ` btn-${size}`}${extra ? ` ${extra}` : ''}`;
}

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
  icon?: IconName;
  block?: boolean;
}

export function Button({ variant = 'primary', size = 'md', loading, icon, block, children, disabled, type = 'button', className, ...rest }: ButtonProps) {
  return (
    <button type={type} className={btn(variant, size, `${block ? 'btn-block' : ''} ${className ?? ''}`.trim())}
      disabled={disabled || loading} aria-busy={loading || undefined} {...rest}>
      {loading ? <span className="spinner" aria-hidden="true" /> : icon && <Icon name={icon} size={18} />}
      {children}
    </button>
  );
}

export function Badge({ label, tone = 'neutral' }: { label: string; tone?: Tone }) {
  return <span className={`badge tone-${tone}`}><span className="badge-dot" aria-hidden="true" />{label}</span>;
}

export function StatusBadge({ status }: { status: StatusLabel }) {
  return <Badge label={status.label} tone={status.tone} />;
}

interface FieldProps {
  label: string;
  hint?: ReactNode;
  error?: string;
  required?: boolean;
  children: ReactElement<{ id?: string; 'aria-describedby'?: string; 'aria-invalid'?: boolean }>;
}

export function Field({ label, hint, error, required, children }: FieldProps) {
  const id = useId();
  const describedBy = error ? `${id}-err` : hint ? `${id}-hint` : undefined;
  return (
    <div className="field">
      <label className="field-label" htmlFor={id}>{label}{required && <span className="req" aria-hidden="true"> *</span>}</label>
      {cloneElement(children, { id, 'aria-describedby': describedBy, 'aria-invalid': error ? true : undefined })}
      {hint && !error && <p className="field-hint" id={`${id}-hint`}>{hint}</p>}
      {error && <p className="field-error" id={`${id}-err`}>{error}</p>}
    </div>
  );
}

export function Input(props: InputHTMLAttributes<HTMLInputElement>) {
  return <input className="input" {...props} />;
}

export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select className="input" {...props} />;
}

export function Textarea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea className="input" rows={3} {...props} />;
}

export function Checkbox({ label, ...rest }: InputHTMLAttributes<HTMLInputElement> & { label: ReactNode }) {
  return <label className="check"><input type="checkbox" {...rest} /><span>{label}</span></label>;
}

export function Card({ title, actions, children, flush }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; flush?: boolean }) {
  return (
    <section className="card">
      {(title || actions) && <header className="card-head"><h2 className="card-title">{title}</h2>{actions}</header>}
      <div className={flush ? '' : 'card-body'}>{children}</div>
    </section>
  );
}

export function PageHeader({ title, subtitle, actions, back }: { title: string; subtitle?: ReactNode; actions?: ReactNode; back?: { to: string; label: string } }) {
  return (
    <header className="page-header">
      <div>
        {back && <Link className="back-link" to={back.to}><Icon name="back" size={16} />{back.label}</Link>}
        <h1 className="page-title">{title}</h1>
        {subtitle && <p className="page-subtitle">{subtitle}</p>}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </header>
  );
}

export function Tabs<T extends string>({ value, onChange, options, label }: {
  value: T; onChange: (v: T) => void; options: { value: T; label: string; count?: number }[]; label: string;
}) {
  return (
    <div className="tabs" role="tablist" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" role="tab" className="tab" aria-selected={o.value === value} onClick={() => onChange(o.value)}>
          {o.label}{o.count !== undefined && <span className="tab-count">{o.count}</span>}
        </button>
      ))}
    </div>
  );
}

export function EmptyState({ icon = 'box', title, children }: { icon?: IconName; title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <span className="empty-icon"><Icon name={icon} size={26} /></span>
      <p className="empty-title">{title}</p>
      {children && <div className="empty-body">{children}</div>}
    </div>
  );
}

export function Notice({ tone = 'info', title, children }: { tone?: Tone; title?: string; children?: ReactNode }) {
  const icon: IconName = tone === 'danger' || tone === 'warning' ? 'alert' : tone === 'success' ? 'check' : 'help';
  return (
    <div className={`notice tone-${tone}`} role={tone === 'danger' ? 'alert' : undefined}>
      <Icon name={icon} size={18} />
      <div>{title && <p className="notice-title">{title}</p>}{children}</div>
    </div>
  );
}

/** Set `inlineFields` when the form shows field errors under its own inputs. */
export function FormError({ error, inlineFields }: { error: unknown; inlineFields?: boolean }) {
  if (!error) return null;
  const message = error instanceof Error ? error.message : 'Không thể thực hiện thao tác. Vui lòng thử lại.';
  const fields = error instanceof ApiError && !inlineFields ? error.fields : [];
  return (
    <Notice tone="danger" title={message}>
      {fields.length > 0 && <ul className="plain">{fields.map((f) => <li key={f.field}>{fieldReason(f.reason)}</li>)}</ul>}
      {error instanceof ApiError && error.requestId && <p className="small">Mã tra cứu: <span className="mono">{error.requestId}</span></p>}
    </Notice>
  );
}

/** The error text of one field, translated, for showing under its input. */
export function fieldError(error: unknown, field: string): string | undefined {
  if (!(error instanceof ApiError)) return undefined;
  const found = error.fields.find((f) => f.field === field);
  return found && fieldReason(found.reason);
}

export function MoneyText({ value }: { value: Money }) {
  return <span className="tnum">{formatMoney(value)}</span>;
}

export function Pagination<T>({ page, onChange }: { page: Page<T>; onChange: (p: number) => void }) {
  const pages = Math.max(1, Math.ceil(page.total / page.page_size));
  if (pages <= 1) return null;
  return (
    <nav className="pagination" aria-label="Phân trang">
      <button type="button" className={btn('outline', 'sm')} disabled={page.page <= 1} onClick={() => onChange(page.page - 1)}>
        <Icon name="chevronLeft" size={16} />Trước
      </button>
      <span className="small">Trang {page.page} / {pages}</span>
      <button type="button" className={btn('outline', 'sm')} disabled={page.page >= pages} onClick={() => onChange(page.page + 1)}>
        Sau<Icon name="chevronRight" size={16} />
      </button>
    </nav>
  );
}

export function Skeleton({ height = 120 }: { height?: number }) {
  return <div className="skeleton" style={{ height }} aria-hidden="true" />;
}

export function DefinitionList({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl className="deflist">
      {items.map((i) => <div key={i.label}><dt>{i.label}</dt><dd>{i.value}</dd></div>)}
    </dl>
  );
}
