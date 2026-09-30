import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Alert, Form, Input, Button, Typography, App, Card } from 'antd';
import { UserOutlined, LockOutlined, SafetyOutlined } from '@ant-design/icons';
import { apiPost, adminSession } from '../api/client';

const { Title, Text } = Typography;

interface LoginResp {
  token?: string;
  refresh_token?: string;
  admin_user_id: number;
  role: string;
  permissions: string[];
  mfa_required?: boolean;
  mfa_challenge?: string;
}

export default function LoginPage() {
  const nav = useNavigate();
  const { message } = App.useApp();
  const [loading, setLoading] = useState(false);
  const [challenge, setChallenge] = useState('');

  const onFinish = async (vals: { username: string; password: string; code?: string }) => {
    setLoading(true);
    try {
      const data = challenge
        ? await apiPost<LoginResp>('/api/v1/admin/auth/mfa', { mfa_challenge: challenge, code: vals.code })
        : await apiPost<LoginResp>('/api/v1/admin/auth/login', { username: vals.username, password: vals.password });
      // 密码正确但开了 MFA 时返回的是一个 challenge，而不是会话。
      if (data.mfa_required) {
        setChallenge(data.mfa_challenge || '');
        message.info('请输入验证器中的 6 位动态验证码');
        return;
      }
      if (!data.token) throw new Error('登录响应缺少令牌');
      await adminSession.login(data as { token: string; refresh_token: string; admin_user_id: number; role: string; permissions: string[] }, vals.username);
      message.success('登录成功');
      nav('/', { replace: true });
    } catch (e: any) {
      message.error(e?.message || '登录失败');
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="login-bg">
      <Card className="login-card">
        <Title level={3} style={{ textAlign: 'center', marginBottom: 24 }}>
          <span style={{ color: '#1677ff' }}>ChargePilot</span> · PC 后台
        </Title>
        {challenge ? (
          <Alert
            type="info" showIcon style={{ marginBottom: 16 }}
            message="该账号已启用双因素认证" description="请输入验证器 App 显示的 6 位动态验证码。"
          />
        ) : null}
        <Form layout="vertical" onFinish={onFinish} autoComplete="off">
          {!challenge && <>
            <Form.Item name="username" rules={[{ required: true, message: '请输入用户名' }]}>
              <Input prefix={<UserOutlined />} placeholder="用户名" size="large" autoComplete="username" />
            </Form.Item>
            <Form.Item name="password" rules={[{ required: true, message: '请输入密码' }]}>
              <Input.Password prefix={<LockOutlined />} placeholder="密码" size="large" autoComplete="current-password" />
            </Form.Item>
          </>}
          {challenge && (
            <Form.Item name="code" rules={[{ required: true, message: '请输入验证码' }, { len: 6, message: '6 位数字' }]}>
              <Input prefix={<SafetyOutlined />} placeholder="6 位动态验证码" size="large" maxLength={6} inputMode="numeric" autoComplete="one-time-code" />
            </Form.Item>
          )}
          <Form.Item>
            <Button type="primary" htmlType="submit" loading={loading} size="large" block>
              {challenge ? '验证并登录' : '登录'}
            </Button>
          </Form.Item>
          {challenge && (
            <Button type="link" block onClick={() => setChallenge('')}>返回重新登录</Button>
          )}
          <div style={{ color: '#999', fontSize: 12, textAlign: 'center' }}>
            <Text type="secondary">默认管理员账户由 .env 中 ADMIN_BOOTSTRAP_* 配置</Text>
          </div>
        </Form>
      </Card>
    </div>
  );
}
