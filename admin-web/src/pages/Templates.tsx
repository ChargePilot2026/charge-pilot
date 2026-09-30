import { Typography } from 'antd';
import ChargingSchemes from './ChargingSchemes';

export default function TemplatesPage() {
  return <div className="page-container">
    <Typography.Title level={3} style={{ marginTop: 0, marginBottom: 12 }}>充电方案模板</Typography.Title>
    <ChargingSchemes />
  </div>;
}
