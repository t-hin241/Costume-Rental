import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { QueryState } from '../../components/feedback';
import { Card, Notice, Skeleton, StatusBadge } from '../../components/ui';
import { revenueApi } from '../../lib/api/endpoints';
import type { RevenueYear } from '../../lib/api/types';
import { formatMoney, formatNumber } from '../../lib/format';
import { thresholdLevel } from '../../lib/labels';

/** Decision D41: calendar-year revenue against the threshold, with the warning marks. */
export function ThresholdCard({ year, link }: { year?: number; link?: boolean }) {
  const result = useQuery({ queryKey: ['revenue', 'year', year ?? 'current'], queryFn: () => revenueApi.year(year) });
  return (
    <QueryState query={result} skeleton={<Skeleton height={150} />}>
      {(y) => <ThresholdView data={y} link={link} />}
    </QueryState>
  );
}

function ThresholdView({ data, link }: { data: RevenueYear; link?: boolean }) {
  const more = link && <Link className="link small" to="/admin/revenue">Xem sổ doanh thu</Link>;
  if (!data.has_profile || !data.revenue || !data.threshold) {
    return (
      <Card title={`Ngưỡng doanh thu năm ${data.year}`} actions={more}>
        <Notice tone="warning" title="Chưa có hồ sơ thuế">
          Hệ thống chưa theo dõi ngưỡng vì chưa có hồ sơ thuế cho năm này. Admin tạo hồ sơ ở trang Doanh thu &amp; thuế.
        </Notice>
      </Card>
    );
  }
  const width = Math.min(data.percent, 100);
  return (
    <Card title={`Ngưỡng doanh thu năm ${data.year}`} actions={more}>
      <div className="stack">
        <div className="between">
          <div>
            <div className="stat-value tnum">{formatMoney(data.revenue)}</div>
            <div className="small muted">trên ngưỡng {formatMoney(data.threshold)} · {formatNumber(data.percent)}%</div>
          </div>
          <StatusBadge status={thresholdLevel[data.level]} />
        </div>
        <div className="hbar-track" role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={width}
          aria-label={`Doanh thu bằng ${data.percent}% ngưỡng năm`}>
          <div className={`hbar-fill${data.level === 'BELOW' ? '' : ' tone-brand-bar'}`} style={{ width: `${width}%` }} />
        </div>
        <p className="small muted">
          Mốc cảnh báo: {data.marks.map((m) => `${m}%`).join(', ')} và khi vượt ngưỡng.
          {data.mark > 0 && ` Đã chạm mốc ${data.mark}%.`}
        </p>
        {data.level === 'EXCEEDED' && (
          <Notice tone="danger" title="Doanh thu đã vượt ngưỡng">
            Hệ thống không tự đổi cách tính hay thêm thuế. Hãy rà soát lại nghĩa vụ thuế với kế toán.
          </Notice>
        )}
        {data.pending_bookings > 0 && (
          <p className="small muted">{formatNumber(data.pending_bookings)} đơn đã kết thúc đang chờ ghi nhận doanh thu.</p>
        )}
      </div>
    </Card>
  );
}
