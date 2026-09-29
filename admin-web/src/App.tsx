import { Routes, Route, Navigate, useLocation } from 'react-router-dom';
import { Typography } from 'antd';
import type { ReactNode } from 'react';
import LoginPage from './pages/Login';
import MainLayout from './layouts/MainLayout';
import DashboardPage from './pages/Dashboard';
import OrdersPage from './pages/Orders';
import DevicesPage from './pages/Devices';
import StationsPage from './pages/Stations';
import UsersPage from './pages/Users';
import AlertsPage from './pages/Alerts';
import CouponsPage from './pages/Coupons';
import BillingPage from './pages/Billing';
import SettingsPage from './pages/Settings';
import WebhooksPage from './pages/Webhooks';
import OTAPage from './pages/OTA';
import AnnouncementsPage from './pages/Announcements';
import CustomerServicePage from './pages/CustomerService';
import CaseworkPage from './pages/Casework';
import AlertRulesPage from './pages/AlertRules';
import ExportsPage from './pages/Exports';
import AuditLogsPage from './pages/AuditLogs';
import PricingRules from './pages/PricingRules';
import ChargePackages from './pages/ChargePackages';

function ConfigPage({ title, children }: { title: string; children: ReactNode }) {
  return <div className="page-container"><Typography.Title level={3}>{title}</Typography.Title>{children}</div>;
}

function RequireAuth({ children }: { children: JSX.Element }) {
  const token = localStorage.getItem('cp_token');
  const location = useLocation();
  if (!token) return <Navigate to="/login" state={{ from: location }} replace />;
  return children;
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/" element={<RequireAuth><MainLayout /></RequireAuth>}>
        <Route index element={<DashboardPage />} />
        <Route path="orders" element={<OrdersPage />} />
        <Route path="devices" element={<DevicesPage />} />
        <Route path="stations" element={<StationsPage />} />
        <Route path="pricing-rules" element={<ConfigPage title="计费规则"><PricingRules /></ConfigPage>} />
        <Route path="charge-packages" element={<ConfigPage title="充电套餐"><ChargePackages /></ConfigPage>} />
        <Route path="users" element={<UsersPage />} />
        <Route path="alerts" element={<AlertsPage />} />
        <Route path="alert-rules" element={<AlertRulesPage />} />
        <Route path="exports" element={<ExportsPage />} />
        <Route path="audit-logs" element={<AuditLogsPage />} />
        <Route path="coupons" element={<CouponsPage />} />
        <Route path="billing" element={<BillingPage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="webhooks" element={<WebhooksPage />} />
        <Route path="ota" element={<OTAPage />} />
        <Route path="announcements" element={<AnnouncementsPage />} />
        <Route path="customer-service" element={<CustomerServicePage />} />
        <Route path="casework" element={<CaseworkPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
