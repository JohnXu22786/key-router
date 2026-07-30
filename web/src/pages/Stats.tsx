import React, { useEffect, useState, useCallback } from 'react';
import {
  Card, Table, Typography, Spin, message, Row, Col, Statistic, Button, Tabs,
} from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import {
  LineChart, Line, BarChart, Bar, PieChart, Pie, Cell,
  XAxis, YAxis, CartesianGrid, Tooltip, Legend, ResponsiveContainer,
} from 'recharts';
import { getConsumptions, getOverview, Consumption, OverviewStats } from '../api/client';
import dayjs from 'dayjs';

const { Title } = Typography;
const COLORS = ['#0088FE', '#00C49F', '#FFBB28', '#FF8042', '#8884d8', '#82ca9d'];

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

  // Aggregate consumption by day
  const dailyData = consumptions.reduce<Record<string, { date: string; input: number; output: number; cost: number; requests: number }>>((acc, c) => {
    const date = dayjs(c.hour_bucket).format('MM-DD');
    if (!acc[date]) acc[date] = { date, input: 0, output: 0, cost: 0, requests: 0 };
    acc[date].input += c.input_tokens;
    acc[date].output += c.output_tokens;
    acc[date].cost += c.cost_usd;
    acc[date].requests += c.request_count;
    return acc;
  }, {});
  const dailyChart = Object.values(dailyData).sort((a, b) => a.date.localeCompare(b.date));

  // Aggregate by key
  const keyData = consumptions.reduce<Record<string, { name: string; tokens: number; cost: number }>>((acc, c) => {
    const name = `Key #${c.key_id}`;
    if (!acc[name]) acc[name] = { name, tokens: 0, cost: 0 };
    acc[name].tokens += c.input_tokens + c.output_tokens;
    acc[name].cost += c.cost_usd;
    return acc;
  }, {});
  const keyChart = Object.values(keyData);

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
        <Col xs={12} sm={6}><Card size="small"><Statistic title="Total Cost" value={overview?.total_cost || 0} precision={6} prefix="$" /></Card></Col>
        <Col xs={12} sm={6}><Card size="small"><Statistic title="Total Tokens" value={totalTokens} /></Card></Col>
        <Col xs={12} sm={6}><Card size="small"><Statistic title="Total Requests" value={overview?.total_requests || 0} /></Card></Col>
        <Col xs={12} sm={6}><Card size="small"><Statistic title="Active Keys" value={overview?.active_keys || 0} suffix={`/ ${overview?.total_keys || 0}`} /></Card></Col>
      </Row>

      <Tabs defaultActiveKey="cost" items={[
        {
          key: 'cost',
          label: 'Daily Cost',
          children: (
            <Card>
              <ResponsiveContainer width="100%" height={300}>
                <LineChart data={dailyChart}>
                  <CartesianGrid strokeDasharray="3 3" />
                  <XAxis dataKey="date" />
                  <YAxis tickFormatter={(v) => `$${Number(v).toFixed(4)}`} />
                  <Tooltip formatter={(v) => `$${Number(v).toFixed(6)}`} />
                  <Legend />
                  <Line type="monotone" dataKey="cost" stroke="#8884d8" name="Cost (USD)" strokeWidth={2} />
                </LineChart>
              </ResponsiveContainer>
            </Card>
          ),
        },
        {
          key: 'tokens',
          label: 'Token Usage',
          children: (
            <Card>
              <ResponsiveContainer width="100%" height={300}>
                <BarChart data={dailyChart}>
                  <CartesianGrid strokeDasharray="3 3" />
                  <XAxis dataKey="date" />
                  <YAxis />
                  <Tooltip />
                  <Legend />
                  <Bar dataKey="input" stackId="a" fill="#0088FE" name="Input Tokens" />
                  <Bar dataKey="output" stackId="a" fill="#00C49F" name="Output Tokens" />
                </BarChart>
              </ResponsiveContainer>
            </Card>
          ),
        },
        {
          key: 'requests',
          label: 'Requests',
          children: (
            <Card>
              <ResponsiveContainer width="100%" height={300}>
                <BarChart data={dailyChart}>
                  <CartesianGrid strokeDasharray="3 3" />
                  <XAxis dataKey="date" />
                  <YAxis />
                  <Tooltip />
                  <Legend />
                  <Bar dataKey="requests" fill="#FFBB28" name="Requests" />
                </BarChart>
              </ResponsiveContainer>
            </Card>
          ),
        },
        {
          key: 'keys',
          label: 'By Key',
          children: (
            <Row gutter={16}>
              <Col xs={24} md={12}>
                <Card title="Token Distribution">
                  <ResponsiveContainer width="100%" height={300}>
                    <PieChart>
                      <Pie data={keyChart} dataKey="tokens" nameKey="name" cx="50%" cy="50%" outerRadius={100} label>
                        {keyChart.map((_, idx) => <Cell key={idx} fill={COLORS[idx % COLORS.length]} />)}
                      </Pie>
                      <Tooltip />
                    </PieChart>
                  </ResponsiveContainer>
                </Card>
              </Col>
              <Col xs={24} md={12}>
                <Card title="Cost by Key">
                  <ResponsiveContainer width="100%" height={300}>
                    <PieChart>
                      <Pie data={keyChart} dataKey="cost" nameKey="name" cx="50%" cy="50%" outerRadius={100} label>
                        {keyChart.map((_, idx) => <Cell key={idx} fill={COLORS[idx % COLORS.length]} />)}
                      </Pie>
                      <Tooltip />
                    </PieChart>
                  </ResponsiveContainer>
                </Card>
              </Col>
            </Row>
          ),
        },
      ]} />

      <Card title="Recent Consumption (Last 7 Days)" style={{ marginTop: 16 }}>
        <Table
          dataSource={consumptions}
          columns={[
            { title: 'Time', dataIndex: 'hour_bucket', key: 'hour_bucket', width: 160, render: (v: string) => dayjs(v).format('MM-DD HH:00') },
            { title: 'Key ID', dataIndex: 'key_id', key: 'key_id', width: 70 },
            { title: 'Requests', dataIndex: 'request_count', key: 'request_count', width: 80 },
            { title: 'Input', dataIndex: 'input_tokens', key: 'input_tokens', width: 110 },
            { title: 'Output', dataIndex: 'output_tokens', key: 'output_tokens', width: 110 },
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
