import { useQuery } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { QueryState, useAction } from '../../components/feedback';
import { Button, Card, Field, FormError, Input, PageHeader, Skeleton, fieldError } from '../../components/ui';
import { accountApi } from '../../lib/api/endpoints';
import type { Profile as ProfileData } from '../../lib/api/types';

export default function Profile() {
  const profile = useQuery({ queryKey: ['profile'], queryFn: accountApi.profile });
  return (
    <>
      <PageHeader title="Hồ sơ" subtitle="Thông tin dùng để liên hệ khi bàn giao và hỗ trợ đơn thuê." />
      <QueryState query={profile} skeleton={<Skeleton height={240} />}>
        {(p) => <ProfileForm profile={p} />}
      </QueryState>
    </>
  );
}

function ProfileForm({ profile }: { profile: ProfileData }) {
  const [fullName, setFullName] = useState(profile.full_name);
  const [phone, setPhone] = useState(profile.phone);
  const save = useAction(() => accountApi.saveProfile({ full_name: fullName.trim(), phone: phone.trim() }), {
    inline: true, success: 'Đã lưu hồ sơ.', invalidate: [['profile']],
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate(undefined);
  };
  return (
    <Card>
      <form className="stack" style={{ maxWidth: 460 }} onSubmit={submit}>
        <FormError error={save.error} inlineFields />
        <Field label="Email" hint="Email đăng nhập không đổi ở đây."><Input value={profile.email} disabled /></Field>
        <Field label="Họ và tên" required error={fieldError(save.error, 'full_name')}>
          <Input autoComplete="name" value={fullName} onChange={(e) => setFullName(e.target.value)} required maxLength={120} />
        </Field>
        <Field label="Số điện thoại" required error={fieldError(save.error, 'phone')}>
          <Input type="tel" autoComplete="tel" value={phone} onChange={(e) => setPhone(e.target.value)} required maxLength={20} />
        </Field>
        <div><Button type="submit" loading={save.isPending}>Lưu thay đổi</Button></div>
      </form>
    </Card>
  );
}
