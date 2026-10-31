import { useEffect, useState } from 'react';
import { Button, Checkbox, Notice } from '../../components/ui';
import type { MFAEnrollment } from '../../lib/api/types';

/** Draws the QR in the browser: the seed must never reach a third-party service. */
export function TotpQR({ enrollment }: { enrollment: MFAEnrollment }) {
  const [src, setSrc] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    void import('qrcode').then(({ toDataURL }) => toDataURL(enrollment.otpauth_uri, { width: 196, margin: 1 })).then((url) => {
      if (live) setSrc(url);
    }).catch(() => setSrc(null));
    return () => { live = false; };
  }, [enrollment.otpauth_uri]);

  return (
    <div className="stack-sm">
      {src && <img className="qr" src={src} alt="Mã QR để thêm vào ứng dụng xác thực" />}
      <p className="small muted">Không quét được? Nhập khóa này vào ứng dụng:</p>
      <p className="secret-box mono small">{enrollment.secret}</p>
    </div>
  );
}

export function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const [saved, setSaved] = useState(false);
  return (
    <div className="stack">
      <Notice tone="warning" title="Mã chỉ hiện một lần">
        Lưu các mã này ở nơi an toàn. Mỗi mã dùng được một lần khi bạn không có ứng dụng xác thực.
      </Notice>
      <div className="code-grid mono">{codes.map((c) => <span key={c}>{c}</span>)}</div>
      <Button variant="outline" onClick={() => void navigator.clipboard.writeText(codes.join('\n'))}>Sao chép tất cả</Button>
      <Checkbox label="Tôi đã lưu các mã khôi phục" checked={saved} onChange={(e) => setSaved(e.target.checked)} />
      <Button block disabled={!saved} onClick={onDone}>Tiếp tục</Button>
    </div>
  );
}
