import { Link } from 'react-router-dom';
import { LogoMark } from '../components/Logo';
import { btn } from '../components/ui';

export default function NotFound() {
  return (
    <main className="not-found">
      <div className="stack" style={{ alignItems: 'center' }}>
        <LogoMark size={64} />
        <h1 className="page-title">Không tìm thấy trang</h1>
        <p className="muted">Liên kết có thể đã thay đổi hoặc không còn tồn tại.</p>
        <Link className={btn('primary')} to="/">Về trang chủ</Link>
      </div>
    </main>
  );
}
