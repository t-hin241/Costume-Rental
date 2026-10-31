import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { Notice, Skeleton, btn } from '../../components/ui';
import { authApi } from '../../lib/api/endpoints';
import { messageForCode } from '../../lib/api/errors';
import { takeFragmentToken } from './token';

type State = 'working' | 'done' | 'failed';

export default function VerifyEmail() {
  const [state, setState] = useState<State>('working');
  const [message, setMessage] = useState('');
  const started = useRef(false);

  useEffect(() => {
    // The link is single-use and React may run effects twice in development.
    if (started.current) return;
    started.current = true;
    const token = takeFragmentToken();
    if (!token) {
      setMessage(messageForCode('identity.invalid_verification_token'));
      setState('failed');
      return;
    }
    authApi.verifyEmail(token).then(() => setState('done'), (err: Error) => {
      setMessage(err.message);
      setState('failed');
    });
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
