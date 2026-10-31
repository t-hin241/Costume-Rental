import { useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { Modal, useAction } from '../../components/feedback';
import { Button, Card, Field, FormError, Input, Notice, PageHeader, StatusBadge, fieldError } from '../../components/ui';
import { authApi } from '../../lib/api/endpoints';
import type { MFAEnrollment } from '../../lib/api/types';
import { useAuth } from '../../lib/auth';
import { RecoveryCodes, TotpQR } from '../auth/mfa';

export default function Security() {
  return (
    <>
      <PageHeader title="Bảo mật" subtitle="Quản lý mật khẩu và xác thực hai bước." />
      <div className="stack">
        <ChangePassword />
        <TwoFactor />
      </div>
    </>
  );
}

function ChangePassword() {
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const change = useAction(() => authApi.changePassword(current, next), {
    inline: true,
    success: 'Đã đổi mật khẩu. Các phiên đăng nhập khác đã bị đăng xuất.',
    onSuccess: () => { setCurrent(''); setNext(''); },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    change.mutate(undefined);
  };
  return (
    <Card title="Đổi mật khẩu">
      <form className="stack" style={{ maxWidth: 460 }} onSubmit={submit}>
        <FormError error={change.error} inlineFields />
        <Field label="Mật khẩu hiện tại" required error={fieldError(change.error, 'current_password')}>
          <Input type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />
        </Field>
        <Field label="Mật khẩu mới" required error={fieldError(change.error, 'new_password') ?? fieldError(change.error, 'password')}>
          <Input type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} required />
        </Field>
        <div><Button type="submit" loading={change.isPending}>Đổi mật khẩu</Button></div>
      </form>
    </Card>
  );
}

function TwoFactor() {
  const { session } = useAuth();
  const client = useQueryClient();
  const [enrollment, setEnrollment] = useState<MFAEnrollment | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [code, setCode] = useState('');
  const [disabling, setDisabling] = useState(false);

  const refreshSession = () => client.invalidateQueries({ queryKey: ['session'] });

  const start = useAction(authApi.mfaSetup, { onSuccess: setEnrollment });
  const confirm = useAction(() => authApi.mfaConfirm(code.trim()), {
    inline: true,
    onSuccess: (r) => {
      setEnrollment(null);
      setCode('');
      setCodes(r.recovery_codes);
      void refreshSession();
    },
  });
  const disable = useAction(() => authApi.mfaDisable(code.trim()), {
    inline: true,
    success: 'Đã tắt xác thực hai bước.',
    onSuccess: () => {
      setDisabling(false);
      setCode('');
      void refreshSession();
    },
  });

  const enabled = Boolean(session?.mfa);
  const required = Boolean(session?.roles.some((r) => r === 'admin' || r === 'finance'));

  return (
    <Card title="Xác thực hai bước"
      actions={<StatusBadge status={enabled ? { label: 'Đang bật', tone: 'success' } : { label: 'Chưa bật', tone: 'neutral' }} />}>
      <div className="stack">
        <p className="small muted">Dùng ứng dụng xác thực để tạo mã khi đăng nhập.</p>
        {required && <Notice tone="info">Vai trò của bạn bắt buộc bật xác thực hai bước, nên không thể tắt.</Notice>}
        <div>
          {!enabled && <Button variant="dark" loading={start.isPending} onClick={() => start.mutate(undefined)}>Bật xác thực hai bước</Button>}
          {enabled && !required && <Button variant="outline" onClick={() => setDisabling(true)}>Tắt xác thực hai bước</Button>}
        </div>
      </div>

      <Modal open={enrollment !== null} title="Bật xác thực hai bước" onClose={() => setEnrollment(null)}>
        {enrollment && (
          <div className="stack">
            <TotpQR enrollment={enrollment} />
            <FormError error={confirm.error} />
            <Field label="Mã 6 số từ ứng dụng" required>
              <Input autoComplete="one-time-code" value={code} maxLength={32} onChange={(e) => setCode(e.target.value)} />
            </Field>
            <Button block disabled={!code.trim()} loading={confirm.isPending} onClick={() => confirm.mutate(undefined)}>Xác nhận</Button>
          </div>
        )}
      </Modal>

      <Modal open={codes !== null} title="Lưu mã khôi phục" onClose={() => setCodes(null)}>
        {codes && <RecoveryCodes codes={codes} onDone={() => setCodes(null)} />}
      </Modal>

      <Modal open={disabling} title="Tắt xác thực hai bước" onClose={() => setDisabling(false)}>
        <div className="stack">
          <FormError error={disable.error} />
          <Field label="Mã xác thực hiện tại" required>
            <Input autoComplete="one-time-code" value={code} maxLength={32} onChange={(e) => setCode(e.target.value)} />
          </Field>
          <Button variant="danger" block disabled={!code.trim()} loading={disable.isPending} onClick={() => disable.mutate(undefined)}>Tắt</Button>
        </div>
      </Modal>
    </Card>
  );
}
