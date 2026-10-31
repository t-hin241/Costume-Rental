import { useState, type FormEvent } from 'react';
import { Link, Navigate, useNavigate, useSearchParams } from 'react-router-dom';
import { Button, Field, FormError, Input, Notice } from '../../components/ui';
import { authApi } from '../../lib/api/endpoints';
import type { LoginChallenge, MFAEnrollment, Session } from '../../lib/api/types';
import { homeFor, safeNext, useAuth } from '../../lib/auth';
import { useAction } from '../../components/feedback';
import { RecoveryCodes, TotpQR } from './mfa';

type Step =
  | { name: 'password' }
  | { name: 'code'; challenge: LoginChallenge; enrollment: MFAEnrollment | null }
  | { name: 'recovery'; session: Session };

export default function Login() {
  const { session, setSession } = useAuth();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [step, setStep] = useState<Step>({ name: 'password' });
  const next = safeNext(params.get('next'));

  const finish = (s: Session) => {
    setSession(s);
    navigate(next ?? homeFor(s), { replace: true });
  };

  // A session that arrives with recovery codes must show them before moving on.
  const afterSecondFactor = (s: Session) => {
    if (s.recovery_codes?.length) setStep({ name: 'recovery', session: s });
    else finish(s);
  };

  if (session && step.name === 'password') return <Navigate to={next ?? homeFor(session)} replace />;

  if (step.name === 'recovery') {
    return (
      <>
        <h1>Lưu mã khôi phục</h1>
        <RecoveryCodes codes={step.session.recovery_codes ?? []} onDone={() => finish(step.session)} />
      </>
    );
  }

  if (step.name === 'code') {
    return <CodeStep challenge={step.challenge} enrollment={step.enrollment} onDone={afterSecondFactor} onRestart={() => setStep({ name: 'password' })} />;
  }

  return (
    <PasswordStep
      onSession={finish}
      onChallenge={(challenge, enrollment) => setStep({ name: 'code', challenge, enrollment })}
      registerLink={next ? `/register?next=${encodeURIComponent(next)}` : '/register'}
    />
  );
}

function PasswordStep({ onSession, onChallenge, registerLink }: {
  onSession: (s: Session) => void;
  onChallenge: (c: LoginChallenge, e: MFAEnrollment | null) => void;
  registerLink: string;
}) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');

  type Outcome = { session: Session } | { challenge: LoginChallenge; enrollment: MFAEnrollment | null };

  const login = useAction(
    async (): Promise<Outcome> => {
      const result = await authApi.login(email.trim(), password);
      if (result.status === 'authenticated') return { session: result };
      const enrollment = result.status === 'mfa_enrollment_required' ? await authApi.loginEnroll(result.challenge) : null;
      return { challenge: result, enrollment };
    },
    {
      inline: true,
      onSuccess: (r) => {
        if ('session' in r) onSession(r.session);
        else onChallenge(r.challenge, r.enrollment);
      },
    },
  );

  const submit = (e: FormEvent) => {
    e.preventDefault();
    login.mutate(undefined);
  };

  return (
    <form className="stack" onSubmit={submit}>
      <div>
        <h1>Đăng nhập</h1>
        <p className="muted">Theo dõi đơn thuê và đặt trang phục nhanh hơn.</p>
      </div>
      {__USE_MOCKS__ && (
        <Notice tone="brand" title="Tài khoản mô phỏng">
          khach@, nhanvien@, kho@, taichinh@, admin@webee.test. Mật khẩu demo-webee-2026, mã xác thực 123456.
        </Notice>
      )}
      <FormError error={login.error} />
      <Field label="Email" required>
        <Input type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} required />
      </Field>
      <Field label="Mật khẩu" required>
        <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
      </Field>
      <div className="row between small">
        <Link className="link" to="/forgot-password">Quên mật khẩu?</Link>
      </div>
      <Button type="submit" size="lg" block loading={login.isPending}>Đăng nhập</Button>
      <p className="small muted">Chưa có tài khoản? <Link className="link" to={registerLink}>Đăng ký</Link></p>
    </form>
  );
}

function CodeStep({ challenge, enrollment, onDone, onRestart }: {
  challenge: LoginChallenge; enrollment: MFAEnrollment | null; onDone: (s: Session) => void; onRestart: () => void;
}) {
  const [code, setCode] = useState('');
  const verify = useAction(() => authApi.loginMFA(challenge.challenge, code.trim()), { inline: true, onSuccess: onDone });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    verify.mutate(undefined);
  };

  return (
    <form className="stack" onSubmit={submit}>
      <div>
        <h1>{enrollment ? 'Bật xác thực hai bước' : 'Xác thực hai bước'}</h1>
        <p className="muted">
          {enrollment
            ? 'Vai trò của bạn bắt buộc dùng xác thực hai bước. Quét mã bằng ứng dụng xác thực rồi nhập mã 6 số.'
            : 'Nhập mã 6 số từ ứng dụng xác thực, hoặc một mã khôi phục.'}
        </p>
      </div>
      {enrollment && <TotpQR enrollment={enrollment} />}
      <FormError error={verify.error} />
      <Field label="Mã xác thực" required>
        <Input inputMode="text" autoComplete="one-time-code" autoFocus value={code} maxLength={32} onChange={(e) => setCode(e.target.value)} required />
      </Field>
      <Button type="submit" size="lg" block loading={verify.isPending}>Xác nhận</Button>
      <Button variant="ghost" onClick={onRestart}>Đăng nhập lại</Button>
      {enrollment && <Notice tone="brand">Sau bước này, hệ thống hiện mã khôi phục một lần duy nhất.</Notice>}
    </form>
  );
}
