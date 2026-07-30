import React, { useEffect, useState } from 'react';
import { Card, Col, Row, Statistic, Typography, Spin, message } from 'antd';
import {
  ApiOutlined,
  KeyOutlined,
  DollarOutlined,
  CheckCircleOutlined,
  CloseCircleOutlined,
} from '@ant-design/icons';
import { getOverview, getHealth, OverviewStats } from '../api/client';

const { Title } = Typography;

const Dashboard: React.FC = () => {
  const [stats, setStats] = useState<OverviewStats | null>(null);
  const [health, setHealth] = useState<string>('checking...');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const fetch = async () => {
      try {
        const [statsRes, healthRes] = await Promise.all([
          getOverview(),
          getHealth(),
        ]);
        setStats(statsRes.data);
        setHealth(healthRes.data.status || 'ok');
      } catch (err) {
        message.error('Failed to load dashboard data');
      } finally {
        setLoading(false);
      }
    };
    fetch();
    const interval = setInterval(fetch, 10000);
    return () => clearInterval(interval);
  }, []);

  if (loading) return <Spin size="large" style={{ display: 'block', margin: '100px auto' }} />;

  return (
    <div>
      <Title level={3}>Dashboard</Title>
      <Row gutter={[16, 16]}>
        <Col xs={24} sm={12} lg={6}>
          <Card>
            <Statistic
              title="Health Status"
              value={health}
              prefix={health === 'ok' ? <CheckCircleOutlined style={{ color: '#52c41a' }} /> : <CloseCircleOutlined style={{ color: '#ff4d4f' }} />}
              valueStyle={{ color: health === 'ok' ? '#52c41a' : '#ff4d4f' }}
            />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card>
            <Statistic title="Total Requests" value={stats?.total_requests || 0} prefix={<ApiOutlined />} />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card>
            <Statistic
              title="Total Cost"
              value={stats?.total_cost || 0}
              precision={6}
              prefix={<DollarOutlined />}
              suffix="USD"
            />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card>
            <Statistic title="Providers" value={stats?.total_providers || 0} prefix={<ApiOutlined />} />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card>
            <Statistic
              title="Active Keys"
              value={stats?.active_keys || 0}
              prefix={<KeyOutlined />}
              valueStyle={{ color: '#52c41a' }}
            />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card>
            <Statistic
              title="Disabled Keys"
              value={stats?.disabled_keys || 0}
              prefix={<CloseCircleOutlined />}
              valueStyle={{ color: '#ff4d4f' }}
            />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card>
            <Statistic title="Input Tokens" value={stats?.total_input_tokens || 0} suffix="tokens" />
          </Card>
        </Col>
        <Col xs={24} sm={12} lg={6}>
          <Card>
            <Statistic title="Output Tokens" value={stats?.total_output_tokens || 0} suffix="tokens" />
          </Card>
        </Col>
      </Row>
    </div>
  );
};

export default Dashboard;
