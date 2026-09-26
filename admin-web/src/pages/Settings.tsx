import { useEffect, useState } from 'react';
import { Typography, Card, Form, Input, Button, Space, Tabs, message } from 'antd';
import { SaveOutlined } from '@ant-design/icons';
import { apiGet, apiPut } from '../api/client';

const { Title } = Typography;

export default function SettingsPage() {
  const [whitelabel, setWhitelabel] = useState<any>(null);
  const [pricingRules, setPricingRules] = useState<any[]>([]);
  const [form] = Form.useForm();

  useEffect(() => {
    apiGet('/api/v1/admin/whitelabel').then(setWhitelabel).catch(() => {});
    apiGet<{ items: any[] }>('/api/v1/admin/settings/charge-rules').then(d => setPricingRules(d.items || [])).catch(() => {});
  }, []);

  const onSave = async (vals: any) => {
    try {
      await apiPut('/api/v1/admin/whitelabel', vals);
      message.success('已保存');
    } catch (e: any) { message.error(e?.message || '失败'); }
  };

  return (
    <div className="page-container">
      <Title level={3}>系统设置</Title>
      <Tabs
        items={[
          {
            key: 'whitelabel',
            label: '白标',
            children: (
              <Card>
                <Form form={form} layout="vertical" initialValues={whitelabel || {}}
                  onFinish={onSave}>
                  <Form.Item name="name" label="客户名称"><Input /></Form.Item>
                  <Form.Item name="logo_url" label="Logo URL"><Input /></Form.Item>
                  <Form.Item name="mini_program_name" label="小程序名称"><Input /></Form.Item>
                  <Form.Item name="mini_program_appid" label="小程序 AppID"><Input /></Form.Item>
                  <Form.Item name="theme_color" label="主题色"><Input placeholder="#1677ff" /></Form.Item>
                  <Form.Item name="contact_phone" label="客服电话"><Input /></Form.Item>
                  <Form.Item name="about_text" label="关于我们"><Input.TextArea rows={4} /></Form.Item>
                  <Form.Item>
                    <Space><Button type="primary" icon={<SaveOutlined />} htmlType="submit">保存</Button></Space>
                  </Form.Item>
                </Form>
              </Card>
            ),
          },
          {
            key: 'pricing',
            label: '计费规则',
            children: (
              <Card>
                {pricingRules.map(r => (
                  <Card.Grid key={r.id} style={{ width: '33.33%' }}>
                    <Title level={5}>{r.name}</Title>
                    <p>模式: {r.mode}</p>
                    <p>服务费: {r.service_fee_cents_per_kwh} 分/kWh</p>
                    <p>起步价: {r.min_charge_cents} 分</p>
                    <p>版本: v{r.version}</p>
                  </Card.Grid>
                ))}
              </Card>
            ),
          },
        ]}
      />
    </div>
  );
}