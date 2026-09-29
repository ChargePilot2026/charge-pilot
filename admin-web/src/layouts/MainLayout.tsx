import { Outlet, useNavigate, useLocation } from 'react-router-dom';
import { Layout, Menu, Avatar, Button, Dropdown, Typography, type MenuProps } from 'antd';
import {
  DashboardOutlined, AlertOutlined, GiftOutlined, AccountBookOutlined,
  SettingOutlined, UserOutlined, LogoutOutlined, ThunderboltOutlined,
  CustomerServiceOutlined,
} from '@ant-design/icons';
import { useEffect, useState, type ReactNode } from 'react';
import { apiPost, adminSession } from '../api/client';

const { Header, Sider, Content } = Layout;
const { Text } = Typography;

type NavLink = { key: string; icon?: ReactNode; label: string; permission: string };
type NavSection = { key: string; icon: ReactNode; label: string; children: NavLink[] };

const dashboard: NavLink = { key: '/', icon: <DashboardOutlined />, label: '仪表盘', permission: 'dashboard.read' };
const sections: NavSection[] = [
  { key: 'charge', icon: <ThunderboltOutlined />, label: '充电运营', children: [
    { key: '/orders', label: '订单', permission: 'order.read' },
    { key: '/stations', label: '站点', permission: 'station.read' },
    { key: '/devices', label: '设备', permission: 'device.read' },
    { key: '/pricing-rules', label: '计费规则', permission: 'pricing.read' },
    { key: '/charge-packages', label: '充电套餐', permission: 'pricing.read' },
  ] },
  { key: 'users', icon: <GiftOutlined />, label: '用户运营', children: [
    { key: '/coupons', label: '优惠券', permission: 'coupon.read' },
    { key: '/announcements', label: '公告', permission: 'announcement.read' },
  ] },
  { key: 'service', icon: <CustomerServiceOutlined />, label: '客户服务', children: [
    { key: '/customer-service', label: '客服坐席', permission: 'customer_service.read' },
    { key: '/casework', label: '反馈与报修', permission: 'feedback.read' },
  ] },
  { key: 'finance', icon: <AccountBookOutlined />, label: '财务管理', children: [
    { key: '/billing', label: '财务', permission: 'finance.read' },
    { key: '/exports', label: '数据导出', permission: 'finance.read' },
  ] },
  { key: 'device-ops', icon: <AlertOutlined />, label: '设备运维', children: [
    { key: '/alerts', label: '告警', permission: 'alert.read' },
    { key: '/alert-rules', label: '告警配置', permission: 'alert.read' },
    { key: '/ota', label: 'OTA', permission: 'ota.read' },
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
  const permissions = new Set<string>(Array.isArray(adminInfo?.permissions) ? adminInfo.permissions : []);
  const visibleSections = sections.map(section => ({ ...section, children: section.children.filter(link => permissions.has(link.permission)) }))
    .filter(section => section.children.length > 0);
  const selectedLink = links.find(link => pathname === link.key || link.key !== '/' && pathname.startsWith(link.key + '/'));
  const activeSection = visibleSections.find(section => section.children.some(link => link.key === selectedLink?.key))?.key;
  const [openKeys, setOpenKeys] = useState<string[]>(activeSection ? [activeSection] : []);
  useEffect(() => { setOpenKeys(activeSection ? [activeSection] : []); }, [activeSection]);
  const menuItems: MenuProps['items'] = [
    ...(permissions.has(dashboard.permission) ? [dashboard] : []),
    ...visibleSections,
  ].map(item => 'children' in item
    ? { key: item.key, icon: item.icon, label: item.label, children: item.children.map(link => ({ key: link.key, label: link.label })) }
    : { key: item.key, icon: item.icon, label: item.label });

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider collapsible collapsed={collapsed} onCollapse={setCollapsed} theme="dark" width={232} style={{ position: 'sticky', top: 0, height: '100vh', overflowY: 'auto' }}>
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
          aria-label="后台导航"
          selectedKeys={selectedLink && permissions.has(selectedLink.permission) ? [selectedLink.key] : []}
          openKeys={collapsed ? undefined : openKeys}
          onOpenChange={keys => setOpenKeys(keys.slice(-1))}
          items={menuItems}
          onClick={({ key }) => { if (links.some(link => link.key === key)) navigate(key); }}
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
