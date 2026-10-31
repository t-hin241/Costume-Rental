import { useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { useAction } from '../../components/feedback';
import { Button, Field, FormError, Input, Notice } from '../../components/ui';
import { authApi } from '../../lib/api/endpoints';

export default function ForgotPassword() {
  const [email, setEmail] = useState('');
  const [sent, setSent] = useState(false);
  const request = useAction(() => authApi.requestReset(email.trim()), { inline: true, onSuccess: () => setSent(true) });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    request.mutate(undefined);
  };

  if (sent) {
    return (
      <div className="stack">
        <h1>Kiểm tra email của bạn</h1>
        <Notice tone="brand">Nếu email có tài khoản, chúng tôi đã gửi liên kết đặt lại mật khẩu.</Notice>
        <Link className="link small" to="/login">Về đăng nhập</Link>
      </div>
    );
  }

  return (
    <form className="stack" onSubmit={submit}>
      <div>
        <h1>Quên mật khẩu</h1>
        <p className="muted">Nhập email, chúng tôi sẽ gửi liên kết đặt lại mật khẩu.</p>
      </div>
      <FormError error={request.error} />
      <Field label="Email" required>
        <Input type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
      </Field>
      <Button type="submit" size="lg" block loading={request.isPending}>Gửi liên kết</Button>
      <Link className="link small" to="/login">Về đăng nhập</Link>
    </form>
  );
}
