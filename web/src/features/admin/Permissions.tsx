import { useQuery } from '@tanstack/react-query';
import { Fragment, useMemo, useState, type FormEvent } from 'react';
import { Modal, QueryState, useAction } from '../../components/feedback';
import { Button, Card, Field, FormError, Input, Notice, PageHeader, Skeleton, Textarea } from '../../components/ui';
import { accessApi } from '../../lib/api/endpoints';
import type { PermissionMatrix, Role } from '../../lib/api/types';
import { allRoles, permissionGroups, roleLabel } from '../../lib/labels';

export default function Permissions() {
  const matrix = useQuery({ queryKey: ['admin', 'matrix'], queryFn: accessApi.matrix });
  return (
    <>
      <PageHeader title="Phân quyền" subtitle="Ma trận quyền có phiên bản. Mọi thay đổi có hiệu lực ngay và được ghi vào lịch sử thao tác." />
      <QueryState query={matrix} skeleton={<Skeleton height={420} />}>
        {(m) => <Editor key={m.version} current={m} />}
      </QueryState>
    </>
  );
}

function Editor({ current }: { current: PermissionMatrix }) {
  const [draft, setDraft] = useState(current.matrix);
  const [publishing, setPublishing] = useState(false);

  // Any permission the backend knows but this screen does not is still shown.
  const groups = useMemo(() => {
    const known = new Set(permissionGroups.flatMap((g) => Object.keys(g.items)));
    const other = [...new Set(Object.values(current.matrix).flat())].filter((p) => !known.has(p));
    return other.length > 0
      ? [...permissionGroups, { title: 'Khác', items: Object.fromEntries(other.map((p) => [p, p])) }]
      : permissionGroups;
  }, [current.matrix]);

  const toggle = (role: Role, perm: string, on: boolean) => {
    setDraft({ ...draft, [role]: on ? [...draft[role], perm] : draft[role].filter((p) => p !== perm) });
  };

  const changes = allRoles.reduce((n, r) => {
    const before = new Set(current.matrix[r]);
    const after = new Set(draft[r]);
    return n + [...before].filter((p) => !after.has(p)).length + [...after].filter((p) => !before.has(p)).length;
  }, 0);

  return (
    <div className="stack">
      <Card title={<>Phiên bản đang dùng: <span className="mono">{current.version}</span></>}
        actions={<Button size="sm" disabled={changes === 0} onClick={() => setPublishing(true)}>Xuất bản ({changes} thay đổi)</Button>} flush>
        <div className="matrix-wrap">
          <table className="table matrix">
            <caption className="sr-only">Ma trận quyền theo vai trò</caption>
            <thead>
              <tr><th scope="col">Quyền</th>{allRoles.map((r) => <th key={r} scope="col">{roleLabel[r]}</th>)}</tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <Fragment key={g.title}>
                  <tr className="group"><td colSpan={allRoles.length + 1}>{g.title}</td></tr>
                  {Object.entries(g.items).map(([perm, label]) => (
                    <tr key={perm}>
                      <td>{label}<span className="cell-sub mono">{perm}</span></td>
                      {allRoles.map((r) => (
                        <td key={r}>
                          <input type="checkbox" aria-label={`${label}: ${roleLabel[r]}`} checked={draft[r].includes(perm)} onChange={(e) => toggle(r, perm, e.target.checked)} />
                        </td>
                      ))}
                    </tr>
                  ))}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
      {publishing && <PublishDialog draft={draft} onClose={() => setPublishing(false)} />}
    </div>
  );
}

function PublishDialog({ draft, onClose }: { draft: PermissionMatrix['matrix']; onClose: () => void }) {
  const [version, setVersion] = useState('');
  const [reason, setReason] = useState('');
  const publish = useAction(() => accessApi.publishMatrix({ version: version.trim(), matrix: draft, reason: reason.trim() }), {
    inline: true, success: 'Đã xuất bản ma trận quyền.', invalidate: [['admin', 'matrix'], ['session']], onSuccess: onClose,
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    publish.mutate(undefined);
  };
  return (
    <Modal open title="Xuất bản ma trận quyền" onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <Notice tone="warning">Backend kiểm tra các quy tắc bắt buộc, ví dụ nhân viên không được có quyền duyệt tiền, và từ chối ma trận vi phạm.</Notice>
        <Field label="Mã phiên bản" required hint="Không được trùng phiên bản đã có."><Input value={version} required maxLength={64} onChange={(e) => setVersion(e.target.value)} /></Field>
        <Field label="Lý do" required><Textarea value={reason} required maxLength={500} onChange={(e) => setReason(e.target.value)} /></Field>
        <FormError error={publish.error} />
        <div className="modal-actions">
          <Button variant="ghost" onClick={onClose}>Quay lại</Button>
          <Button type="submit" loading={publish.isPending}>Xuất bản</Button>
        </div>
      </form>
    </Modal>
  );
}
