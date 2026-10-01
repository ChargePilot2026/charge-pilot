import { Alert, Button, Form, Input, QRCode, Space, Spin, Tag, Typography, message } from 'antd';
import axios from 'axios';
import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { adminSession, apiGet, apiPost } from '../api/client';
import type { SessionProfile } from '../api/session';
import { LoadError } from '../components/LoadError';

type Enrollment = { enrollment_id: string; secret: string; otpauth_uri: string; expires_in: number };
type MFAResult = { mfa_enabled: boolean; sessions_revoked: boolean };
type Action = 'enrol' | 'confirm' | 'disable';
const codeRules = [{ required: true, message: '请输入验证器中的动态码' }, { pattern: /^\d{6}$/, message: '请输入 6 位数字动态码' }];
const passwordRules = [{ required: true, message: '请输入当前密码' }];
const errorMessage = (cause: unknown) => cause instanceof Error ? cause.message : '账号安全设置失败，请稍后重试';

export default function SecurityPage() {
  const navigate = useNavigate();
  const [profile, setProfile] = useState<SessionProfile | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [pending, setPending] = useState<Enrollment>();
  const [form] = Form.useForm<{ password: string; code: string }>();
  const sessionEpoch = useRef(adminSession.epoch());
  const generation = useRef(0);
  const submitting = useRef(false);

  const currentRequest = (current: number) => current === generation.current && sessionEpoch.current === adminSession.epoch();
  const load = async () => {
    const current = ++generation.current;
    setLoading(true); setLoadError(''); setError('');
    try {
      const result = await apiGet<SessionProfile>('/api/v1/admin/auth/me');
      if (!currentRequest(current)) return;
      if (typeof result?.mfa_enabled !== 'boolean') throw new Error('账号安全状态不完整，请重新读取');
      setProfile(result);
      if (result.mfa_enabled) { setPending(undefined); form.resetFields(); }
    } catch (cause: unknown) {
      if (currentRequest(current)) { setProfile(null); setLoadError(errorMessage(cause)); }
    } finally {
      if (currentRequest(current)) setLoading(false);
    }
  };

  useEffect(() => {
    void load();
    const invalidate = () => {
      if (sessionEpoch.current !== adminSession.epoch()) {
        generation.current++; setProfile(null); setPending(undefined); setLoading(false);
        form.resetFields();
        setLoadError('登录账号已变化，请重新打开账号安全页面');
      }
    };
    window.addEventListener('cp-session', invalidate);
    window.addEventListener('storage', invalidate);
    return () => {
      generation.current++;
      window.removeEventListener('cp-session', invalidate);
      window.removeEventListener('storage', invalidate);
    };
  }, []);

  const submit = async (action: Action) => {
    if (submitting.current || loading || !profile || sessionEpoch.current !== adminSession.epoch()) return;
    const current = generation.current;
    submitting.current = true; setSaving(true); setError('');
    try {
      let body: { action: Action; password?: string; code?: string; enrollment_id?: string };
      if (action === 'enrol') {
        const values = await form.validateFields();
        body = { action, password: values.password };
      } else if (action === 'confirm') {
        if (!pending) return;
        const values = await form.validateFields();
        body = { action, enrollment_id: pending.enrollment_id, code: values.code };
      } else {
        const values = await form.validateFields();
        body = { action, password: values.password, code: values.code };
      }
      if (!currentRequest(current)) return;
      if (action === 'enrol') {
        const result = await apiPost<Enrollment>('/api/v1/admin/auth/mfa-settings', body);
        if (!currentRequest(current)) return;
        if (!result?.enrollment_id || !result.secret || !result.otpauth_uri || !Number.isFinite(result.expires_in) || result.expires_in <= 0) {
          throw new Error('绑定信息不完整，请重新开始');
        }
        setPending(result); form.resetFields();
      } else {
        const result = await apiPost<MFAResult>('/api/v1/admin/auth/mfa-settings', body);
        if (!currentRequest(current)) return;
        if (!result?.sessions_revoked || result.mfa_enabled !== (action === 'confirm')) {
          throw new Error('账号安全设置结果未确认，请重新读取状态');
        }
        setPending(undefined); form.resetFields();
        message.success(action === 'confirm' ? '双因素认证已启用，请重新登录' : '双因素认证已关闭，请重新登录');
        if (await adminSession.clear(sessionEpoch.current)) navigate('/login', { replace: true });
      }
    } catch (cause: unknown) {
      if (!currentRequest(current) || (cause && typeof cause === 'object' && 'errorFields' in cause)) return;
      if (axios.isAxiosError(cause) && cause.response?.status === 409 && action === 'confirm') {
        setPending(undefined); form.resetFields();
        setError('绑定已过期、被替换或账号状态已变化，请重新读取状态后开始绑定。');
      } else setError(errorMessage(cause));
    } finally {
      submitting.current = false;
      if (currentRequest(current)) setSaving(false);
    }
  };

  return <div className="page-container">
    {loading && <Spin />}
    {loadError && <LoadError title="账号安全状态读取失败" detail={loadError} onRetry={() => void load()} />}
    <Form form={form} name="self_mfa_settings" layout="vertical" disabled={saving || loading}
      onFinish={() => void submit(profile?.mfa_enabled ? 'disable' : pending ? 'confirm' : 'enrol')}>
    {profile && <Space direction="vertical" size="large" style={{ width: '100%', maxWidth: 560 }}>
      <Space><Typography.Text strong>双因素认证（MFA）</Typography.Text><Tag>{profile.mfa_enabled ? '已启用' : '未启用'}</Tag></Space>
      <Typography.Text type="secondary">启用后登录需输入密码和验证器动态码。完成设置后需要重新登录。</Typography.Text>
      {error && <Alert type="error" showIcon message="设置未完成" description={error}
        action={<Button size="small" disabled={saving} onClick={() => void load()}>重新读取</Button>} />}
      {profile.mfa_enabled ? <div style={{ width: '100%' }}>
        <Form.Item name="password" label="当前密码" rules={passwordRules}><Input.Password autoComplete="current-password" maxLength={72} /></Form.Item>
        <Form.Item name="code" label="验证器动态码" rules={codeRules} normalize={(value: string) => value.trim()}>
          <Input inputMode="numeric" autoComplete="one-time-code" maxLength={6} placeholder="6 位数字" />
        </Form.Item>
        <Button htmlType="submit" danger loading={saving}>关闭双因素认证</Button>
      </div> : pending ? <>
        <Typography.Text>使用验证器扫描二维码，或手动输入密钥。绑定信息有效 {Math.ceil(pending.expires_in / 60)} 分钟。</Typography.Text>
        <QRCode value={pending.otpauth_uri} />
        <Space direction="vertical" style={{ width: '100%' }}>
          <Typography.Text strong>密钥</Typography.Text><Typography.Text copyable>{pending.secret}</Typography.Text>
          <Typography.Text strong>导入链接</Typography.Text><Typography.Paragraph copyable ellipsis={{ rows: 2, expandable: true }}>{pending.otpauth_uri}</Typography.Paragraph>
        </Space>
        <div style={{ width: '100%' }}>
          <Form.Item name="code" label="验证器动态码" rules={codeRules} normalize={(value: string) => value.trim()}>
            <Input inputMode="numeric" autoComplete="one-time-code" maxLength={6} placeholder="输入验证器中的 6 位数字" />
          </Form.Item>
          <Space><Button type="primary" htmlType="submit" loading={saving}>确认启用</Button>
            <Button disabled={saving} onClick={() => { setPending(undefined); form.resetFields(); setError(''); }}>重新开始</Button></Space>
        </div>
      </> : <div style={{ width: '100%' }}>
        <Form.Item name="password" label="当前密码" rules={passwordRules}><Input.Password autoComplete="current-password" maxLength={72} /></Form.Item>
        <Button type="primary" htmlType="submit" loading={saving}>开始绑定验证器</Button>
      </div>}
    </Space>}
    </Form>
  </div>;
}
