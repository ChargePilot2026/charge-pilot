import { Card, Row, Col, Statistic, Typography } from 'antd';
import { ThunderboltOutlined, DollarOutlined, UserOutlined, AlertOutlined } from '@ant-design/icons';

const { Title, Paragraph } = Typography;

export default function DashboardPage() {
  return (
    <div className="page-container">
      <Title level={3}>仪表盘</Title>
      <Paragraph type="secondary">
        系统状态总览。详细数据请进入对应模块。
      </Paragraph>
      <Row gutter={16}>
        <Col span={6}>
          <Card>
            <Statistic title="在线充电" value={0} prefix={<ThunderboltOutlined />} suffix="台" />
          </Card>
        </Col>
        <Col span={6}>
          <Card>
            <Statistic title="今日营收" value={0} prefix={<DollarOutlined />} suffix="元" precision={2} />
          </Card>
        </Col>
        <Col span={6}>
          <Card>
            <Statistic title="活跃用户" value={0} prefix={<UserOutlined />} suffix="人" />
          </Card>
        </Col>
        <Col span={6}>
          <Card>
            <Statistic title="待处理告警" value={0} prefix={<AlertOutlined />} suffix="条" valueStyle={{ color: '#cf1322' }} />
          </Card>
        </Col>
      </Row>
      <Row gutter={16} style={{ marginTop: 16 }}>
        <Col span={12}>
          <Card title="设备分布">
            <Paragraph type="secondary">近 7 天设备活跃度</Paragraph>
            {/* 接入图表: echarts-for-react 或 @ant-design/charts */}
          </Card>
        </Col>
        <Col span={12}>
          <Card title="订单趋势">
            <Paragraph type="secondary">近 7 天订单 / 营收</Paragraph>
          </Card>
        </Col>
      </Row>
    </div>
  );
}