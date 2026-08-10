import React, { useEffect, useState, useCallback, useMemo } from 'react';
import {
  Card, Table, Typography, Spin, message, Row, Col, Button, Segmented, Space,
} from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import {
  AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
} from 'recharts';
import { getConsumptions, Consumption } from '../api/client';
import dayjs from 'dayjs';

const { Title, Text } = Typography;

// ---- number formatting (OpenRouter-style: $41k, $15.9, 341B tok) ----
const fmtCompact = (v: number): string => {
  const abs = Math.abs(v);
  if (abs >= 1e12) return (v / 1e12).toFixed(2).replace(/\.?0+$/, '') + 'T';
  if (abs >= 1e9) return (v / 1e9).toFixed(2).replace(/\.?0+$/, '') + 'B';
  if (abs >= 1e6) return (v / 1e6).toFixed(2).replace(/\.?0+$/, '') + 'M';
  if (abs >= 1e3) return (v / 1e3).toFixed(1).replace(/\.?0$/, '') + 'k';
  return String(Math.round(v));
};

const fmtUSD = (v: number): string => {
  const abs = Math.abs(v);
  if (abs >= 1e9) return `$${(v / 1e9).toFixed(2)}B`;
  if (abs >= 1e6) return `$${(v / 1e6).toFixed(2)}M`;
  if (abs >= 1e3) return `$${(v / 1e3).toFixed(1)}k`;
  return `$${v.toFixed(2)}`;
};

const fmtTokens = (v: number): string => fmtCompact(v) + ' tok';

// ---- palette (OpenRouter brand refresh: bright accent + deep neutral) ----
const ACCENT = '#6d5cff';       // primary violet
const ACCENT_SOFT = '#a89bff';
const GRID = 'rgba(120,120,140,0.14)';
const AXIS = 'rgba(120,120,140,0.75)';

const RANGES = [
  { label: '7 days', days: 7 },
  { label: '30 days', days: 30 },
  { label: '90 days', days: 90 },
];

const METRICS = [
  { key: 'cost', label: 'Spend', dataKey: 'cost', color: ACCENT, fmt: fmtUSD },
  { key: 'tokens', label: 'Tokens', dataKey: 'tokens', color: '#22c1a3', fmt: fmtTokens },
  { key: 'requests', label: 'Requests', dataKey: 'requests', color: '#ffb020', fmt: (v: number) => String(v) },
];

// ---- per-key table aggregation with Max/Avg/Min/Sum (OpenRouter style) ----
interface KeyRow { key_id: number; name: string; max: number; avg: number; min: number; sum: number; }

const Stats: React.FC = () => {
  const [consumptions, setConsumptions] = useState<Consumption[]>([]);
  const [prevConsumptions, setPrevConsumptions] = useState<Consumption[]>([]);
  const [rangeIdx, setRangeIdx] = useState(0);
  const [metric, setMetric] = useState('cost');
  const [selectedKey, setSelectedKey] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState(false);

  const range = RANGES[rangeIdx];

  const fetch = useCallback(async (isRefresh = false) => {
    if (isRefresh) setRefreshing(true);
    else setLoading(true);
    setError(false);
    try {
      const days = RANGES[rangeIdx].days;
      const now = dayjs();
      const [curRes, prevRes] = await Promise.all([
        getConsumptions({ since: now.subtract(days, 'day').toISOString(), until: now.toISOString() }),
        getConsumptions({
          since: now.subtract(2 * days, 'day').toISOString(),
          until: now.subtract(days, 'day').toISOString(),
        }),
      ]);
      setConsumptions(curRes.data);
      setPrevConsumptions(prevRes.data);
      setSelectedKey(null);
    } catch { setError(true); message.error('Failed to load stats'); }
    finally { setLoading(false); setRefreshing(false); }
  }, [rangeIdx]);

  useEffect(() => { fetch(); }, [fetch]);

  // ---- daily aggregation for the main chart (current period only; the
  // previous-period comparison lives in the KPI delta chips) ----
  const daily = useMemo(() => {
    const acc = new Map<string, { date: string; sort: string; cost: number; tokens: number; requests: number }>();
    const push = (c: Consumption) => {
      const s = dayjs(c.hour_bucket).format('YYYY-MM-DD');
      const d = acc.get(s) || { date: dayjs(c.hour_bucket).format('MM-DD'), sort: s, cost: 0, tokens: 0, requests: 0 };
      d.cost += c.cost_usd;
      d.tokens += c.input_tokens + c.output_tokens;
      d.requests += c.request_count;
      acc.set(s, d);
    };
    consumptions.forEach(push);
    return [...acc.values()].sort((a, b) => a.sort.localeCompare(b.sort));
  }, [consumptions]);

  const chartData = useMemo(() => {
    if (selectedKey == null) return daily;
    const acc = new Map<string, { date: string; sort: string; cost: number; tokens: number; requests: number }>();
    for (const c of consumptions) {
      if (c.key_id !== selectedKey) continue;
      const s = dayjs(c.hour_bucket).format('YYYY-MM-DD');
      const d = acc.get(s) || { date: dayjs(c.hour_bucket).format('MM-DD'), sort: s, cost: 0, tokens: 0, requests: 0 };
      d.cost += c.cost_usd;
      d.tokens += c.input_tokens + c.output_tokens;
      d.requests += c.request_count;
      acc.set(s, d);
    }
    return [...acc.values()].sort((a, b) => a.sort.localeCompare(b.sort));
  }, [daily, consumptions, selectedKey]);

  // ---- KPI deltas (current vs previous period) ----
  const sum = (list: Consumption[]) => list.reduce((a, c) => ({
    cost: a.cost + c.cost_usd,
    tokens: a.tokens + c.input_tokens + c.output_tokens,
    requests: a.requests + c.request_count,
    cacheHit: a.cacheHit + c.cache_hit_tokens,
    input: a.input + c.input_tokens,
  }), { cost: 0, tokens: 0, requests: 0, cacheHit: 0, input: 0 });

  const cur = sum(consumptions);
  const prev = sum(prevConsumptions);
  const delta = (curV: number, prevV: number) =>
    prevV > 0 ? ((curV - prevV) / prevV) * 100 : (curV > 0 ? 100 : 0);

  const kpis = [
    { label: 'Total Spend', value: fmtUSD(cur.cost), delta: delta(cur.cost, prev.cost) },
    { label: 'Requests', value: fmtCompact(cur.requests), delta: delta(cur.requests, prev.requests) },
    { label: 'Token Volume', value: fmtTokens(cur.tokens), delta: delta(cur.tokens, prev.tokens) },
    {
      label: 'Cache Hit Rate',
      value: `${(((cur.cacheHit) / Math.max(1, cur.input + cur.cacheHit)) * 100).toFixed(1)}%`,
      delta: delta(cur.cacheHit, prev.cacheHit),
    },
  ];

  // ---- per-key table ----
  const keyRows: KeyRow[] = useMemo(() => {
    const acc = new Map<number, { key_id: number; name: string; values: number[]; sum: number }>();
    for (const c of consumptions) {
      const v = metric === 'cost' ? c.cost_usd : metric === 'tokens' ? c.input_tokens + c.output_tokens : c.request_count;
      const row = acc.get(c.key_id) || { key_id: c.key_id, name: `Key #${c.key_id}`, values: [], sum: 0 };
      row.values.push(v);
      row.sum += v;
      acc.set(c.key_id, row);
    }
    const rows = [...acc.values()].map(r => ({
      key_id: r.key_id,
      name: r.name,
      max: Math.max(...r.values),
      avg: r.values.length ? r.sum / r.values.length : 0,
      min: Math.min(...r.values),
      sum: r.sum,
    }));
    rows.sort((a, b) => b.sum - a.sum);
    return rows;
  }, [consumptions, metric]);

  if (loading) return <Spin size="large" style={{ display: 'block', margin: '100px auto' }} />;

  const activeMetric = METRICS.find(m => m.key === metric)!;
  const fmtValue = activeMetric.fmt;

  const metricY = (v: number) => (metric === 'cost' ? fmtUSD(v) : metric === 'tokens' ? fmtTokens(v) : String(v));

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16, flexWrap: 'wrap', gap: 8 }}>
        <Title level={3} style={{ margin: 0 }}>Statistics</Title>
        <Space wrap>
          <Segmented
            options={RANGES.map((r, i) => ({ label: r.label, value: i }))}
            value={rangeIdx}
            onChange={(v) => setRangeIdx(v as number)}
          />
          <Segmented
            options={METRICS.map(m => ({ label: m.label, value: m.key }))}
            value={metric}
            onChange={(v) => setMetric(v as string)}
          />
          <Button icon={<ReloadOutlined />} loading={refreshing} onClick={() => fetch(true)}>
            Refresh
          </Button>
        </Space>
      </div>

      {error && (
        <Card style={{ marginBottom: 16 }}><Text type="danger">Failed to load stats — check the log file.</Text></Card>
      )}

      {/* KPI cards with "vs prev period" deltas (OpenRouter Overview style) */}
      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        {kpis.map(k => (
          <Col xs={12} sm={6} key={k.label}>
            <Card size="small" style={{ borderRadius: 12 }}>
              <Text type="secondary" style={{ fontSize: 12 }}>{k.label}</Text>
              <div style={{ fontSize: 22, fontWeight: 600, margin: '4px 0' }}>{k.value}</div>
              <Text style={{ fontSize: 12, color: k.delta >= 0 ? '#22c1a3' : '#ff5f6d' }}>
                {k.delta >= 0 ? '▲' : '▼'} {Math.abs(k.delta).toFixed(1)}% <Text type="secondary" style={{ fontSize: 12 }}>vs prev period</Text>
              </Text>
            </Card>
          </Col>
        ))}
      </Row>

      {/* Main chart: single-series area, minimal gridlines, gradient fill */}
      <Card
        style={{ borderRadius: 12, marginBottom: 16 }}
        title={
          <Space>
            <span>{activeMetric.label} over time</span>
            {selectedKey != null && (
              <Button size="small" onClick={() => setSelectedKey(null)}>Clear key filter ✕</Button>
            )}
          </Space>
        }
        extra={<Text type="secondary" style={{ fontSize: 12 }}>{range.label} · daily</Text>}
      >
        <ResponsiveContainer width="100%" height={300}>
          <AreaChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
            <defs>
              <linearGradient id="metricFill" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor={activeMetric.color} stopOpacity={0.35} />
                <stop offset="100%" stopColor={activeMetric.color} stopOpacity={0.02} />
              </linearGradient>
            </defs>
            <CartesianGrid strokeDasharray="3 3" stroke={GRID} vertical={false} />
            <XAxis
              dataKey="date"
              tick={{ fill: AXIS, fontSize: 11 }}
              tickLine={false}
              axisLine={false}
              interval="preserveStartEnd"
              minTickGap={24}
            />
            <YAxis
              tick={{ fill: AXIS, fontSize: 11 }}
              tickLine={false}
              axisLine={false}
              width={64}
              tickFormatter={metricY}
            />
            <Tooltip
              formatter={(v) => [fmtValue(Number(v)), activeMetric.label]}
              labelStyle={{ color: AXIS }}
              contentStyle={{ borderRadius: 8, border: '1px solid ' + GRID }}
            />
            <Area
              type="monotone"
              dataKey={metric}
              stroke={activeMetric.color}
              strokeWidth={2}
              fill="url(#metricFill)"
              dot={false}
              activeDot={{ r: 4 }}
            />
          </AreaChart>
        </ResponsiveContainer>
        {selectedKey != null && (
          <Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: 8 }}>
            Chart filtered to {keyRows.find(r => r.key_id === selectedKey)?.name}.
          </Text>
        )}
      </Card>

      {/* Per-key breakdown table with Max/Avg/Min/Sum, click row to filter chart */}
      <Card
        style={{ borderRadius: 12 }}
        title={`${activeMetric.label} by Key`}
        extra={<Text type="secondary" style={{ fontSize: 12 }}>Click a row to filter the chart</Text>}
      >
        <Table<KeyRow>
          dataSource={keyRows}
          rowKey="key_id"
          size="small"
          pagination={false}
          rowClassName={(r) => r.key_id === selectedKey ? 'ant-table-row-selected' : ''}
          onRow={(r) => ({
            onClick: () => setSelectedKey(selectedKey === r.key_id ? null : r.key_id),
            style: { cursor: 'pointer' },
          })}
          columns={[
            { title: 'Key', dataIndex: 'name', key: 'name' },
            {
              title: 'Max', dataIndex: 'max', key: 'max', align: 'right', width: 120,
              render: (v: number) => fmtValue(v),
            },
            {
              title: 'Avg', dataIndex: 'avg', key: 'avg', align: 'right', width: 120,
              render: (v: number) => fmtValue(v),
            },
            {
              title: 'Min', dataIndex: 'min', key: 'min', align: 'right', width: 120,
              render: (v: number) => fmtValue(v),
            },
            {
              title: 'Sum', dataIndex: 'sum', key: 'sum', align: 'right', width: 140,
              render: (v: number) => <Text strong>{fmtValue(v)}</Text>,
            },
          ]}
        />
      </Card>
    </div>
  );
};

export default Stats;
