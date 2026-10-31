import { useQuery } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { DataTable, type Column } from '../../components/DataTable';
import { Icon } from '../../components/Icon';
import { Modal, QueryState, useAction } from '../../components/feedback';
import { Button, Card, Checkbox, EmptyState, Field, FormError, Input, PageHeader, Select, Skeleton, StatusBadge, Tabs, Textarea, fieldError } from '../../components/ui';
import { adminApi, catalogApi, type BundleInput, type ItemInput } from '../../lib/api/endpoints';
import type { AdminBundle, AdminBundleComponent, AdminItem, CatalogStatus, Category, Image, Money, RentalPackage, Variant } from '../../lib/api/types';
import { formatHours, formatMoney } from '../../lib/format';
import { catalogStatus } from '../../lib/labels';

type Tab = 'items' | 'bundles' | 'categories' | 'packages';

const vnd = (amount: number): Money => ({ amount, currency: 'VND' });
const invalidate = [['admin', 'catalog'], ['offerings'], ['categories'], ['packages']];

export default function CatalogAdmin() {
  const [tab, setTab] = useState<Tab>('items');
  const items = useQuery({ queryKey: ['admin', 'catalog', 'items'], queryFn: adminApi.items });
  const bundles = useQuery({ queryKey: ['admin', 'catalog', 'bundles'], queryFn: adminApi.bundles });
  const categories = useQuery({ queryKey: ['categories'], queryFn: catalogApi.categories });
  const packages = useQuery({ queryKey: ['packages'], queryFn: catalogApi.packages });

  return (
    <>
      <PageHeader title="Sản phẩm" subtitle="Danh mục, món và SKU, bộ trang phục, gói thuê. Đơn đã đặt giữ nguyên giá lúc đặt." />
      <div className="stack">
        <Tabs label="Loại dữ liệu" value={tab} onChange={setTab} options={[
          { value: 'items', label: 'Món và SKU', count: items.data?.length },
          { value: 'bundles', label: 'Bộ trang phục', count: bundles.data?.length },
          { value: 'categories', label: 'Danh mục', count: categories.data?.length },
          { value: 'packages', label: 'Gói thuê', count: packages.data?.length },
        ]} />
        {tab === 'items' && (
          <QueryState query={items} skeleton={<Skeleton height={300} />}>
            {(rows) => <ItemsTab rows={rows} categories={categories.data ?? []} packages={packages.data ?? []} />}
          </QueryState>
        )}
        {tab === 'bundles' && (
          <QueryState query={bundles} skeleton={<Skeleton height={300} />}>
            {(rows) => <BundlesTab rows={rows} items={items.data ?? []} categories={categories.data ?? []} packages={packages.data ?? []} />}
          </QueryState>
        )}
        {tab === 'categories' && (
          <QueryState query={categories} skeleton={<Skeleton height={200} />}>{(rows) => <CategoriesTab rows={rows} />}</QueryState>
        )}
        {tab === 'packages' && (
          <QueryState query={packages} skeleton={<Skeleton height={200} />}>{(rows) => <PackagesTab rows={rows} />}</QueryState>
        )}
      </div>
    </>
  );
}

function TabCard({ title, onAdd, addLabel, children, empty }: { title: string; onAdd: () => void; addLabel: string; children: React.ReactNode; empty: boolean }) {
  return (
    <Card title={title} flush actions={<Button size="sm" icon="plus" onClick={onAdd}>{addLabel}</Button>}>
      {empty ? <EmptyState icon="shirt" title="Chưa có dữ liệu" /> : children}
    </Card>
  );
}

function StatusSelect({ value, onChange }: { value: CatalogStatus; onChange: (v: CatalogStatus) => void }) {
  return (
    <Field label="Trạng thái" required>
      <Select value={value} onChange={(e) => onChange(e.target.value as CatalogStatus)}>
        {Object.entries(catalogStatus).map(([v, s]) => <option key={v} value={v}>{s.label}</option>)}
      </Select>
    </Field>
  );
}

function PricesEditor({ packages, prices, onChange }: {
  packages: RentalPackage[]; prices: Record<string, string>; onChange: (p: Record<string, string>) => void;
}) {
  return (
    <fieldset className="stack-sm" style={{ border: 0, padding: 0, margin: 0 }}>
      <legend className="field-label">Giá thuê theo gói (VND)</legend>
      <div className="form-grid">
        {packages.map((p) => (
          <Field key={p.id} label={p.label}>
            <Input type="number" min={0} step={1} inputMode="numeric" value={prices[p.id] ?? ''} onChange={(e) => onChange({ ...prices, [p.id]: e.target.value })} />
          </Field>
        ))}
      </div>
    </fieldset>
  );
}

function ImagesEditor({ images, onChange }: { images: Image[]; onChange: (i: Image[]) => void }) {
  const set = (i: number, patch: Partial<Image>) => onChange(images.map((img, n) => (n === i ? { ...img, ...patch } : img)));
  return (
    <fieldset className="stack-sm" style={{ border: 0, padding: 0, margin: 0 }}>
      <legend className="field-label">Ảnh sản phẩm</legend>
      {images.map((img, i) => (
        <div key={i} className="row" style={{ flexWrap: 'nowrap' }}>
          <Input aria-label={`Đường dẫn ảnh ${i + 1}`} placeholder="Đường dẫn ảnh" value={img.url} onChange={(e) => set(i, { url: e.target.value })} />
          <Input aria-label={`Mô tả ảnh ${i + 1}`} placeholder="Mô tả ảnh" value={img.alt} onChange={(e) => set(i, { alt: e.target.value })} />
          <button type="button" className="icon-button" aria-label={`Xóa ảnh ${i + 1}`} onClick={() => onChange(images.filter((_, n) => n !== i))}><Icon name="trash" size={18} /></button>
        </div>
      ))}
      <div><Button variant="ghost" size="sm" icon="plus" onClick={() => onChange([...images, { url: '', alt: '' }])}>Thêm ảnh</Button></div>
    </fieldset>
  );
}

function pricesToForm(prices: { package_id: string; amount: Money }[]): Record<string, string> {
  return Object.fromEntries(prices.map((p) => [p.package_id, String(p.amount.amount)]));
}

function pricesFromForm(prices: Record<string, string>): { package_id: string; amount: Money }[] {
  return Object.entries(prices).filter(([, v]) => Number(v) > 0).map(([package_id, v]) => ({ package_id, amount: vnd(Number(v)) }));
}

// ---- Items ----

function ItemsTab({ rows, categories, packages }: { rows: AdminItem[]; categories: Category[]; packages: RentalPackage[] }) {
  const [editing, setEditing] = useState<AdminItem | 'new' | null>(null);
  const columns: Column<AdminItem>[] = [
    { key: 'name', header: 'Món', render: (r) => <button type="button" className="cell-title" style={{ background: 'none', border: 0, padding: 0, textAlign: 'left' }} onClick={() => setEditing(r)}>{r.name}</button> },
    { key: 'variants', header: 'SKU', render: (r) => <span>{r.variants.length} biến thể<span className="cell-sub">{r.variants.map((v) => v.size).join(', ')}</span></span> },
    { key: 'assets', header: 'Tài sản', align: 'right', render: (r) => <span className="tnum">{r.asset_count}</span> },
    { key: 'deposit', header: 'Bảo đảm', align: 'right', render: (r) => <span className="tnum">{formatMoney(r.security_deposit)}</span> },
    { key: 'status', header: 'Trạng thái', render: (r) => <StatusBadge status={catalogStatus[r.status]} /> },
  ];
  return (
    <>
      <TabCard title="Món và SKU" addLabel="Thêm món" onAdd={() => setEditing('new')} empty={rows.length === 0}>
        <DataTable rows={rows} columns={columns} rowKey={(r) => r.id} caption="Danh sách món" />
      </TabCard>
      {editing && <ItemDialog item={editing === 'new' ? null : editing} categories={categories} packages={packages} onClose={() => setEditing(null)} />}
    </>
  );
}

const newVariant = (): Variant => ({ id: '', sku: '', size: '', color: '', status: 'ACTIVE' });

function ItemDialog({ item, categories, packages, onClose }: { item: AdminItem | null; categories: Category[]; packages: RentalPackage[]; onClose: () => void }) {
  const [form, setForm] = useState({
    name: item?.name ?? '', slug: item?.slug ?? '', category_id: item?.category_id ?? '', description: item?.description ?? '',
    status: item?.status ?? 'DRAFT' as CatalogStatus, individually_rentable: item?.individually_rentable ?? true,
    security_deposit: item ? String(item.security_deposit.amount) : '', daily_rate: item ? String(item.daily_rate.amount) : '', buffer_class: item?.buffer_class ?? '',
  });
  const [images, setImages] = useState<Image[]>(item?.images ?? []);
  const [variants, setVariants] = useState<Variant[]>(item?.variants ?? [newVariant()]);
  const [prices, setPrices] = useState(pricesToForm(item?.prices ?? []));
  const set = (patch: Partial<typeof form>) => setForm({ ...form, ...patch });
  const setVariant = (i: number, patch: Partial<Variant>) => setVariants(variants.map((v, n) => (n === i ? { ...v, ...patch } : v)));

  const save = useAction(() => {
    const body: ItemInput = {
      ...form, category_id: form.category_id || null, images: images.filter((i) => i.url.trim()), variants, prices: pricesFromForm(prices),
      security_deposit: vnd(Number(form.security_deposit)), daily_rate: vnd(Number(form.daily_rate)),
    };
    return adminApi.saveItem(item?.id ?? null, body);
  }, { inline: true, success: 'Đã lưu món.', invalidate, onSuccess: onClose });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(undefined);
  };

  return (
    <Modal open wide title={item ? `Sửa ${item.name}` : 'Thêm món'} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <div className="form-grid">
          <Field label="Tên món" required error={fieldError(save.error, 'name')}><Input value={form.name} required maxLength={200} onChange={(e) => set({ name: e.target.value })} /></Field>
          <Field label="Đường dẫn (slug)" required hint="Chữ thường, số, dấu gạch ngang." error={fieldError(save.error, 'slug')}>
            <Input value={form.slug} required maxLength={120} pattern="[a-z0-9]+(-[a-z0-9]+)*" onChange={(e) => set({ slug: e.target.value })} />
          </Field>
          <Field label="Danh mục">
            <Select value={form.category_id} onChange={(e) => set({ category_id: e.target.value })}>
              <option value="">Không có</option>
              {categories.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </Select>
          </Field>
          <StatusSelect value={form.status} onChange={(status) => set({ status })} />
          <Field label="Tiền bảo đảm mỗi món (VND)" required><Input type="number" min={0} step={1} value={form.security_deposit} required onChange={(e) => set({ security_deposit: e.target.value })} /></Field>
          <Field label="Giá thuê/ngày (VND)" required hint="Cơ sở tính phí trả trễ."><Input type="number" min={1} step={1} value={form.daily_rate} required onChange={(e) => set({ daily_rate: e.target.value })} /></Field>
          <Field label="Nhóm buffer" required hint="Khớp nhóm trong chính sách."><Input value={form.buffer_class} required onChange={(e) => set({ buffer_class: e.target.value })} /></Field>
          <div className="row"><Checkbox label="Cho thuê lẻ" checked={form.individually_rentable} onChange={(e) => set({ individually_rentable: e.target.checked })} /></div>
          <div className="full"><Field label="Mô tả"><Textarea value={form.description} onChange={(e) => set({ description: e.target.value })} /></Field></div>
        </div>
        <ImagesEditor images={images} onChange={setImages} />
        <fieldset className="stack-sm" style={{ border: 0, padding: 0, margin: 0 }}>
          <legend className="field-label">Biến thể (size, màu)</legend>
          {variants.map((v, i) => (
            <div key={i} className="row" style={{ flexWrap: 'nowrap' }}>
              <Input aria-label="SKU" placeholder="SKU" value={v.sku} required onChange={(e) => setVariant(i, { sku: e.target.value })} />
              <Input aria-label="Size" placeholder="Size" value={v.size} required onChange={(e) => setVariant(i, { size: e.target.value })} />
              <Input aria-label="Màu" placeholder="Màu" value={v.color} onChange={(e) => setVariant(i, { color: e.target.value })} />
              <button type="button" className="icon-button" aria-label="Xóa biến thể" disabled={variants.length === 1} onClick={() => setVariants(variants.filter((_, n) => n !== i))}><Icon name="trash" size={18} /></button>
            </div>
          ))}
          <div><Button variant="ghost" size="sm" icon="plus" onClick={() => setVariants([...variants, newVariant()])}>Thêm biến thể</Button></div>
        </fieldset>
        <PricesEditor packages={packages} prices={prices} onChange={setPrices} />
        <FormError error={save.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={save.isPending}>Lưu</Button>
        </div>
      </form>
    </Modal>
  );
}

// ---- Bundles ----

function BundlesTab({ rows, items, categories, packages }: { rows: AdminBundle[]; items: AdminItem[]; categories: Category[]; packages: RentalPackage[] }) {
  const [editing, setEditing] = useState<AdminBundle | 'new' | null>(null);
  const columns: Column<AdminBundle>[] = [
    { key: 'name', header: 'Bộ', render: (r) => <button type="button" className="cell-title" style={{ background: 'none', border: 0, padding: 0, textAlign: 'left' }} onClick={() => setEditing(r)}>{r.name}</button> },
    { key: 'components', header: 'Thành phần', render: (r) => r.components.map((c) => `${c.item_name} × ${c.quantity}`).join(', ') },
    { key: 'deposit', header: 'Bảo đảm', render: (r) => (r.deposit_override ? <span className="tnum">{formatMoney(r.deposit_override)}</span> : <span className="muted small">Theo thành phần</span>) },
    { key: 'status', header: 'Trạng thái', render: (r) => <StatusBadge status={catalogStatus[r.status]} /> },
  ];
  return (
    <>
      <TabCard title="Bộ trang phục" addLabel="Thêm bộ" onAdd={() => setEditing('new')} empty={rows.length === 0}>
        <DataTable rows={rows} columns={columns} rowKey={(r) => r.id} caption="Danh sách bộ trang phục" />
      </TabCard>
      {editing && <BundleDialog bundle={editing === 'new' ? null : editing} items={items} categories={categories} packages={packages} onClose={() => setEditing(null)} />}
    </>
  );
}

function BundleDialog({ bundle, items, categories, packages, onClose }: { bundle: AdminBundle | null; items: AdminItem[]; categories: Category[]; packages: RentalPackage[]; onClose: () => void }) {
  const [form, setForm] = useState({
    name: bundle?.name ?? '', slug: bundle?.slug ?? '', category_id: bundle?.category_id ?? '', description: bundle?.description ?? '',
    status: bundle?.status ?? 'DRAFT' as CatalogStatus, deposit_override: bundle?.deposit_override ? String(bundle.deposit_override.amount) : '',
  });
  const [images, setImages] = useState<Image[]>(bundle?.images ?? []);
  const [components, setComponents] = useState<AdminBundleComponent[]>(bundle?.components ?? []);
  const [prices, setPrices] = useState(pricesToForm(bundle?.prices ?? []));
  const set = (patch: Partial<typeof form>) => setForm({ ...form, ...patch });
  const setComponent = (i: number, patch: Partial<AdminBundleComponent>) => setComponents(components.map((c, n) => (n === i ? { ...c, ...patch } : c)));

  const save = useAction(() => {
    const body: BundleInput = {
      ...form, category_id: form.category_id || null, images: images.filter((i) => i.url.trim()), components, prices: pricesFromForm(prices),
      deposit_override: form.deposit_override === '' ? null : vnd(Number(form.deposit_override)),
    };
    return adminApi.saveBundle(bundle?.id ?? null, body);
  }, { inline: true, success: 'Đã lưu bộ trang phục.', invalidate, onSuccess: onClose });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(undefined);
  };

  return (
    <Modal open wide title={bundle ? `Sửa ${bundle.name}` : 'Thêm bộ trang phục'} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <div className="form-grid">
          <Field label="Tên bộ" required><Input value={form.name} required maxLength={200} onChange={(e) => set({ name: e.target.value })} /></Field>
          <Field label="Đường dẫn (slug)" required><Input value={form.slug} required maxLength={120} pattern="[a-z0-9]+(-[a-z0-9]+)*" onChange={(e) => set({ slug: e.target.value })} /></Field>
          <Field label="Danh mục">
            <Select value={form.category_id} onChange={(e) => set({ category_id: e.target.value })}>
              <option value="">Không có</option>
              {categories.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
            </Select>
          </Field>
          <StatusSelect value={form.status} onChange={(status) => set({ status })} />
          <Field label="Tiền bảo đảm riêng cho bộ (VND)" hint="Để trống thì tính theo từng thành phần. Không thu cả hai.">
            <Input type="number" min={0} step={1} value={form.deposit_override} onChange={(e) => set({ deposit_override: e.target.value })} />
          </Field>
          <div className="full"><Field label="Mô tả"><Textarea value={form.description} onChange={(e) => set({ description: e.target.value })} /></Field></div>
        </div>
        <ImagesEditor images={images} onChange={setImages} />
        <fieldset className="stack" style={{ border: 0, padding: 0, margin: 0 }}>
          <legend className="field-label">Thành phần (dùng chung kho với món lẻ)</legend>
          {components.map((c, i) => {
            const item = items.find((it) => it.id === c.item_id);
            return (
              <div key={i} className="stack-sm" style={{ padding: 10, background: 'var(--canvas)', borderRadius: 8 }}>
                <div className="row" style={{ flexWrap: 'nowrap' }}>
                  <Select aria-label="Món" value={c.item_id} onChange={(e) => {
                    const next = items.find((it) => it.id === e.target.value);
                    setComponent(i, { item_id: e.target.value, item_name: next?.name ?? '', options: [] });
                  }}>
                    <option value="">Chọn món</option>
                    {items.map((it) => <option key={it.id} value={it.id}>{it.name}</option>)}
                  </Select>
                  <Input aria-label="Số lượng" type="number" min={1} step={1} style={{ maxWidth: 80 }} value={c.quantity} onChange={(e) => setComponent(i, { quantity: Number(e.target.value) })} />
                  <button type="button" className="icon-button" aria-label="Xóa thành phần" onClick={() => setComponents(components.filter((_, n) => n !== i))}><Icon name="trash" size={18} /></button>
                </div>
                <div className="row">
                  {item?.variants.map((v) => (
                    <Checkbox key={v.id} label={`${v.size} ${v.color}`} checked={c.options.includes(v.id)}
                      onChange={(e) => setComponent(i, { options: e.target.checked ? [...c.options, v.id] : c.options.filter((o) => o !== v.id) })} />
                  ))}
                </div>
              </div>
            );
          })}
          <div><Button variant="ghost" size="sm" icon="plus" onClick={() => setComponents([...components, { key: `c${Date.now()}`, item_id: '', item_name: '', quantity: 1, options: [] }])}>Thêm thành phần</Button></div>
        </fieldset>
        <PricesEditor packages={packages} prices={prices} onChange={setPrices} />
        <FormError error={save.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={save.isPending}>Lưu</Button>
        </div>
      </form>
    </Modal>
  );
}

// ---- Categories and packages ----

function CategoriesTab({ rows }: { rows: Category[] }) {
  const [editing, setEditing] = useState<Category | 'new' | null>(null);
  return (
    <>
      <TabCard title="Danh mục" addLabel="Thêm danh mục" onAdd={() => setEditing('new')} empty={rows.length === 0}>
        <DataTable rows={rows} rowKey={(r) => r.id} caption="Danh sách danh mục" columns={[
          { key: 'name', header: 'Tên', render: (r) => <button type="button" className="cell-title" style={{ background: 'none', border: 0, padding: 0 }} onClick={() => setEditing(r)}>{r.name}</button> },
          { key: 'slug', header: 'Đường dẫn', render: (r) => <span className="mono">{r.slug}</span> },
          { key: 'count', header: 'Số mẫu', align: 'right', render: (r) => <span className="tnum">{r.offering_count}</span> },
        ]} />
      </TabCard>
      {editing && <CategoryDialog category={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
    </>
  );
}

function CategoryDialog({ category, onClose }: { category: Category | null; onClose: () => void }) {
  const [name, setName] = useState(category?.name ?? '');
  const [slug, setSlug] = useState(category?.slug ?? '');
  const save = useAction(() => adminApi.saveCategory(category?.id ?? null, { name: name.trim(), slug: slug.trim() }), {
    inline: true, success: 'Đã lưu danh mục.', invalidate, onSuccess: onClose,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(undefined);
  };
  return (
    <Modal open title={category ? 'Sửa danh mục' : 'Thêm danh mục'} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <Field label="Tên" required><Input value={name} required maxLength={120} onChange={(e) => setName(e.target.value)} /></Field>
        <Field label="Đường dẫn (slug)" required><Input value={slug} required pattern="[a-z0-9]+(-[a-z0-9]+)*" onChange={(e) => setSlug(e.target.value)} /></Field>
        <FormError error={save.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={save.isPending}>Lưu</Button>
        </div>
      </form>
    </Modal>
  );
}

function PackagesTab({ rows }: { rows: RentalPackage[] }) {
  const [editing, setEditing] = useState<RentalPackage | 'new' | null>(null);
  return (
    <>
      <TabCard title="Gói thuê" addLabel="Thêm gói" onAdd={() => setEditing('new')} empty={rows.length === 0}>
        <DataTable rows={rows} rowKey={(r) => r.id} caption="Danh sách gói thuê" columns={[
          { key: 'label', header: 'Tên gói', render: (r) => <button type="button" className="cell-title" style={{ background: 'none', border: 0, padding: 0 }} onClick={() => setEditing(r)}>{r.label}</button> },
          { key: 'code', header: 'Mã', render: (r) => <span className="mono">{r.code}</span> },
          { key: 'duration', header: 'Thời lượng', render: (r) => formatHours(r.duration_hours) },
          { key: 'status', header: 'Trạng thái', render: (r) => <StatusBadge status={catalogStatus[r.status]} /> },
        ]} />
      </TabCard>
      {editing && <PackageDialog pkg={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
    </>
  );
}

function PackageDialog({ pkg, onClose }: { pkg: RentalPackage | null; onClose: () => void }) {
  const [code, setCode] = useState(pkg?.code ?? '');
  const [label, setLabel] = useState(pkg?.label ?? '');
  const [hours, setHours] = useState(pkg ? String(pkg.duration_hours) : '');
  const [status, setStatus] = useState<CatalogStatus>(pkg?.status ?? 'DRAFT');
  const save = useAction(() => adminApi.savePackage(pkg?.id ?? null, { code: code.trim(), label: label.trim(), duration_hours: Number(hours), status }), {
    inline: true, success: 'Đã lưu gói thuê.', invalidate, onSuccess: onClose,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(undefined);
  };
  return (
    <Modal open title={pkg ? 'Sửa gói thuê' : 'Thêm gói thuê'} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <Field label="Mã" required><Input value={code} required onChange={(e) => setCode(e.target.value)} /></Field>
        <Field label="Tên hiển thị" required><Input value={label} required onChange={(e) => setLabel(e.target.value)} /></Field>
        <Field label="Thời lượng (giờ)" required hint="Thời lượng là định nghĩa của gói, tên chỉ để hiển thị.">
          <Input type="number" min={1} step={1} value={hours} required onChange={(e) => setHours(e.target.value)} />
        </Field>
        <StatusSelect value={status} onChange={setStatus} />
        <FormError error={save.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={save.isPending}>Lưu</Button>
        </div>
      </form>
    </Modal>
  );
}
