import { Tabs } from 'antd';
import { OtaPackages, OtaSchedules } from './OtaOps';

export default function OTAPage() {
  return (
    <div className="page-container">
      <Tabs destroyOnHidden items={[
        { key: 'packages', label: '固件管理', children: <OtaPackages /> },
        { key: 'schedules', label: '升级计划', children: <OtaSchedules /> },
      ]} />
    </div>
  );
}
