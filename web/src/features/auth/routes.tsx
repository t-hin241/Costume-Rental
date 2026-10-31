import type { ReactNode } from 'react';
import { Route, Routes } from 'react-router-dom';
import { Logo } from '../../components/Logo';
import ForgotPassword from './ForgotPassword';
import Login from './Login';
import Register from './Register';
import ResetPassword from './ResetPassword';
import VerifyEmail from './VerifyEmail';

/** Paths match the links in emails (decision D24). */
export default function AuthPages({ fallback }: { fallback: ReactNode }) {
  return (
    <Routes>
      <Route path="login" element={<AuthShell><Login /></AuthShell>} />
      <Route path="register" element={<AuthShell><Register /></AuthShell>} />
      <Route path="verify-email" element={<AuthShell><VerifyEmail /></AuthShell>} />
      <Route path="forgot-password" element={<AuthShell><ForgotPassword /></AuthShell>} />
      <Route path="reset-password" element={<AuthShell><ResetPassword /></AuthShell>} />
      <Route path="*" element={fallback} />
    </Routes>
  );
}

function AuthShell({ children }: { children: ReactNode }) {
  return (
    <div className="auth-page">
      <aside className="auth-art">
        <Logo />
        <h2>Mỗi dịp một <em>vai diễn</em>, chỉ cần chọn đúng size.</h2>
        <svg className="honeycomb" viewBox="0 0 420 380" aria-hidden="true">
          {honeycomb.map(([x, y, fill]) => (
            <path key={`${x}-${y}`} fill={fill} transform={`translate(${x} ${y})`} d="M60 0 112 30v60L60 120 8 90V30z" />
          ))}
        </svg>
      </aside>
      <main className="auth-main">
        <div className="auth-card">
          <div className="auth-logo-mobile"><Logo /></div>
          {children}
        </div>
      </main>
    </div>
  );
}

const honeycomb: [number, number, string][] = [
  [0, 130, '#2c2c29'], [104, 130, '#ffc400'], [208, 130, '#2c2c29'], [312, 130, '#ffc400'],
  [52, 220, '#ffc400'], [156, 220, '#2c2c29'], [260, 220, '#ffffff'],
  [156, 40, '#2c2c29'], [260, 40, '#ffc400'],
];
