import { useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Icon, type IconName } from '../components/Icon';
import { Logo } from '../components/Logo';
import { useToast } from '../components/feedback';
import { opsApi } from '../lib/api/endpoints';
import type { Permission } from '../lib/api/types';
import { useAuth } from '../lib/auth';

interface NavItem {
  to: string;
  label: string;
  icon: IconName;
  anyOf: Permission[];
  end?: boolean;
  badge?: 'alerts';
}

const groups: { title: string; items: NavItem[] }[] = [
  {
    title: 'Quản trị',
    items: [
      { to: '/admin', label: 'Tổng quan', icon: 'chart', anyOf: ['revenue.read', 'policy.manage'], end: true },
      { to: '/admin/catalog', label: 'Sản phẩm', icon: 'shirt', anyOf: ['catalog.manage'] },
      { to: '/admin/promotions', label: 'Khuyến mãi', icon: 'tag', anyOf: ['promotion.manage'] },
      { to: '/admin/policies', label: 'Chính sách', icon: 'settings', anyOf: ['policy.manage'] },
      { to: '/admin/users', label: 'Người dùng', icon: 'users', anyOf: ['user.role_manage', 'user.lock'] },
      { to: '/admin/permissions', label: 'Phân quyền', icon: 'shield', anyOf: ['policy.manage'] },
      { to: '/admin/audit', label: 'Lịch sử thao tác', icon: 'list', anyOf: ['audit.read'] },
    ],
  },
  {
    title: 'Vận hành',
    items: [
      { to: '/ops', label: 'Hôm nay', icon: 'home', anyOf: ['booking.read_all'], end: true },
      { to: '/ops/bookings', label: 'Đơn thuê', icon: 'list', anyOf: ['booking.read_all'] },
      { to: '/ops/schedule', label: 'Lịch giao nhận', icon: 'calendar', anyOf: ['booking.read_all'] },
      { to: '/ops/alerts', label: 'Cảnh báo', icon: 'bell', anyOf: ['booking.read_all', 'inventory.read'], badge: 'alerts' },
      { to: '/ops/assets', label: 'Tài sản', icon: 'box', anyOf: ['inventory.read'] },
      { to: '/ops/support', label: 'Hỗ trợ khách', icon: 'help', anyOf: ['booking.read_all'] },
    ],
  },
];

export function BackOfficeLayout() {
  const { canAny, signOut } = useAuth();
  const toast = useToast();
  const navigate = useNavigate();
  const location = useLocation();
  const [open, setOpen] = useState(false);
  const alerts = useQuery({
    queryKey: ['ops', 'overview'],
    queryFn: opsApi.overview,
    enabled: canAny(['booking.read_all']),
    retry: false,
  });
  const counters = alerts.data?.counters;
  const alertCount = counters ? counters.overdue + counters.conflicts : 0;

  useEffect(() => setOpen(false), [location.pathname]);

  const logout = async () => {
    try {
      await signOut();
      navigate('/login');
    } catch {
      toast.error('Không đăng xuất được. Vui lòng thử lại.');
    }
  };

  const visible = groups
    .map((g) => ({ ...g, items: g.items.filter((i) => canAny(i.anyOf)) }))
    .filter((g) => g.items.length > 0);

  return (
    <div className="bo">
      <a className="skip-link" href="#bo-main">Đến nội dung chính</a>
      <button type="button" className="bo-scrim" data-open={open} aria-label="Đóng menu" onClick={() => setOpen(false)} />
      <aside className="bo-side" data-open={open}>
        <Logo tagline="Nội bộ" to={visible[0]?.items[0]?.to ?? '/'} />
        <nav aria-label="Điều hướng nội bộ">
          {visible.map((g) => (
            <div key={g.title}>
              <p className="nav-group-title">{g.title}</p>
              {g.items.map((i) => (
                <NavLink key={i.to} to={i.to} end={i.end} className={({ isActive }) => `nav-link${isActive ? ' active' : ''}`}>
                  <Icon name={i.icon} size={18} />{i.label}
                  {i.badge === 'alerts' && alertCount > 0 && <span className="nav-count" aria-label={`${alertCount} cảnh báo`}>{alertCount}</span>}
                </NavLink>
              ))}
            </div>
          ))}
        </nav>
        <div className="bo-side-foot">
          <NavLink to="/" className="nav-link"><Icon name="store" size={18} />Xem cửa hàng</NavLink>
          <NavLink to="/account/security" className="nav-link"><Icon name="shield" size={18} />Bảo mật tài khoản</NavLink>
          <button type="button" className="nav-link" style={{ background: 'none', border: 0, width: '100%' }} onClick={() => void logout()}>
            <Icon name="logout" size={18} />Đăng xuất
          </button>
        </div>
      </aside>
      <div className="bo-main">
        <header className="bo-top">
          <button type="button" className="icon-button" aria-label="Mở menu" onClick={() => setOpen(true)}><Icon name="menu" size={22} /></button>
          <Logo tagline="Nội bộ" to={visible[0]?.items[0]?.to ?? '/'} />
        </header>
        <main id="bo-main" className="bo-content"><Outlet /></main>
      </div>
    </div>
  );
}
