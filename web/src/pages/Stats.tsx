import React, { useEffect, useState, useCallback } from 'react';
import {
  Card, Table, Typography, Spin, message, Row, Col, Statistic, Button,
} from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { getConsumptions, getOverview, Consumption, OverviewStats } from '../api/client';
import dayjs from 'dayjs';

const { Title } = Typography;

const Stats: React.FC = () => {
  const [consumptions, setConsumptions] = useState<Consumption[]>([]);
  const [overview, setOverview] = useState<OverviewStats | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

  const fetch = useCallback(async (isRefresh = false) => {
    if (isRefresh) setRefreshing(true);
    try {
      const [consRes, ovRes] = await Promise.all([
        getConsumptions({ since: dayjs().subtract(7, 'day').toISOString() }),
        getOverview(),
      ]);
      setConsumptions(consRes.data);
      setOverview(ovRes.data);
    } catch { message.error('Failed to load stats'); }
    finally { setLoading(false); setRefreshing(false); }
  }, []);

  useEffect(() => { fetch(); }, [fetch]);

  if (loading) return <Spin size="large" style={{ display: 'block', margin: '100px auto' }} />;

  const totalTokens = (overview?.total_input_tokens || 0) + (overview?.total_output_tokens || 0);

  return (
    <div>
      <Title level={3}>
        Statistics
        <Button icon={<ReloadOutlined />} loading={refreshing} onClick={() => fetch(true)} style={{ marginLeft: 12 }}>
          Refresh
        </Button>
      </Title>
      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        <Col span={8}><Card><Statistic title="Total Cost" value={overview?.total_cost || 0} precision={6} prefix="$" /></Card></Col>
        <Col span={8}><Card><Statistic title="Total Tokens" value={totalTokens} /></Card></Col>
        <Col span={8}><Card><Statistic title="Total Requests" value={overview?.total_requests || 0} /></Card></Col>
      </Row>

      <Card title="Recent Consumption (Last 7 Days)">
        <Table
          dataSource={consumptions}
          columns={[
            {
              title: 'Time', dataIndex: 'hour_bucket', key: 'hour_bucket', width: 180,
              render: (v: string) => dayjs(v).format('YYYY-MM-DD HH:00'),
            },
            { title: 'Key ID', dataIndex: 'key_id', key: 'key_id', width: 70 },
            { title: 'Requests', dataIndex: 'request_count', key: 'request_count', width: 90 },
            { title: 'Input Tokens', dataIndex: 'input_tokens', key: 'input_tokens', width: 120 },
            { title: 'Output Tokens', dataIndex: 'output_tokens', key: 'output_tokens', width: 120 },
            { title: 'Cache Hit', dataIndex: 'cache_hit_tokens', key: 'cache_hit_tokens', width: 100 },
            { title: 'Cost', dataIndex: 'cost_usd', key: 'cost_usd', width: 120, render: (c: number) => `$${c.toFixed(6)}` },
          ]}
          rowKey="id"
          size="small"
          pagination={{ pageSize: 20 }}
        />
      </Card>
    </div>
  );
};

export default Stats;
