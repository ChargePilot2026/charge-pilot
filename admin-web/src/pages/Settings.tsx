import { useEffect, useState } from 'react';
import { Card, Form, Input, Button, Space, message } from 'antd';
import { SaveOutlined } from '@ant-design/icons';
import { apiGet, apiPut } from '../api/client';
import { LoadError } from '../components/LoadError';

export default function SettingsPage() {
  const [whitelabel, setWhitelabel] = useState<Record<string, unknown>>({});
  const [form] = Form.useForm();
  const [loadError, setLoadError] = useState<string | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    setLoadError(null);
    apiGet<Record<string, unknown>>('/api/v1/admin/whitelabel').then((value) => {
      const config = value || {};
      setWhitelabel(config);
      form.setFieldsValue(config);
    }).catch((error: any) => setLoadError(error?.message || '白标配置读取失败'));
  }, [form, reload]);

  const onSave = async (vals: any) => {
    try {
      const config = await apiPut<Record<string, unknown>>('/api/v1/admin/whitelabel', vals);
      if (config?.config && typeof config.config === 'object') {
        setWhitelabel(config.config as Record<string, unknown>);
        form.setFieldsValue(config.config as Record<string, unknown>);
      }
      message.success('已保存');
    } catch (e: any) { message.error(e?.message || '失败'); }
  };

  return (
    <div className="page-container">
      {loadError && <LoadError title="平台设置加载失败" detail={loadError} onRetry={() => setReload(v => v + 1)} />}
      <Card>
        <Form form={form} layout="vertical" initialValues={whitelabel} onFinish={onSave}>
          <Form.Item name="miniprogram_name" label="小程序名称" rules={[{ required: true, whitespace: true, max: 64 }]}><Input maxLength={64} /></Form.Item>
          <Form.Item name="miniprogram_logo_url" label="小程序 Logo HTTPS 链接" rules={[{ type: 'url', warningOnly: true }, { validator: async (_, value) => { if (value && !value.startsWith('https://')) throw new Error('链接必须使用 HTTPS'); } }]}><Input maxLength={512} /></Form.Item>
          <Form.Item name="admin_logo_url" label="后台 Logo HTTPS 链接" rules={[{ type: 'url', warningOnly: true }, { validator: async (_, value) => { if (value && !value.startsWith('https://')) throw new Error('链接必须使用 HTTPS'); } }]}><Input maxLength={512} /></Form.Item>
          <Form.Item name="theme_color" label="主题色" rules={[{ required: true, pattern: /^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$/, message: '请输入 #RRGGBB 或 #RRGGBBAA 格式' }]}><Input placeholder="#1677ff" maxLength={9} /></Form.Item>
          <Form.Item name="service_phone" label="客服电话"><Input maxLength={32} /></Form.Item>
          <Form.Item name="service_wechat_id" label="客服微信号"><Input maxLength={64} /></Form.Item>
          <Form.Item name="icp_record_no" label="ICP备案号"><Input maxLength={128} /></Form.Item>
          <Form.Item name="custom_domain" label="自定义域名"><Input maxLength={253} placeholder="charge.example.com" /></Form.Item>
          <Form.Item name="agreement_url" label="用户协议 HTTPS 链接" rules={[{ type: 'url', warningOnly: true }, { validator: async (_, value) => { if (value && !value.startsWith('https://')) throw new Error('链接必须使用 HTTPS'); } }]}><Input maxLength={512} /></Form.Item>
          <Form.Item name="privacy_url" label="隐私政策 HTTPS 链接" rules={[{ type: 'url', warningOnly: true }, { validator: async (_, value) => { if (value && !value.startsWith('https://')) throw new Error('链接必须使用 HTTPS'); } }]}><Input maxLength={512} /></Form.Item>
          <Form.Item name="about_us" label="关于我们"><Input.TextArea rows={4} maxLength={20000} showCount /></Form.Item>
          <Form.Item><Space><Button type="primary" icon={<SaveOutlined />} htmlType="submit">保存</Button></Space></Form.Item>
        </Form>
      </Card>
    </div>
  );
}
