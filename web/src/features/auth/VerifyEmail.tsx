import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { Notice, Skeleton, btn } from '../../components/ui';
import { authApi } from '../../lib/api/endpoints';
import { messageForCode } from '../../lib/api/errors';
import { clearFragmentToken, fragmentToken } from './token';

type State = 'working' | 'done' | 'failed';

// The link is single-use, and React may run an effect twice or mount the page
// again, so each token is sent once and every run waits for that one answer.
const attempts = new Map<string, Promise<void>>();

function verifyOnce(token: string): Promise<void> {
  let attempt = attempts.get(token);
  if (!attempt) {
    attempt = authApi.verifyEmail(token);
    attempts.set(token, attempt);
  }
  return attempt;
}

export default function VerifyEmail() {
  const [state, setState] = useState<State>('working');
  const [message, setMessage] = useState('');

  useEffect(() => {
    const token = fragmentToken();
    clearFragmentToken();
    if (!token) {
      setMessage(messageForCode('identity.invalid_verification_token'));
      setState('failed');
      return;
    }
    let live = true;
    verifyOnce(token).then(() => live && setState('done'), (err: Error) => {
      if (!live) return;
      setMessage(err.message);
      setState('failed');
    });
    return () => { live = false; };
  }, []);

  return (
    <div className="stack">
      <h1>Xác minh email</h1>
      {state === 'working' && <Skeleton height={60} />}
      {state === 'done' && <Notice tone="success" title="Email đã được xác minh">Bạn có thể đăng nhập ngay bây giờ.</Notice>}
      {state === 'failed' && <Notice tone="danger" title={message}>Bạn có thể yêu cầu gửi lại liên kết từ trang đăng ký.</Notice>}
      <Link className={btn('primary', 'lg', 'btn-block')} to="/login">Đến trang đăng nhập</Link>
    </div>
  );
}
