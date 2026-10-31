import { useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { useAction } from '../../components/feedback';
import { Button, Field, FormError, Input, Notice, fieldError } from '../../components/ui';
import { authApi } from '../../lib/api/endpoints';

export function CheckEmail({ email }: { email: string }) {
  const resend = useAction(() => authApi.resendVerification(email), { success: 'Đã gửi lại email xác minh nếu tài khoản cần.' });
  return (
    <div className="stack">
      <h1>Kiểm tra email của bạn</h1>
      <Notice tone="brand">
        Nếu email hợp lệ, chúng tôi đã gửi liên kết xác minh đến <strong>{email}</strong>. Tài khoản dùng được sau khi bạn mở liên kết.
      </Notice>
      <Button variant="outline" loading={resend.isPending} onClick={() => resend.mutate(undefined)}>Gửi lại email</Button>
      <Link className="link small" to="/login">Về đăng nhập</Link>
    </div>
  );
}

export default function Register() {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [sent, setSent] = useState(false);
  const register = useAction(() => authApi.register(email.trim(), password), { inline: true, onSuccess: () => setSent(true) });

  if (sent) return <CheckEmail email={email.trim()} />;

  const mismatch = confirm !== '' && confirm !== password;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!mismatch) register.mutate(undefined);
  };

  return (
    <form className="stack" onSubmit={submit}>
      <div>
        <h1>Tạo tài khoản</h1>
        <p className="muted">Đặt thuê, theo dõi đơn và nhận hỗ trợ từ cửa hàng.</p>
      </div>
      <FormError error={register.error} inlineFields />
      <Field label="Email" required error={fieldError(register.error, 'email')}>
        <Input type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
      </Field>
      <Field label="Mật khẩu" required error={fieldError(register.error, 'password')}>
        <Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
      </Field>
      <Field label="Nhập lại mật khẩu" required error={mismatch ? 'Mật khẩu nhập lại không khớp.' : undefined}>
        <Input type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
      </Field>
      <Button type="submit" size="lg" block loading={register.isPending}>Đăng ký</Button>
      <p className="small muted">Đã có tài khoản? <Link className="link" to="/login">Đăng nhập</Link></p>
    </form>
  );
}
