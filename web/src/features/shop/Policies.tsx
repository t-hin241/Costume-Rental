import { useQuery } from '@tanstack/react-query';
import { QueryState } from '../../components/feedback';
import { Card, DefinitionList, PageHeader, Skeleton } from '../../components/ui';
import { storeApi } from '../../lib/api/endpoints';
import type { PublicPolicy } from '../../lib/api/types';
import { formatHours, formatMoney } from '../../lib/format';

function cancellationRows(p: PublicPolicy) {
  const tiers = [...p.cancellation.tiers].sort((a, b) => b.min_notice_hours - a.min_notice_hours);
  return tiers.map((t, i) => {
    const previous = tiers[i - 1];
    const label = t.min_notice_hours > 0
      ? `Trước giờ nhận từ ${formatHours(t.min_notice_hours)}`
      : `Dưới ${formatHours(previous?.min_notice_hours ?? 0)} trước giờ nhận`;
    return { label, value: t.fee_percent === 0 ? 'Miễn phí' : `${t.fee_percent}% tiền thuê` };
  });
}

function reservationText(p: PublicPolicy): string {
  const { kind, amount, rent_percent: percent } = p.reservation;
  if (kind === 'NONE') return 'Không thu';
  return kind === 'FIXED' && amount ? formatMoney(amount) : `${percent}% tiền thuê`;
}

export default function Policies() {
  const policy = useQuery({ queryKey: ['public-policy'], queryFn: storeApi.policy, staleTime: 5 * 60_000 });
  return (
    <div className="container" style={{ paddingTop: 24 }}>
      <PageHeader title="Chính sách thuê" subtitle="Áp dụng theo phiên bản tại thời điểm bạn đặt đơn. Đơn đã đặt giữ nguyên điều khoản lúc đặt." />
      <QueryState query={policy} skeleton={<Skeleton height={320} />}>
        {(p) => (
          <div className="grid-2">
            <Card title="Giữ lịch">
              <DefinitionList items={[
                { label: 'Tiền giữ lịch', value: reservationText(p) },
                { label: 'Cách xử lý khi thuê thành công', value: p.reservation.treatment === 'APPLIED_TO_RENT' ? 'Trừ vào tiền thuê' : 'Hoàn lại sau khi trả' },
                { label: 'Thời gian giữ hàng khi đơn chờ', value: `${p.hold_minutes} phút` },
              ]} />
            </Card>
            <Card title="Hủy đơn">
              <DefinitionList items={[
                ...cancellationRows(p),
                { label: `Không đến nhận sau ${formatHours(p.cancellation.no_show_after_hours)}`, value: `${p.cancellation.no_show_fee_percent}% tiền thuê` },
              ]} />
            </Card>
            <Card title="Trả trễ">
              <DefinitionList items={[
                { label: `Trong ${p.late_fee.grace_minutes} phút đầu`, value: 'Miễn phí nếu không ảnh hưởng đơn kế tiếp' },
                ...p.late_fee.tiers.map((t) => ({ label: `Trễ đến ${formatHours(t.up_to_hours)}`, value: `${t.percent}% giá thuê/ngày của món` })),
                { label: `Mỗi ${formatHours(p.late_fee.extra_period_hours)} tiếp theo`, value: `Thêm ${p.late_fee.extra_percent}%` },
              ]} />
            </Card>
            <Card title="Đổi lịch, sửa đơn, thiếu món">
              <DefinitionList items={[
                { label: 'Tự đổi lịch đến trước giờ nhận', value: formatHours(p.reschedule_cutoff_hours) },
                { label: 'Tự sửa đơn đến trước giờ nhận', value: formatHours(p.modification_cutoff_hours) },
                { label: 'Thời hạn tìm lại món thiếu', value: formatHours(p.loss_verification_hours) },
              ]} />
            </Card>
          </div>
        )}
      </QueryState>
    </div>
  );
}
