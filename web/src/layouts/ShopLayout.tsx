import { useQuery } from '@tanstack/react-query';
import { useEffect, useState, type FormEvent } from 'react';
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom';
import { Icon, type IconName } from '../components/Icon';
import { Logo } from '../components/Logo';
import { useToast } from '../components/feedback';
import { storeApi } from '../lib/api/endpoints';
import { homeFor, isBackOffice, useAuth } from '../lib/auth';
import { useBag } from '../lib/bag';

const nav = [
  { to: '/catalog', label: 'Tất cả trang phục' },
  { to: '/catalog?kind=BUNDLE', label: 'Thuê nguyên bộ' },
  { to: '/policies', label: 'Chính sách thuê' },
];

export function ShopLayout() {
  const { session } = useAuth();
  const { count } = useBag();
  const navigate = useNavigate();
  const location = useLocation();
  const [menuOpen, setMenuOpen] = useState(false);
  const [term, setTerm] = useState('');

  useEffect(() => {
    setMenuOpen(false);
    window.scrollTo({ top: 0 });
  }, [location.pathname]);

  const search = (e: FormEvent) => {
    e.preventDefault();
    navigate(`/catalog?q=${encodeURIComponent(term.trim())}`);
  };

  return (
    <div className="shop">
      <a className="skip-link" href="#main">Đến nội dung chính</a>
      <header className="shop-header on-dark">
        <div className="container">
          <div className="shop-header-inner">
            <Logo />
            <nav className="shop-nav" aria-label="Điều hướng chính">
              {nav.map((n) => <NavLink key={n.to} to={n.to} end className={navClass(n.to, location)}>{n.label}</NavLink>)}
            </nav>
            <form className="shop-search" role="search" onSubmit={search}>
              <Icon name="search" size={18} />
              <input type="search" aria-label="Tìm trang phục" placeholder="Tìm trang phục, nhân vật..." value={term} onChange={(e) => setTerm(e.target.value)} />
            </form>
            <div className="shop-tools">
              <Link to="/checkout" className="icon-button" aria-label={`Giỏ thuê, ${count} món`}>
                <Icon name="bag" size={22} />
                {count > 0 && <span className="bag-count">{count}</span>}
              </Link>
              <Link to={session ? homeFor(session) : '/login'} className="icon-button" aria-label={session ? 'Tài khoản' : 'Đăng nhập'}>
                <Icon name="user" size={22} />
              </Link>
              <button type="button" className="icon-button shop-menu-button" aria-label="Mở menu" aria-expanded={menuOpen} onClick={() => setMenuOpen((v) => !v)}>
                <Icon name={menuOpen ? 'close' : 'menu'} size={22} />
              </button>
            </div>
          </div>
          <nav className="shop-mobile-nav" hidden={!menuOpen} aria-label="Điều hướng trên di động">
            {nav.map((n) => <NavLink key={n.to} to={n.to} end className={navClass(n.to, location)}>{n.label}</NavLink>)}
          </nav>
        </div>
      </header>
      <main id="main"><Outlet /></main>
      <ShopFooter />
    </div>
  );
}

// NavLink ignores the query string, so the two catalog links need this.
function navClass(to: string, location: { pathname: string; search: string }) {
  return () => (to === location.pathname + location.search ? 'active' : '');
}

function ShopFooter() {
  const { data: store } = useQuery({ queryKey: ['store-info'], queryFn: storeApi.info, staleTime: 5 * 60_000, retry: false });
  return (
    <footer className="shop-footer">
      <div className="container shop-footer-grid">
        <div>
          <Logo />
          <p className="small" style={{ marginTop: 10, maxWidth: '36ch' }}>Thuê trang phục đúng size, đúng lịch, rõ từng khoản tiền.</p>
        </div>
        <div>
          <h3>Khám phá</h3>
          <ul className="small">
            <li><Link to="/catalog">Tất cả trang phục</Link></li>
            <li><Link to="/policies">Chính sách thuê</Link></li>
            <li><Link to="/account/support">Yêu cầu hỗ trợ</Link></li>
          </ul>
        </div>
        {store && (
          <div>
            <h3>{store.name}</h3>
            <ul className="small">
              <li>{store.address}</li>
              <li>{store.hours}</li>
              <li><a href={`tel:${store.phone.replace(/\s/g, '')}`}>{store.phone}</a></li>
              <li><a href={`mailto:${store.email}`}>{store.email}</a></li>
            </ul>
          </div>
        )}
      </div>
      <div className="shop-footer-bottom"><div className="container">© {new Date().getFullYear()} Webee Rental</div></div>
    </footer>
  );
}

const accountNav: { to: string; label: string; icon: IconName }[] = [
  { to: '/account/bookings', label: 'Đơn thuê của tôi', icon: 'list' },
  { to: '/account/support', label: 'Yêu cầu hỗ trợ', icon: 'help' },
  { to: '/account/profile', label: 'Hồ sơ', icon: 'user' },
  { to: '/account/security', label: 'Bảo mật', icon: 'shield' },
];

export function AccountLayout() {
  const { session, signOut } = useAuth();
  const toast = useToast();
  const navigate = useNavigate();

  const logout = async () => {
    try {
      await signOut();
      navigate('/');
    } catch {
      toast.error('Không đăng xuất được. Vui lòng thử lại.');
    }
  };

  return (
    <div className="container account-layout">
      <nav className="account-nav" aria-label="Tài khoản">
        {accountNav.map((n) => (
          <NavLink key={n.to} to={n.to}><Icon name={n.icon} size={18} />{n.label}</NavLink>
        ))}
        {session && isBackOffice(session) && <Link to={homeFor(session)}><Icon name="grid" size={18} />Trang làm việc</Link>}
        <a href="#logout" onClick={(e) => { e.preventDefault(); void logout(); }}><Icon name="logout" size={18} />Đăng xuất</a>
      </nav>
      <div><Outlet /></div>
    </div>
  );
}
