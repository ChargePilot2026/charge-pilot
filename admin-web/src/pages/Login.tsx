import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Form, Input, Button, Typography, App, Card } from 'antd';
import { UserOutlined, LockOutlined } from '@ant-design/icons';
import { apiPost } from '../api/client';

const { Title } = Typography;

interface LoginResp {
  token: string;
  admin_user_id: number;
  role: string;
  permissions: string[];
}

export default function LoginPage() {
  const nav = useNavigate();
  const { message } = App.useApp();
  const [loading, setLoading] = useState(false);

  const onFinish = async (vals: { username: string; password: string }) => {
    setLoading(true);
    try {
      const data = await apiPost<LoginResp>('/api/v1/admin/auth/login', vals);
      localStorage.setItem('cp_token', data.token);
      localStorage.setItem('cp_admin', JSON.stringify({ username: vals.username, role: data.role }));
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
        <Form layout="vertical" onFinish={onFinish} autoComplete="off">
          <Form.Item name="username" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input prefix={<UserOutlined />} placeholder="用户名" size="large" />
          </Form.Item>
          <Form.Item name="password" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password prefix={<LockOutlined />} placeholder="密码" size="large" />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit" loading={loading} size="large" block>
              登录
            </Button>
          </Form.Item>
          <div style={{ color: '#999', fontSize: 12, textAlign: 'center' }}>
            默认管理员账户由 .env 中 ADMIN_BOOTSTRAP_* 配置
          </div>
        </Form>
      </Card>
    </div>
  );
}