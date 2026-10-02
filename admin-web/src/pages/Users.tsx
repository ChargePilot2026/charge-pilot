import { useCallback, useEffect, useState } from 'react';
import { Alert, App, Button, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd';
import { KeyOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { apiDelete, apiGet, apiPost, apiPut, adminSession } from '../api/client';
import type { SessionProfile } from '../api/session';
import { formatTime } from '../utils/time';
import { LoadError } from '../components/LoadError';
import { DEFAULT_PAGE_SIZE, TABLE_PAGINATION } from '../utils/tablePagination';

const { Text, Paragraph } = Typography;

interface AdminUser {
  id: number;
  username: string;
  display_name: string | null;
  role_id: number | null;
  role_code: string | null;
  role_name: string | null;
  phone: string | null;
  email: string | null;
  status: string;
  mfa_enabled: boolean;
  last_login_at: string | null;
  locked_until: string | null;
  failed_login_count: number;
}

interface Role { id: number; name: string; code: string }

const statusMeta: Record<string, { color: string; label: string }> = {
  active: { color: 'green', label: '正常' },
  disabled: { color: 'red', label: '已停用' },
  locked: { color: 'orange', label: '已锁定' },
};

export default function UsersPage() {
  const navigate = useNavigate();
  const { message } = App.useApp();
  const [roles, setRoles] = useState<Role[]>([]);
  const [rolesError, setRolesError] = useState<string | null>(null);
  const [rows, setRows] = useState<AdminUser[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState<number>(DEFAULT_PAGE_SIZE);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [creating, setCreating] = useState(false);
  const [editing, setEditing] = useState<AdminUser | null>(null);
  const [resetting, setResetting] = useState<AdminUser | null>(null);
  const [mfa, setMfa] = useState<{ user: AdminUser; secret: string; uri: string; code: string } | null>(null);
  const [saving, setSaving] = useState(false);
  const [createForm] = Form.useForm();
  const [editForm] = Form.useForm();
  const [resetForm] = Form.useForm();
  const [mfaForm] = Form.useForm();
  const isSelf = (row: AdminUser) => {
    try { return JSON.parse(localStorage.getItem('cp_admin') || 'null')?.admin_user_id === row.id; }
    catch { return false; }
  };

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await apiGet<{ items: AdminUser[]; total: number }>(`/api/v1/admin/admin-users?page=${page}&page_size=${pageSize}`);
      setRows(data.items || []);
      setTotal(data.total || 0);
    } catch (e: any) {
      setError(e?.message || '管理员列表读取失败');
    } finally {
      setLoading(false);
    }
  }, [page, pageSize]);

  // 角色列表读不到时不能把下拉留空：空下拉会被理解成「系统里没有角色」，
  // 于是管理员根本建不出来，也看不出是接口问题。
  const loadRoles = useCallback(async () => {
    try {
      setRoles((await apiGet<{ items: Role[] }>('/api/v1/admin/roles')).items || []);
      setRolesError(null);
    } catch (e: any) {
      setRoles([]); setRolesError(e?.message || '角色列表读取失败');
    }
  }, []);

  useEffect(() => { void load(); }, [load]);
  useEffect(() => { void loadRoles(); }, [loadRoles]);

  const create = async () => {
    const values = await createForm.validateFields();
    setSaving(true);
    try {
      await apiPost('/api/v1/admin/admin-users', values);
      message.success('账号已创建');
      setCreating(false);
      createForm.resetFields();
      await load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e?.message || '创建失败');
    } finally {
      setSaving(false);
    }
  };

  const update = async () => {
    if (!editing) return;
    const values = await editForm.validateFields();
    setSaving(true);
    try {
      const result = await apiPut<{ sessions_revoked: boolean }>(`/api/v1/admin/admin-users/${editing.id}`, values);
      message.success(result.sessions_revoked ? '账号已更新，原有会话已失效' : '账号已更新');
      setEditing(null);
      await load();
      void adminSession.syncProfile(() => apiGet<SessionProfile>('/api/v1/admin/auth/me')).catch(() => undefined);
    } catch (e: any) {
      if (!e?.errorFields) message.error(e?.message || '更新失败');
    } finally {
      setSaving(false);
    }
  };

  const remove = async (row: AdminUser) => {
    setSaving(true);
    try {
      await apiDelete(`/api/v1/admin/admin-users/${row.id}`);
      message.success('账号已删除');
      await load();
    } catch (e: any) {
      message.error(e?.message || '删除失败');
    } finally {
      setSaving(false);
    }
  };

  const unlock = async (row: AdminUser) => {
    setSaving(true);
    try {
      await apiPost(`/api/v1/admin/admin-users/${row.id}/unlock`, {});
      message.success('账号已解锁');
      await load();
    } catch (e: any) {
      message.error(e?.message || '解锁失败');
    } finally {
      setSaving(false);
    }
  };

  const resetPassword = async () => {
    if (!resetting) return;
    const values = await resetForm.validateFields();
    setSaving(true);
    try {
      await apiPost(`/api/v1/admin/admin-users/${resetting.id}/reset-password`, { new_password: values.new_password });
      message.success('密码已重置，该账号所有会话已失效');
      setResetting(null);
      resetForm.resetFields();
      await load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e?.message || '重置失败');
    } finally {
      setSaving(false);
    }
  };

  const enrolMFA = async (row: AdminUser) => {
    setSaving(true);
    try {
      const result = await apiPost<{ secret: string; otpauth_uri: string; code: string }>(`/api/v1/admin/admin-users/${row.id}/mfa`, { action: 'enrol' });
      setMfa({ user: row, secret: result.secret, uri: result.otpauth_uri, code: result.code });
      mfaForm.resetFields();
      mfaForm.setFieldsValue({ secret: result.secret, code: result.code });
    } catch (e: any) {
      message.error(e?.message || '生成密钥失败');
    } finally {
      setSaving(false);
    }
  };

  const confirmMFA = async () => {
    if (!mfa) return;
    const values = await mfaForm.validateFields();
    setSaving(true);
    try {
      await apiPost(`/api/v1/admin/admin-users/${mfa.user.id}/mfa`, { action: 'confirm', secret: values.secret, code: values.code });
      message.success('双因素认证已启用');
      setMfa(null);
      await load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e?.message || '确认失败');
    } finally {
      setSaving(false);
    }
  };

  const disableMFA = async (row: AdminUser) => {
    setSaving(true);
    try {
      await apiPost(`/api/v1/admin/admin-users/${row.id}/mfa`, { action: 'disable' });
      message.success('双因素认证已关闭');
      await load();
    } catch (e: any) {
      message.error(e?.message || '关闭失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }} wrap>
        <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreating(true)}>新建账号</Button>
        {error && <LoadError title="管理员列表加载失败" detail={error} onRetry={() => void load()} />}
      </Space>
      <Table<AdminUser> size="middle"
        rowKey="id" loading={loading} dataSource={rows} scroll={{ x: 1100 }}
        pagination={{ ...TABLE_PAGINATION, current: page, pageSize, total, onChange: (nextPage, nextPageSize) => { setPage(nextPageSize === pageSize ? nextPage : 1); setPageSize(nextPageSize); } }}
        columns={[
          { title: '用户名', dataIndex: 'username', width: 160 },
          { title: '显示名', dataIndex: 'display_name', width: 130, render: (v: string | null) => v || '—' },
          { title: '角色', dataIndex: 'role_code', width: 260, render: (v: string | null, row) => row.role_name || v
            ? <Tag>{row.role_name && v ? `${row.role_name}（${v}）` : row.role_name || v}</Tag> : `ID ${row.role_id ?? '—'}` },
          { title: '状态', dataIndex: 'status', width: 100, render: (v: string) => <Tag color={statusMeta[v]?.color}>{statusMeta[v]?.label || v}</Tag> },
          { title: '双因素', dataIndex: 'mfa_enabled', width: 100, render: (v: boolean) => v ? <Tag color="green">已启用</Tag> : <Tag>未启用</Tag> },
          { title: '最近登录', dataIndex: 'last_login_at', width: 170, render: (v: string | null) => v ? formatTime(v) : '从未登录' },
          { title: '失败次数', dataIndex: 'failed_login_count', width: 100, render: (v: number) => (v > 0 ? <Text type="danger">{v}</Text> : 0) },
          {
            title: '操作', fixed: 'right', width: 280, render: (_, row) => (<Space size={0}>
              <Button type="link" onClick={() => { setEditing(row); editForm.setFieldsValue(row); }}>编辑</Button>
              <Button type="link" onClick={() => { setResetting(row); resetForm.resetFields(); }}>重置密码</Button>
              {row.status === 'locked'
                ? <Button type="link" onClick={() => void unlock(row)} loading={saving}>解锁</Button>
                : <Button type="link" icon={<KeyOutlined />} onClick={() => isSelf(row) ? navigate('/security') : void enrolMFA(row)} loading={saving}>MFA</Button>}
              {row.mfa_enabled && <Button type="link" danger onClick={() => isSelf(row) ? navigate('/security') : void disableMFA(row)} loading={saving}>关闭 MFA</Button>}
              <Popconfirm title={`删除账号 ${row.username}？`} description="该账号将立即失效且无法登录。" onConfirm={() => void remove(row)}>
                <Button type="link" danger>删除</Button>
              </Popconfirm>
            </Space>),
          },
        ]}
      />
      <Modal title="新建管理员" open={creating} onCancel={() => setCreating(false)} onOk={() => void create()}
        confirmLoading={saving} okText="创建" cancelText="取消" destroyOnHidden>
        <Paragraph type="secondary">只能分配不超出自己权限的角色。</Paragraph>
        <Form form={createForm} layout="vertical">
          <Form.Item name="username" label="用户名" rules={[{ required: true }, { pattern: /^[A-Za-z0-9_.-]{3,64}$/, message: '3–64 位英文、数字、_ . -' }]}>
            <Input maxLength={64} />
          </Form.Item>
          <Form.Item name="display_name" label="显示名" rules={[{ max: 128 }]}><Input maxLength={128} /></Form.Item>
          <Form.Item name="password" label="初始密码" rules={[{ required: true, min: 12, message: '至少 12 位' }]}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          {rolesError && <LoadError title="角色列表加载失败" detail={rolesError} onRetry={() => void loadRoles()} />}
          <Form.Item name="role_id" label="角色" rules={[{ required: true, message: '请选择角色' }]}>
            <Select options={roles.map((r) => ({ value: r.id, label: `${r.name}（${r.code}）` }))} />
          </Form.Item>
          <Form.Item name="phone" label="手机号" rules={[{ max: 32 }]}><Input maxLength={32} /></Form.Item>
        </Form>
      </Modal>
      <Modal title={`编辑账号 ${editing?.username || ''}`} open={!!editing} onCancel={() => setEditing(null)}
        onOk={() => void update()} confirmLoading={saving} okText="保存" cancelText="取消" destroyOnHidden>
        <Paragraph type="secondary">修改角色会立即使该账号已签发的会话失效。</Paragraph>
        <Form form={editForm} layout="vertical">
          <Form.Item name="display_name" label="显示名" rules={[{ max: 128 }]}><Input maxLength={128} /></Form.Item>
          {rolesError && <LoadError title="角色列表加载失败" detail={rolesError} onRetry={() => void loadRoles()} />}
          <Form.Item name="role_id" label="角色">
            <Select options={roles.map((r) => ({ value: r.id, label: `${r.name}（${r.code}）` }))} />
          </Form.Item>
          <Form.Item name="phone" label="手机号" rules={[{ max: 32 }]}><Input maxLength={32} /></Form.Item>
          <Form.Item name="email" label="邮箱" rules={[{ type: 'email' }]}><Input maxLength={128} /></Form.Item>
          <Form.Item name="status" label="状态">
            <Select options={[{ value: 'active', label: '正常' }, { value: 'disabled', label: '停用' }]} />
          </Form.Item>
        </Form>
      </Modal>
      <Modal title={`重置 ${resetting?.username || ''} 的密码`} open={!!resetting} onCancel={() => setResetting(null)}
        onOk={() => void resetPassword()} confirmLoading={saving} okText="重置" cancelText="取消" destroyOnHidden>
        <Alert type="warning" showIcon message="重置后该账号的所有登录会话会立即失效。" style={{ marginBottom: 12 }} />
        <Form form={resetForm} layout="vertical">
          <Form.Item name="new_password" label="新密码" rules={[{ required: true, min: 12, message: '至少 12 位' }]}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
        </Form>
      </Modal>
      <Modal title={`为 ${mfa?.user.username || ''} 启用双因素认证`} open={!!mfa} onCancel={() => setMfa(null)}
        onOk={() => void confirmMFA()} confirmLoading={saving} okText="确认启用" cancelText="取消" width={620} destroyOnHidden>
        <Paragraph>把密钥加入验证器 App，然后用其生成的 6 位验证码完成确认。未确认前该密钥不会生效。</Paragraph>
        <Form form={mfaForm} layout="vertical">
          <Form.Item label="密钥（base32）">
            <Input value={mfa?.secret} readOnly onFocus={(e) => e.target.select()} />
          </Form.Item>
          <Form.Item label="otpauth 链接">
            <Input value={mfa?.uri} readOnly />
          </Form.Item>
          <Form.Item name="secret" label="提交的密钥" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="code" label="验证器当前验证码" rules={[{ required: true }, { len: 6, message: '6 位数字' }]}>
            <Input maxLength={6} placeholder={mfa?.code} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
