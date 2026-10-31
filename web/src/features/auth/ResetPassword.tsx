import { useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { useAction } from '../../components/feedback';
import { Button, Field, FormError, Input, Notice, btn, fieldError } from '../../components/ui';
import { authApi } from '../../lib/api/endpoints';
import { takeFragmentToken } from './token';

export default function ResetPassword() {
  const [token] = useState(takeFragmentToken);
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [done, setDone] = useState(false);
  const reset = useAction(() => authApi.confirmReset(token ?? '', password), { inline: true, onSuccess: () => setDone(true) });

  if (done) {
    return (
      <div className="stack">
        <h1>Đã đặt lại mật khẩu</h1>
        <Notice tone="success">Mọi phiên đăng nhập cũ đã bị đăng xuất. Hãy đăng nhập bằng mật khẩu mới.</Notice>
        <Link className={btn('primary', 'lg', 'btn-block')} to="/login">Đăng nhập</Link>
      </div>
    );
  }

  if (!token) {
    return (
      <div className="stack">
        <h1>Đặt lại mật khẩu</h1>
        <Notice tone="danger" title="Liên kết không hợp lệ">Vui lòng yêu cầu một liên kết mới.</Notice>
        <Link className={btn('primary', 'md', 'btn-block')} to="/forgot-password">Yêu cầu liên kết mới</Link>
      </div>
    );
  }

  const mismatch = confirm !== '' && confirm !== password;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!mismatch) reset.mutate(undefined);
  };

  return (
    <form className="stack" onSubmit={submit}>
      <h1>Đặt mật khẩu mới</h1>
      <FormError error={reset.error} inlineFields />
      <Field label="Mật khẩu mới" required error={fieldError(reset.error, 'new_password') ?? fieldError(reset.error, 'password')}>
        <Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
      </Field>
      <Field label="Nhập lại mật khẩu mới" required error={mismatch ? 'Mật khẩu nhập lại không khớp.' : undefined}>
        <Input type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
      </Field>
      <Button type="submit" size="lg" block loading={reset.isPending}>Lưu mật khẩu</Button>
    </form>
  );
}
