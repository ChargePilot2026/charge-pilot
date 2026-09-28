import { Typography } from 'antd';
import { OtaPackages, OtaSchedules } from './OtaOps';

const { Title } = Typography;

export default function OTAPage() {
  return (
    <div className="page-container">
      <Title level={3}>OTA 固件</Title>
      <OtaPackages />
      <div style={{ height: 24 }} />
      <OtaSchedules />
    </div>
  );
}
