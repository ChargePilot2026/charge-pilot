import { Outlet, useNavigate, useLocation } from 'react-router-dom';
import { Layout, Menu, Avatar, Button, Dropdown, Typography } from 'antd';
import {
  DashboardOutlined, ShoppingOutlined, DesktopOutlined, EnvironmentOutlined,
  TeamOutlined, AlertOutlined, GiftOutlined, AccountBookOutlined,
  SettingOutlined, ApiOutlined, CloudUploadOutlined, NotificationOutlined,
  UserOutlined, LogoutOutlined, MessageOutlined, ToolOutlined,
} from '@ant-design/icons';
import { useEffect, useState } from 'react';
import { apiPost, adminSession } from '../api/client';

const { Header, Sider, Content } = Layout;
const { Text } = Typography;

const items = [
  { key: '/', icon: <DashboardOutlined />, label: '仪表盘' },
  { key: '/orders', icon: <ShoppingOutlined />, label: '订单' },
  { key: '/devices', icon: <DesktopOutlined />, label: '设备' },
  { key: '/stations', icon: <EnvironmentOutlined />, label: '站点' },
  { key: '/users', icon: <TeamOutlined />, label: '管理员' },
  { key: '/alerts', icon: <AlertOutlined />, label: '告警' },
  { key: '/coupons', icon: <GiftOutlined />, label: '优惠券' },
  { key: '/billing', icon: <AccountBookOutlined />, label: '财务' },
  { key: '/webhooks', icon: <ApiOutlined />, label: 'Webhook' },
  { key: '/ota', icon: <CloudUploadOutlined />, label: 'OTA' },
  { key: '/announcements', icon: <NotificationOutlined />, label: '公告' },
  { key: '/customer-service', icon: <MessageOutlined />, label: '客服坐席' },
  { key: '/casework', icon: <ToolOutlined />, label: '反馈与报修' },
  { key: '/settings', icon: <SettingOutlined />, label: '设置' },
];

const menuPermission: Record<string,string> = {
 '/':'dashboard.read','/orders':'order.read','/devices':'device.read','/stations':'station.read',
 '/users':'admin_user.read','/alerts':'alert.read','/coupons':'coupon.read','/billing':'finance.read',
 '/webhooks':'webhook.read','/ota':'ota.read','/announcements':'announcement.read',
 '/customer-service':'customer_service.read','/casework':'feedback.read','/settings':'whitelabel.read',
};

export default function MainLayout() {
  const [collapsed, setCollapsed] = useState(false);
  const [, sessionChanged] = useState(0);
  useEffect(() => { const update = () => sessionChanged(v => v + 1); window.addEventListener('cp-session', update); window.addEventListener('storage', update); return () => { window.removeEventListener('cp-session', update); window.removeEventListener('storage', update); }; }, []);
  const navigate = useNavigate();
  const { pathname } = useLocation();

  const onLogout = async () => {
    const epoch = adminSession.epoch();
    await apiPost('/api/v1/admin/auth/logout').catch(() => undefined);
    if (await adminSession.clear(epoch)) navigate('/login', { replace: true });
  };

  const adminInfo = (() => {
    try { return JSON.parse(localStorage.getItem('cp_admin') || 'null'); } catch { return null; }
  })();

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider collapsible collapsed={collapsed} onCollapse={setCollapsed} theme="dark">
        <div className="logo">
          <svg viewBox="0 0 64 64" width="28" height="28">
            <rect width="64" height="64" rx="12" fill="#1677ff" />
            <path d="M22 12 L42 12 L36 28 L46 28 L26 52 L30 36 L20 36 Z" fill="#fff" stroke="#fff" strokeWidth="1.5" />
          </svg>
          {!collapsed && <span>ChargePilot</span>}
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[items.find(item => item.key !== '/' && (pathname === item.key || pathname.startsWith(item.key + '/')))?.key || '/']}
          items={items.filter(item => adminInfo?.permissions?.includes(menuPermission[item.key]))}
          onClick={({ key }) => navigate(key)}
        />
      </Sider>
      <Layout>
        <Header className="layout-header">
          <Text strong style={{ fontSize: 16 }}>ChargePilot · 二轮车充电运营管理</Text>
          <Dropdown
            trigger={['click']}
            menu={{
              items: [
                { key: 'logout', icon: <LogoutOutlined />, label: '退出', onClick: onLogout },
              ],
            }}
          >
            <Button type="text" aria-label="账号菜单" style={{ height: 'auto', display: 'flex', alignItems: 'center', gap: 8 }}>
              <Avatar icon={<UserOutlined />} />
              <span>{adminInfo?.username || 'admin'}</span>
              <Text type="secondary" style={{ fontSize: 12 }}>{adminInfo?.role || ''}</Text>
            </Button>
          </Dropdown>
        </Header>
        <Content>
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  );
}
