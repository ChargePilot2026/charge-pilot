import { Tabs, Typography } from 'antd';
import { useSearchParams } from 'react-router-dom';
import PricingTemplates from './PricingTemplates';
import PackageTemplates from './PackageTemplates';

export default function TemplatesPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = searchParams.get('tab') === 'packages' ? 'packages' : 'pricing';

  return <div className="page-container">
    <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 12 }}>模板</Typography.Title>
    <Tabs activeKey={activeTab} destroyOnHidden onChange={tab => {
      const params = new URLSearchParams(searchParams);
      params.set('tab', tab);
      setSearchParams(params);
    }} items={[
      { key: 'pricing', label: '计费模板', children: <PricingTemplates /> },
      { key: 'packages', label: '套餐模板', children: <PackageTemplates /> },
    ]} />
  </div>;
}
