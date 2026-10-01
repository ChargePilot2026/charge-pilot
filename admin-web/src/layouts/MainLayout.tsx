import { Outlet, useNavigate, useLocation } from 'react-router-dom';
import { Layout, Menu, Avatar, Button, Dropdown, Typography, type MenuProps } from 'antd';
import {
  DashboardOutlined, AlertOutlined, GiftOutlined, AccountBookOutlined,
  SettingOutlined, UserOutlined, LogoutOutlined, ThunderboltOutlined, SafetyCertificateOutlined,
} from '@ant-design/icons';
import { useEffect, useState, type ReactNode } from 'react';
import { apiGet, apiPost, adminSession } from '../api/client';
import type { SessionProfile } from '../api/session';

const { Header, Content } = Layout;
const { Text } = Typography;

type NavLink = { key: string; icon?: ReactNode; label: string; permission: string };
type NavSection = { key: string; icon: ReactNode; label: string; children: NavLink[] };

const dashboard: NavLink = { key: '/', icon: <DashboardOutlined />, label: '仪表盘', permission: 'dashboard.read' };
const sections: NavSection[] = [
  { key: 'charge', icon: <ThunderboltOutlined />, label: '充电运营', children: [
    { key: '/orders', label: '充电订单', permission: 'order.read' },
    { key: '/payment-orders', label: '支付订单', permission: 'order.read' },
    { key: '/stations', label: '站点', permission: 'station.read' },
    { key: '/devices', label: '设备', permission: 'device.read' },
    { key: '/templates', label: '模板', permission: 'pricing.read' },
  ] },
  { key: 'users', icon: <GiftOutlined />, label: '用户运营', children: [
    { key: '/charge-users', label: '充电用户', permission: 'charge_user.read' },
    { key: '/online-cards', label: '在线卡', permission: 'charge_user.read' },
    { key: '/coupons', label: '优惠券', permission: 'coupon.read' },
    { key: '/announcements', label: '公告', permission: 'announcement.read' },
  ] },
  { key: 'finance', icon: <AccountBookOutlined />, label: '财务管理', children: [
    { key: '/billing', label: '财务', permission: 'finance.read' },
    { key: '/exports', label: '数据导出', permission: 'finance.read' },
  ] },
  { key: 'device-ops', icon: <AlertOutlined />, label: '设备运维', children: [
    { key: '/vendors', label: '厂商', permission: 'vendor.read' },
    { key: '/alerts', label: '告警', permission: 'alert.read' },
    { key: '/casework', label: '反馈报修', permission: 'feedback.read' },
  ] },
  { key: 'system', icon: <SettingOutlined />, label: '系统管理', children: [
    { key: '/settings', label: '平台设置', permission: 'whitelabel.read' },
    { key: '/users', label: '管理员', permission: 'admin_user.read' },
    { key: '/webhooks', label: 'Webhook', permission: 'webhook.read' },
    { key: '/audit-logs', label: '审计日志', permission: 'audit.read' },
  ] },
];
const links = [dashboard, ...sections.flatMap(section => section.children)];

export default function MainLayout() {
  const [, sessionChanged] = useState(0);
  useEffect(() => { const update = () => sessionChanged(v => v + 1); window.addEventListener('cp-session', update); window.addEventListener('storage', update); return () => { window.removeEventListener('cp-session', update); window.removeEventListener('storage', update); }; }, []);
  const sessionEpoch = adminSession.epoch();
  useEffect(() => {
    // 进入会话时同步身份；登录和续期响应已经包含最新权限，无需按窗口焦点重复查询。
    void adminSession.syncProfile(() => apiGet<SessionProfile>('/api/v1/admin/auth/me')).catch(() => undefined);
  }, [sessionEpoch]);
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
  const permissions = new Set<string>(Array.isArray(adminInfo?.permissions) ? adminInfo.permissions : []);
  const visibleSections = sections.map(section => ({ ...section, children: section.children.filter(link => permissions.has(link.permission)) }))
    .filter(section => section.children.length > 0);
  const selectedLink = links.find(link => pathname === link.key || link.key !== '/' && pathname.startsWith(link.key + '/'));
  const pageTitle = pathname === '/security' ? '账号安全' : selectedLink?.label;
  const menuItems: MenuProps['items'] = [
    ...(permissions.has(dashboard.permission) ? [dashboard] : []),
    ...visibleSections,
  ].map(item => 'children' in item
    ? { key: item.key, icon: item.icon, label: item.label, children: item.children.map(link => ({ key: link.key, label: link.label })) }
    : { key: item.key, icon: item.icon, label: item.label });

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Header className="layout-header">
        <div className="layout-heading">
          <Text strong className="layout-brand">ChargePilot ·</Text>
          {pageTitle && <Typography.Title level={3} className="layout-page-title">{pageTitle}</Typography.Title>}
        </div>
        <Menu
          mode="horizontal"
          className="layout-navigation"
          aria-label="后台导航"
          selectedKeys={selectedLink && permissions.has(selectedLink.permission) ? [selectedLink.key] : []}
          items={menuItems}
          onClick={({ key }) => { if (links.some(link => link.key === key)) navigate(key); }}
        />
        <Dropdown
          trigger={['click']}
          menu={{
            items: [
              { key: 'security', icon: <SafetyCertificateOutlined />, label: '账号安全', onClick: () => navigate('/security') },
              { key: 'logout', icon: <LogoutOutlined />, label: '退出', onClick: onLogout },
            ],
          }}
        >
          <Button type="text" aria-label="账号菜单" style={{ height: 'auto', display: 'flex', alignItems: 'center', gap: 8, flexShrink: 0 }}>
            <Avatar icon={<UserOutlined />} />
            <span>{adminInfo?.display_name?.trim() || adminInfo?.username || 'admin'}</span>
            <Text type="secondary" style={{ fontSize: 12 }}>{adminInfo?.role_name || adminInfo?.role || ''}</Text>
          </Button>
        </Dropdown>
      </Header>
      <Content>
        <Outlet />
      </Content>
    </Layout>
  );
}
