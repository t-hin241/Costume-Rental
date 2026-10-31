import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { QueryState } from '../../components/feedback';
import { Button, EmptyState, PageHeader, Pagination, Select, Skeleton } from '../../components/ui';
import { catalogApi } from '../../lib/api/endpoints';
import type { OfferingKind } from '../../lib/api/types';
import { ProductCard } from './shared';

const pageSize = 12;

const sorts = [
  { value: 'popular', label: 'Thuê nhiều' },
  { value: 'newest', label: 'Mới nhất' },
  { value: 'price_asc', label: 'Giá tăng dần' },
  { value: 'price_desc', label: 'Giá giảm dần' },
];

const kinds: { value: OfferingKind | ''; label: string }[] = [
  { value: '', label: 'Tất cả' },
  { value: 'BUNDLE', label: 'Nguyên bộ' },
  { value: 'ITEM', label: 'Từng món' },
];

function ChipFilter({ title, value, options, onChange }: {
  title: string; value: string; options: { value: string; label: string }[]; onChange: (v: string) => void;
}) {
  return (
    <div>
      <p className="filter-title">{title}</p>
      <div className="chip-group">
        {options.map((o) => (
          <button key={o.value} type="button" className="chip" aria-pressed={value === o.value}
            onClick={() => onChange(value === o.value ? '' : o.value)}>{o.label}</button>
        ))}
      </div>
    </div>
  );
}

export default function Catalog() {
  const [params, setParams] = useSearchParams();
  const [filtersOpen, setFiltersOpen] = useState(false);
  const q = params.get('q') ?? '';
  const category = params.get('category') ?? '';
  const kind = (params.get('kind') ?? '') as OfferingKind | '';
  const size = params.get('size') ?? '';
  const color = params.get('color') ?? '';
  const sort = params.get('sort') ?? 'popular';
  const page = Number(params.get('page')) || 1;

  const categories = useQuery({ queryKey: ['categories'], queryFn: catalogApi.categories, staleTime: 5 * 60_000 });
  const result = useQuery({
    queryKey: ['offerings', { q, category, kind, size, color, sort, page }],
    queryFn: () => catalogApi.offerings({ q, category, kind, size, color, sort, page, page_size: pageSize }),
    placeholderData: keepPreviousData,
  });

  useEffect(() => { window.scrollTo({ top: 0 }); }, [page]);

  const update = (changes: Record<string, string>) => {
    setParams((prev) => {
      const next = new URLSearchParams(prev);
      for (const [k, v] of Object.entries(changes)) {
        if (v) next.set(k, v); else next.delete(k);
      }
      if (!('page' in changes)) next.delete('page');
      return next;
    });
  };

  const activeCategory = categories.data?.find((c) => c.slug === category);
  const hasFilters = Boolean(category || kind || size || color);
  const facets = result.data?.facets;

  return (
    <div className="container" style={{ paddingTop: 24 }}>
      <PageHeader
        title={activeCategory?.name ?? (kind === 'BUNDLE' ? 'Thuê nguyên bộ' : q ? `Kết quả cho “${q}”` : 'Tất cả trang phục')}
        subtitle={result.data ? `${result.data.total} mẫu` : undefined}
      />
      <div className="catalog-layout" style={{ paddingTop: 0 }}>
        <aside className="filters" data-open={filtersOpen} aria-label="Bộ lọc">
          <ChipFilter title="Hình thức thuê" value={kind} options={kinds.filter((k) => k.value).map((k) => ({ value: k.value, label: k.label }))} onChange={(v) => update({ kind: v })} />
          {categories.data && categories.data.length > 0 && (
            <ChipFilter title="Danh mục" value={category} options={categories.data.map((c) => ({ value: c.slug, label: c.name }))} onChange={(v) => update({ category: v })} />
          )}
          {facets && facets.sizes.length > 0 && (
            <ChipFilter title="Size" value={size} options={facets.sizes.map((s) => ({ value: s, label: s }))} onChange={(v) => update({ size: v })} />
          )}
          {facets && facets.colors.length > 0 && (
            <ChipFilter title="Màu" value={color} options={facets.colors.map((c) => ({ value: c, label: c }))} onChange={(v) => update({ color: v })} />
          )}
          {hasFilters && <Button variant="ghost" size="sm" onClick={() => update({ category: '', kind: '', size: '', color: '' })}>Xóa bộ lọc</Button>}
        </aside>
        <div>
          <div className="catalog-toolbar">
            <Button className="filter-toggle" variant="outline" size="sm" icon="settings" aria-expanded={filtersOpen} onClick={() => setFiltersOpen((v) => !v)}>Bộ lọc</Button>
            <label className="row small" style={{ marginLeft: 'auto', gap: 8 }}>
              Sắp xếp
              <Select aria-label="Sắp xếp" value={sort} onChange={(e) => update({ sort: e.target.value })}>
                {sorts.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}
              </Select>
            </label>
          </div>
          <QueryState query={result} skeleton={<Skeleton height={480} />}>
            {(data) => data.items.length === 0
              ? (
                <EmptyState icon="search" title="Không có trang phục phù hợp">
                  <p>Thử bỏ bớt bộ lọc hoặc tìm với từ khóa khác.</p>
                </EmptyState>
              )
              : (
                <>
                  <div className="product-grid" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(min(190px, 45%), 1fr))' }}>
                    {data.items.map((o) => <ProductCard key={o.id} offering={o} />)}
                  </div>
                  <Pagination page={data} onChange={(p) => update({ page: String(p) })} />
                </>
              )}
          </QueryState>
        </div>
      </div>
    </div>
  );
}
