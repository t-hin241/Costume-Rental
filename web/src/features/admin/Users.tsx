import { keepPreviousData, useQuery } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { DataTable, type Column } from '../../components/DataTable';
import { Modal, QueryState, ReasonDialog, useAction } from '../../components/feedback';
import { Badge, Button, Card, Checkbox, DefinitionList, EmptyState, Field, Input, Notice, PageHeader, Pagination, Select, Skeleton, StatusBadge } from '../../components/ui';
import { ApiError } from '../../lib/api/client';
import { accessApi, adminApi } from '../../lib/api/endpoints';
import type { AdminUser, Role } from '../../lib/api/types';
import { useAuth } from '../../lib/auth';
import { formatDateTime } from '../../lib/format';
import { useDebounced } from '../../lib/hooks';
import { allRoles, roleLabel, userStatus } from '../../lib/labels';

const pageSize = 20;

export default function Users() {
  const [q, setQ] = useState('');
  const [role, setRole] = useState('');
  const [page, setPage] = useState(1);
  const [openId, setOpenId] = useState<string | null>(null);
  const term = useDebounced(q);

  const result = useQuery({
    queryKey: ['admin', 'users', { term, role, page }],
    queryFn: () => adminApi.users({ q: term, role, page, page_size: pageSize }),
    placeholderData: keepPreviousData,
  });
  const listMissing = result.error instanceof ApiError && result.error.endpointMissing;

  const columns: Column<AdminUser>[] = [
    {
      key: 'user', header: 'Người dùng',
      render: (u) => (
        <span className="user-cell">
          <span className="avatar" aria-hidden="true">{(u.full_name || u.email).slice(0, 1).toUpperCase()}</span>
          <span>
            <button type="button" className="cell-title" style={{ background: 'none', border: 0, padding: 0 }} onClick={() => setOpenId(u.id)}>{u.full_name || u.email}</button>
            <span className="cell-sub">{u.email}</span>
          </span>
        </span>
      ),
    },
    { key: 'roles', header: 'Vai trò', render: (u) => <span className="row" style={{ gap: 4 }}>{u.roles.map((r) => <Badge key={r} label={roleLabel[r]} />)}</span> },
    { key: 'mfa', header: 'Xác thực 2 bước', render: (u) => (u.mfa_enabled ? 'Đã bật' : 'Chưa bật') },
    { key: 'seen', header: 'Hoạt động gần nhất', render: (u) => (u.last_seen_at ? formatDateTime(u.last_seen_at) : '') },
    { key: 'status', header: 'Trạng thái', render: (u) => <StatusBadge status={userStatus[u.status]} /> },
  ];

  return (
    <>
      <PageHeader title="Người dùng" subtitle="Vai trò, trạng thái tài khoản và phân quyền nhân viên." />
      {listMissing ? <LookupById onOpen={setOpenId} /> : (
        <>
          <div className="toolbar">
            <Field label="Tìm kiếm">
              <Input type="search" placeholder="Tên, email" value={q} onChange={(e) => { setQ(e.target.value); setPage(1); }} />
            </Field>
            <Field label="Vai trò">
              <Select value={role} onChange={(e) => { setRole(e.target.value); setPage(1); }}>
                <option value="">Tất cả</option>
                {allRoles.map((r) => <option key={r} value={r}>{roleLabel[r]}</option>)}
              </Select>
            </Field>
          </div>
          <div className="card">
            <QueryState query={result} skeleton={<Skeleton height={320} />}>
              {(data) => data.items.length === 0
                ? <EmptyState icon="users" title="Không tìm thấy người dùng" />
                : <DataTable rows={data.items} columns={columns} rowKey={(u) => u.id} caption="Danh sách người dùng" />}
            </QueryState>
          </div>
          {result.data && <Pagination page={result.data} onChange={setPage} />}
        </>
      )}
      {openId && <UserDialog id={openId} onClose={() => setOpenId(null)} />}
    </>
  );
}

/** Works today: the backend can read one user by ID, but cannot list them yet. */
function LookupById({ onOpen }: { onOpen: (id: string) => void }) {
  const [id, setId] = useState('');
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (id.trim()) onOpen(id.trim());
  };
  return (
    <Card title="Tra cứu theo mã người dùng">
      <form className="stack" style={{ maxWidth: 460 }} onSubmit={submit}>
        <Notice tone="info">Danh sách người dùng chưa có API. Hiện tra cứu được theo mã người dùng.</Notice>
        <Field label="Mã người dùng" required><Input value={id} maxLength={64} onChange={(e) => setId(e.target.value)} /></Field>
        <div><Button type="submit" disabled={!id.trim()}>Tra cứu</Button></div>
      </form>
    </Card>
  );
}

type Pending =
  | { kind: 'grant' | 'revoke'; role: Role }
  | { kind: 'lock' | 'unlock' | 'mfa' }
  | null;

function pendingTitle(p: Exclude<Pending, null>): string {
  switch (p.kind) {
    case 'grant': return `Cấp vai trò ${roleLabel[p.role]}`;
    case 'revoke': return `Thu vai trò ${roleLabel[p.role]}`;
    case 'lock': return 'Khóa tài khoản';
    case 'unlock': return 'Mở khóa tài khoản';
    case 'mfa': return 'Đặt lại xác thực hai bước';
  }
}

function UserDialog({ id, onClose }: { id: string; onClose: () => void }) {
  const { session, can } = useAuth();
  const user = useQuery({ queryKey: ['admin', 'user', id], queryFn: () => accessApi.user(id) });
  const [pending, setPending] = useState<Pending>(null);
  const self = session?.user_id === id;

  const apply = useAction((reason: string) => {
    if (!pending) return Promise.resolve();
    switch (pending.kind) {
      case 'grant': return accessApi.grantRole(id, pending.role, reason);
      case 'revoke': return accessApi.revokeRole(id, pending.role, reason);
      case 'lock': return accessApi.lock(id, reason);
      case 'unlock': return accessApi.unlock(id, reason);
      case 'mfa': return accessApi.resetMFA(id, reason);
    }
  }, {
    inline: true,
    success: 'Đã cập nhật. Các phiên đăng nhập của người dùng đã được làm mới.',
    invalidate: [['admin', 'users'], ['admin', 'user', id]],
    onSuccess: () => setPending(null),
  });

  return (
    <Modal open title="Tài khoản người dùng" onClose={onClose}>
      <QueryState query={user} skeleton={<Skeleton height={220} />}>
        {(u) => (
          <div className="stack">
            <DefinitionList items={[
              { label: 'Email', value: u.email },
              { label: 'Trạng thái', value: <StatusBadge status={userStatus[u.status]} /> },
              { label: 'Tạo lúc', value: formatDateTime(u.created_at) },
              { label: 'Mã người dùng', value: <span className="mono small">{u.id}</span> },
            ]} />
            <fieldset className="stack-sm" style={{ border: 0, padding: 0, margin: 0 }}>
              <legend className="field-label">Vai trò</legend>
              {self && <p className="small muted">Bạn không thể tự đổi vai trò của mình.</p>}
              {allRoles.map((r) => (
                <Checkbox key={r} label={roleLabel[r]} checked={u.roles.includes(r)} disabled={self || !can('user.role_manage')}
                  onChange={(e) => setPending({ kind: e.target.checked ? 'grant' : 'revoke', role: r })} />
              ))}
            </fieldset>
            {can('user.lock') && !self && (
              <div className="action-bar">
                {u.status === 'ACTIVE'
                  ? <Button variant="danger" size="sm" onClick={() => setPending({ kind: 'lock' })}>Khóa tài khoản</Button>
                  : <Button variant="outline" size="sm" onClick={() => setPending({ kind: 'unlock' })}>Mở khóa</Button>}
                <Button variant="outline" size="sm" onClick={() => setPending({ kind: 'mfa' })}>Đặt lại xác thực hai bước</Button>
              </div>
            )}
            {pending && (
              <ReasonDialog open title={pendingTitle(pending)} confirmLabel="Xác nhận" danger={pending.kind === 'lock'} onClose={() => setPending(null)}
                pending={apply.isPending} error={apply.error} onConfirm={(reason) => apply.mutate(reason)} />
            )}
          </div>
        )}
      </QueryState>
    </Modal>
  );
}
