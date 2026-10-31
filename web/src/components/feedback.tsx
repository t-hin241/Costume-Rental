import { useMutation, useQueryClient, type QueryKey, type UseQueryResult } from '@tanstack/react-query';
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { ApiError } from '../lib/api/client';
import { Icon } from './Icon';
import { Button, EmptyState, Field, FormError, Skeleton, Textarea } from './ui';

// ---- Toasts: every action reports success or failure ----

type ToastKind = 'success' | 'error';
interface ToastItem { id: number; kind: ToastKind; message: string }

interface ToastValue {
  success: (message: string) => void;
  error: (message: string) => void;
}

const ToastContext = createContext<ToastValue | null>(null);
const toastLifetimeMs = 5000;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const nextId = useRef(1);

  const remove = useCallback((id: number) => setItems((all) => all.filter((t) => t.id !== id)), []);
  const push = useCallback((kind: ToastKind, message: string) => {
    const id = nextId.current++;
    setItems((all) => [...all, { id, kind, message }]);
    window.setTimeout(() => remove(id), toastLifetimeMs);
  }, [remove]);

  const value = useMemo<ToastValue>(() => ({
    success: (m) => push('success', m),
    error: (m) => push('error', m),
  }), [push]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className={`toast toast-${t.kind}`}>
            <Icon name={t.kind === 'success' ? 'check' : 'alert'} size={18} />
            <span>{t.message}</span>
            <button type="button" className="toast-close" aria-label="Đóng thông báo" onClick={() => remove(t.id)}><Icon name="close" size={16} /></button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): ToastValue {
  const value = useContext(ToastContext);
  if (!value) throw new Error('useToast must be used inside ToastProvider');
  return value;
}

// ---- Actions ----

interface ActionOptions<R> {
  /** Toast on success. */
  success?: string | ((result: R) => string);
  invalidate?: QueryKey[];
  onSuccess?: (result: R) => void;
  /** The form shows the error inline, so no toast is needed. */
  inline?: boolean;
}

/** A mutation that refreshes what it changed and tells the user how it went. */
export function useAction<V, R>(fn: (vars: V) => Promise<R>, options: ActionOptions<R> = {}) {
  const toast = useToast();
  const client = useQueryClient();
  return useMutation<R, Error, V>({
    mutationFn: fn,
    onSuccess: async (result) => {
      await Promise.all((options.invalidate ?? []).map((queryKey) => client.invalidateQueries({ queryKey })));
      const { success } = options;
      if (success) toast.success(typeof success === 'function' ? success(result) : success);
      options.onSuccess?.(result);
    },
    onError: (err) => {
      if (!options.inline) toast.error(err.message);
    },
  });
}

// ---- Query states ----

export function NotConnected({ what }: { what?: string }) {
  return (
    <EmptyState icon="settings" title={what ? `${what} chưa được kết nối API` : 'Chức năng chưa được kết nối API'}>
      <p>Giao diện đã sẵn sàng. Dữ liệu sẽ hiển thị khi backend cung cấp endpoint tương ứng.</p>
    </EmptyState>
  );
}

export function QueryError({ error, retry }: { error: unknown; retry?: () => void }) {
  if (error instanceof ApiError) {
    if (error.endpointMissing) return <NotConnected />;
    if (error.status === 403) {
      return <EmptyState icon="shield" title="Bạn không có quyền xem nội dung này" />;
    }
    if (error.code === 'storage.not_found') return <EmptyState icon="box" title="Không tìm thấy dữ liệu" />;
  }
  return (
    <EmptyState icon="alert" title={error instanceof Error ? error.message : 'Không tải được dữ liệu'}>
      {error instanceof ApiError && error.requestId && <p className="small">Mã tra cứu: <span className="mono">{error.requestId}</span></p>}
      {retry && <Button variant="outline" size="sm" onClick={retry}>Thử lại</Button>}
    </EmptyState>
  );
}

export function QueryState<T>({ query, children, skeleton }: {
  query: UseQueryResult<T>; children: (data: T) => ReactNode; skeleton?: ReactNode;
}) {
  if (query.isPending) return <>{skeleton ?? <Skeleton />}</>;
  if (query.isError) return <QueryError error={query.error} retry={() => void query.refetch()} />;
  return <>{children(query.data)}</>;
}

// ---- Dialogs ----

export function Modal({ open, title, onClose, children, footer, wide }: {
  open: boolean; title: string; onClose: () => void; children: ReactNode; footer?: ReactNode; wide?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);
  return (
    <dialog ref={ref} className={`modal${wide ? ' modal-wide' : ''}`} onClose={onClose} aria-label={title}>
      {open && (
        <div className="modal-inner">
          <header className="modal-head">
            <h2>{title}</h2>
            <button type="button" className="icon-button" aria-label="Đóng" onClick={onClose}><Icon name="close" /></button>
          </header>
          <div className="modal-body">{children}</div>
          {footer && <footer className="modal-foot">{footer}</footer>}
        </div>
      )}
    </dialog>
  );
}

/** Confirms a sensitive action; the reason goes to the audit log. */
export function ReasonDialog({ open, title, description, confirmLabel, danger, onClose, onConfirm, pending, error }: {
  open: boolean; title: string; description?: ReactNode; confirmLabel: string; danger?: boolean;
  onClose: () => void; onConfirm: (reason: string) => void; pending: boolean; error?: unknown;
}) {
  const [reason, setReason] = useState('');
  const submit = (e: FormEvent) => {
    e.preventDefault();
    onConfirm(reason.trim());
  };
  return (
    <Modal open={open} title={title} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        {description}
        <FormError error={error} />
        <Field label="Lý do" required hint="Lý do được lưu vào lịch sử thao tác.">
          <Textarea value={reason} maxLength={500} required onChange={(e) => setReason(e.target.value)} />
        </Field>
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" variant={danger ? 'danger' : 'primary'} loading={pending} disabled={!reason.trim()}>{confirmLabel}</Button>
        </div>
      </form>
    </Modal>
  );
}
