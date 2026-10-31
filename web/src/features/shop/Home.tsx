import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router-dom';
import { QueryState } from '../../components/feedback';
import { EmptyState, Skeleton, btn } from '../../components/ui';
import { catalogApi } from '../../lib/api/endpoints';
import { ProductCard } from './shared';

const steps = [
  { title: 'Chọn trang phục', body: 'Tìm theo danh mục, nhân vật hoặc dịp bạn cần, thuê nguyên bộ hoặc từng món.' },
  { title: 'Chọn size và lịch', body: 'Chọn size, gói thuê, ngày nhận. Hệ thống báo ngay còn hàng hay không.' },
  { title: 'Đặt thuê, giữ lịch', body: 'Xem rõ từng khoản tiền rồi đặt. Đơn được giữ theo thời hạn hiển thị trên đơn.' },
  { title: 'Nhận và trả', body: 'Nhận đồ, sử dụng, trả đúng hạn. Tiền bảo đảm được đối soát sau khi kiểm tra.' },
];

const moneyTerms = [
  { title: 'Tiền thuê', body: 'Khoản trả cho thời gian sử dụng theo gói bạn chọn.' },
  { title: 'Tiền giữ lịch', body: 'Khoản thu để cửa hàng giữ hàng cho lịch của bạn.' },
  { title: 'Tiền bảo đảm', body: 'Khoản hoàn lại sau khi trả hàng, trừ khoản khấu trừ hợp lệ nếu có.' },
  { title: 'Phí giao nhận', body: 'Chỉ áp dụng khi bạn chọn giao hoặc lấy tận nơi.' },
];

export default function Home() {
  const categories = useQuery({ queryKey: ['categories'], queryFn: catalogApi.categories, staleTime: 5 * 60_000 });
  const featured = useQuery({
    queryKey: ['offerings', 'featured'],
    queryFn: () => catalogApi.offerings({ sort: 'popular', page_size: 8 }),
  });
  const mosaic = featured.data?.items.filter((o) => o.images[0]).slice(0, 5) ?? [];

  return (
    <>
      <section className="hero on-dark">
        <div className="container hero-inner">
          <div>
            <h1>Thuê trang phục cho <em>mọi vai diễn</em> của bạn.</h1>
            <p className="hero-lead">Chọn đúng size, xem lịch còn hàng theo ngày bạn cần và biết rõ từng khoản tiền trước khi đặt.</p>
            <div className="hero-actions">
              <Link className={btn('primary', 'lg')} to="/catalog">Xem trang phục</Link>
              <Link className={btn('outline', 'lg')} to="/catalog?kind=BUNDLE">Thuê nguyên bộ</Link>
            </div>
          </div>
          <div className="hex-mosaic" aria-hidden={mosaic.length === 0}>
            {mosaic.length > 0
              ? mosaic.map((o) => (
                <Link key={o.id} className="hex" to={`/catalog/${o.kind.toLowerCase()}/${o.slug}`} aria-label={o.name}>
                  <img src={o.images[0]?.url} alt="" />
                </Link>
              ))
              : Array.from({ length: 5 }, (_, i) => <span key={i} className={`hex${i % 2 ? ' hex-fill' : ''}`} />)}
          </div>
        </div>
      </section>

      <div className="container">
        <QueryState query={categories} skeleton={<div className="section"><Skeleton height={150} /></div>}>
          {(items) => items.length > 0 && (
            <section className="section" aria-labelledby="cat-title">
              <div className="section-head"><h2 id="cat-title" className="section-title">Danh mục</h2></div>
              <div className="category-rail">
                {items.map((c) => (
                  <Link key={c.id} className="category-tile" to={`/catalog?category=${c.slug}`}>
                    {c.image && <img src={c.image.url} alt="" loading="lazy" />}
                    <span>{c.name}<small>{c.offering_count} mẫu</small></span>
                  </Link>
                ))}
              </div>
            </section>
          )}
        </QueryState>

        <section className="section" aria-labelledby="featured-title">
          <div className="section-head">
            <h2 id="featured-title" className="section-title">Được thuê nhiều</h2>
            <Link className="link small" to="/catalog">Xem tất cả</Link>
          </div>
          <QueryState query={featured} skeleton={<Skeleton height={380} />}>
            {(page) => page.items.length === 0
              ? <EmptyState icon="shirt" title="Chưa có trang phục nào" />
              : <div className="product-grid">{page.items.map((o) => <ProductCard key={o.id} offering={o} />)}</div>}
          </QueryState>
        </section>

        <section className="section" aria-labelledby="how-title">
          <div className="section-head"><h2 id="how-title" className="section-title">Thuê trong bốn bước</h2></div>
          <ol className="steps-grid">
            {steps.map((s, i) => (
              <li key={s.title} className="step-card">
                <span className="step-num">{i + 1}</span>
                <h3>{s.title}</h3>
                <p>{s.body}</p>
              </li>
            ))}
          </ol>
        </section>

        <section className="section" aria-labelledby="money-title">
          <div className="money-explain on-dark">
            <div className="section-head">
              <h2 id="money-title" className="section-title">Rõ từng khoản tiền</h2>
              <Link className={btn('outline', 'sm')} to="/policies">Xem chính sách thuê</Link>
            </div>
            <div className="grid-4">
              {moneyTerms.map((t) => <div key={t.title}><h3>{t.title}</h3><p>{t.body}</p></div>)}
            </div>
          </div>
        </section>
      </div>
    </>
  );
}
