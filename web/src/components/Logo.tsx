import { Link } from 'react-router-dom';

export function LogoMark({ size = 32 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true">
      <path fill="#ffc400" d="M16 1.500 28.600 8.750v14.500L16 30.500 3.400 23.250V8.750z" />
      <path fill="#111" d="M9 11h3.200l2 7.200L16 12l1.800 6.200 2-7.200H23l-3.800 11h-2.600L16 19.600 15.400 22h-2.600z" />
    </svg>
  );
}

export function Logo({ to = '/', tagline }: { to?: string; tagline?: string }) {
  return (
    <Link to={to} className="logo" aria-label="Webee Rental, về trang chủ">
      <LogoMark />
      <span className="logo-text">Webee<span>Rental</span></span>
      {tagline && <span className="logo-tag">{tagline}</span>}
    </Link>
  );
}
