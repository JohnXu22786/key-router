import React, { useEffect, useState, useCallback, useMemo } from 'react';
import {
  Card, Table, Typography, Spin, message, Row, Col, Button, Segmented, Space,
  Modal, Tabs,
} from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import {
  AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
  BarChart, Bar,
} from 'recharts';
import { getConsumptions, getOverview, getKeys, getProviders, Consumption, OverviewStats, Key, Provider } from '../api/client';
import { cacheHitRate } from './activityShared';
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
const ACCENT = '#6d5cff';
const GRID = 'rgba(120,120,140,0.14)';
const AXIS = 'rgba(120,120,140,0.75)';
const COLORS = ['#6d5cff', '#22c1a3', '#ffb020', '#ff5f6d', '#3b82f6', '#a855f7', '#14b8a6', '#f59e0b'];

// ---- time ranges: Today / 24h / 3d / 7d / 30d (OpenRouter-style selector) ----
const RANGES = [
  { key: 'today', label: 'Today', since: () => dayjs().startOf('day'), granularity: 'hour', granularityLabel: 'hourly' },
  { key: '24h', label: '24h', since: () => dayjs().subtract(24, 'hour'), granularity: 'hour', granularityLabel: 'hourly' },
  { key: '3d', label: '3 days', since: () => dayjs().subtract(3, 'day'), granularity: 'hour', granularityLabel: 'hourly' },
  { key: '7d', label: '7 days', since: () => dayjs().subtract(7, 'day'), granularity: 'day', granularityLabel: 'daily' },
  { key: '30d', label: '30 days', since: () => dayjs().subtract(30, 'day'), granularity: 'day', granularityLabel: 'daily' },
];

const METRICS = [
  { key: 'cost', label: 'Spend', color: ACCENT, fmt: fmtUSD },
  { key: 'tokens', label: 'Tokens', color: '#22c1a3', fmt: fmtTokens },
  { key: 'requests', label: 'Requests', color: '#ffb020', fmt: (v: number) => String(v) },
  { key: 'cache', label: 'Cache Hits', color: '#14b8a6', fmt: fmtTokens },
];

interface KeyRow { key_id: number; name: string; max: number; avg: number; min: number; sum: number; }

const Stats: React.FC = () => {
  const [consumptions, setConsumptions] = useState<Consumption[]>([]);
  const [prevConsumptions, setPrevConsumptions] = useState<Consumption[]>([]);
  const [overview, setOverview] = useState<OverviewStats | null>(null);
  const [keys, setKeys] = useState<Key[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [rangeIdx, setRangeIdx] = useState(3); // default 7 days
  const [metric, setMetric] = useState('cost');
  const [selectedKey, setSelectedKey] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState(false);
  // Detail modal: { metric, name } opens a per-dimension breakdown
  const [detail, setDetail] = useState<{ title: string; keyId?: number } | null>(null);

  const range = RANGES[rangeIdx];

  const fetch = useCallback(async (isRefresh = false) => {
    if (isRefresh) setRefreshing(true);
    else setLoading(true);
    setError(false);
    try {
      const r = RANGES[rangeIdx];
      const since = r.since().toISOString();
      const now = dayjs().toISOString();
      // Previous period of equal length (for KPI deltas)
      const prevLen = dayjs(now).diff(since, 'millisecond');
      const prevSince = dayjs(since).subtract(prevLen, 'millisecond').toISOString();
      const [curRes, prevRes, ovRes, keyRes, provRes] = await Promise.all([
        getConsumptions({ since, until: now }),
        getConsumptions({ since: prevSince, until: since }),
        getOverview(),
        getKeys(),
        getProviders(),
      ]);
      setConsumptions(curRes.data);
      setPrevConsumptions(prevRes.data);
      setOverview(ovRes.data);
      setKeys(keyRes.data);
      setProviders(provRes.data);
      setSelectedKey(null);
    } catch { setError(true); message.error('Failed to load stats'); }
    finally { setLoading(false); setRefreshing(false); }
  }, [rangeIdx]);

  useEffect(() => { fetch(); }, [fetch]);

  const keyName = useCallback((id: number) => {
    const k = keys.find(x => x.id === id);
    if (k?.name) return k.name;
    return `Key #${id}`;
  }, [keys]);

  // ---- bucket key: hour or day depending on range granularity ----
  const bucketKey = useCallback((c: Consumption) => {
    if (range.granularity === 'hour') return dayjs(c.hour_bucket).format('YYYY-MM-DD HH:00');
    return dayjs(c.hour_bucket).format('YYYY-MM-DD');
  }, [range.granularity]);

  const bucketLabel = useCallback((k: string) => {
    if (range.granularity === 'hour') return dayjs(k).format('MM-DD HH:00');
    return dayjs(k).format('MM-DD');
  }, [range.granularity]);

  // ---- aggregate consumptions into time buckets ----
  const series = useMemo(() => {
    const acc = new Map<string, { t: string; cost: number; tokens: number; requests: number; cache: number }>();
    const push = (c: Consumption) => {
      const k = bucketKey(c);
      const d = acc.get(k) || { t: k, cost: 0, tokens: 0, requests: 0, cache: 0 };
      d.cost += c.cost_usd;
      d.tokens += c.input_tokens + c.output_tokens;
      d.requests += c.request_count;
      d.cache += c.cache_hit_tokens;
      acc.set(k, d);
    };
    for (const c of consumptions) {
      if (selectedKey != null && c.key_id !== selectedKey) continue;
      push(c);
    }
    return [...acc.values()].sort((a, b) => a.t.localeCompare(b.t)).map(d => ({ ...d, label: bucketLabel(d.t) }));
  }, [consumptions, selectedKey, bucketKey, bucketLabel]);

  // ---- KPI deltas (current vs previous period) ----
  const sum = (list: Consumption[]) => list.reduce((a, c) => ({
    cost: a.cost + c.cost_usd,
    tokens: a.tokens + c.input_tokens + c.output_tokens,
    requests: a.requests + c.request_count,
    cache: a.cache + c.cache_hit_tokens,
    input: a.input + c.input_tokens,
  }), { cost: 0, tokens: 0, requests: 0, cache: 0, input: 0 });

  const cur = sum(consumptions);
  const prev = sum(prevConsumptions);
  const delta = (curV: number, prevV: number) =>
    prevV > 0 ? ((curV - prevV) / prevV) * 100 : (curV > 0 ? 100 : 0);

  // Cache hit rate = cached / total input tokens (incl. cached); the delta
  // compares the RATE against the previous period, like the Activity page.
  const curRate = cacheHitRate(cur.input, cur.cache);
  const prevRate = cacheHitRate(prev.input, prev.cache);

  const kpis = [
    { label: 'Total Spend', value: fmtUSD(cur.cost), delta: delta(cur.cost, prev.cost) },
    { label: 'Requests', value: fmtCompact(cur.requests), delta: delta(cur.requests, prev.requests) },
    { label: 'Token Volume', value: fmtTokens(cur.tokens), delta: delta(cur.tokens, prev.tokens) },
    {
      label: 'Cache Hit Rate',
      value: `${curRate.toFixed(1)}%`,
      delta: delta(curRate, prevRate),
    },
  ];

  // ---- per-key table (Max / Avg / Min) ----
  const keyRows: KeyRow[] = useMemo(() => {
    // Consumption rows are keyed per (key, hour, model, app), so a key serving
    // several models (or apps) in one hour yields multiple rows for the same
    // hour. Coalesce them back into ONE value per (key, hour) before taking
    // Max/Avg/Min — otherwise avg divides by the model count and max/min
    // narrow to a single model's share of the hour. Hours are the unit (not
    // the chart's day buckets), matching the pre-split behavior of one row per
    // key+hour so the numbers keep their old meaning on every range.
    const byHour = new Map<string, { key_id: number; name: string; total: number }>();
    for (const c of consumptions) {
      const v = metric === 'cost' ? c.cost_usd : metric === 'tokens' ? c.input_tokens + c.output_tokens : metric === 'requests' ? c.request_count : c.cache_hit_tokens;
      const k = `${c.key_id}|${dayjs(c.hour_bucket).format('YYYY-MM-DD HH:00')}`;
      const e = byHour.get(k) || { key_id: c.key_id, name: keyName(c.key_id), total: 0 };
      e.total += v;
      byHour.set(k, e);
    }
    const byKey = new Map<number, { key_id: number; name: string; hourTotals: number[]; sum: number }>();
    for (const e of byHour.values()) {
      const row = byKey.get(e.key_id) || { key_id: e.key_id, name: e.name, hourTotals: [], sum: 0 };
      row.hourTotals.push(e.total);
      row.sum += e.total;
      byKey.set(e.key_id, row);
    }
    const rows = [...byKey.values()].map(r => ({
      key_id: r.key_id,
      name: r.name,
      max: Math.max(...r.hourTotals),
      avg: r.hourTotals.length ? r.sum / r.hourTotals.length : 0,
      min: Math.min(...r.hourTotals),
      sum: r.sum,
    }));
    rows.sort((a, b) => b.sum - a.sum);
    return rows;
  }, [consumptions, metric, keyName]);

  // ---- per-provider breakdown (Explore-style) ----
  const providerRows = useMemo(() => {
    const acc = new Map<number, { provider_id: number; name: string; cost: number; tokens: number; requests: number }>();
    for (const c of consumptions) {
      const k = keys.find(x => x.id === c.key_id);
      const pid = k?.provider_id ?? 0;
      const row = acc.get(pid) || { provider_id: pid, name: providers.find(p => p.id === pid)?.name || (pid ? `Provider #${pid}` : 'Unknown'), cost: 0, tokens: 0, requests: 0 };
      row.cost += c.cost_usd;
      row.tokens += c.input_tokens + c.output_tokens;
      row.requests += c.request_count;
      acc.set(pid, row);
    }
    return [...acc.values()].sort((a, b) => b.cost - a.cost);
  }, [consumptions, keys, providers]);

  const activeMetric = METRICS.find(m => m.key === metric)!;
  const fmtValue = activeMetric.fmt;
  const metricY = (v: number) => activeMetric.fmt(v);
  const metricDataKey = metric === 'cache' ? 'cache' : metric;

  if (loading) return <Spin size="large" style={{ display: 'block', margin: '100px auto' }} />;

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16, flexWrap: 'wrap', gap: 8 }}>
        <Title level={3} style={{ margin: 0 }}>Activity</Title>
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

      {/* KPI cards with "vs prev period" deltas */}
      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        {kpis.map(k => (
          <Col xs={12} sm={6} key={k.label}>
            <Card size="small" hoverable style={{ borderRadius: 12 }} onClick={() => setDetail({ title: k.label })}>
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
        extra={<Text type="secondary" style={{ fontSize: 12 }}>{range.label} · {range.granularityLabel}</Text>}
      >
        <ResponsiveContainer width="100%" height={300}>
          <AreaChart data={series} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
            <defs>
              <linearGradient id="metricFill" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor={activeMetric.color} stopOpacity={0.35} />
                <stop offset="100%" stopColor={activeMetric.color} stopOpacity={0.02} />
              </linearGradient>
            </defs>
            <CartesianGrid strokeDasharray="3 3" stroke={GRID} vertical={false} />
            <XAxis
              dataKey="label"
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
              dataKey={metricDataKey}
              stroke={activeMetric.color}
              strokeWidth={2}
              fill="url(#metricFill)"
              dot={false}
              activeDot={{ r: 4 }}
            />
          </AreaChart>
        </ResponsiveContainer>
      </Card>

      {/* Breakdown: per-key table (click row to filter chart) + per-provider */}
      <Row gutter={[16, 16]}>
        <Col xs={24} lg={14}>
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
                { title: 'Max', dataIndex: 'max', key: 'max', align: 'right', width: 100, render: (v: number) => fmtValue(v) },
                { title: 'Avg', dataIndex: 'avg', key: 'avg', align: 'right', width: 100, render: (v: number) => fmtValue(v) },
                { title: 'Min', dataIndex: 'min', key: 'min', align: 'right', width: 100, render: (v: number) => fmtValue(v) },
                { title: 'Sum', dataIndex: 'sum', key: 'sum', align: 'right', width: 110, render: (v: number) => <Text strong>{fmtValue(v)}</Text> },
              ]}
            />
          </Card>
        </Col>
        <Col xs={24} lg={10}>
          <Card
            style={{ borderRadius: 12 }}
            title="By Provider"
            extra={
              <Button size="small" onClick={() => setDetail({ title: 'By Provider' })}>Explore</Button>
            }
          >
            <Table
              dataSource={providerRows}
              rowKey="provider_id"
              size="small"
              pagination={false}
              columns={[
                { title: 'Provider', dataIndex: 'name', key: 'name' },
                { title: 'Cost', dataIndex: 'cost', key: 'cost', align: 'right', width: 100, render: (v: number) => fmtUSD(v) },
                { title: 'Req', dataIndex: 'requests', key: 'requests', align: 'right', width: 70, render: (v: number) => fmtCompact(v) },
              ]}
            />
          </Card>
        </Col>
      </Row>

      {/* Detail modal: per-dimension exploration (OpenRouter Explore-style) */}
      <Modal
        title={detail?.title}
        open={detail != null}
        onCancel={() => setDetail(null)}
        footer={null}
        width={760}
      >
        {detail && (
          <Tabs
            items={[
              {
                key: 'series',
                label: 'Time Series',
                children: (
                  <ResponsiveContainer width="100%" height={260}>
                    <BarChart data={series} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                      <CartesianGrid strokeDasharray="3 3" stroke={GRID} vertical={false} />
                      <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={20} />
                      <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={64} tickFormatter={metricY} />
                      <Tooltip formatter={(v) => [fmtValue(Number(v)), activeMetric.label]} contentStyle={{ borderRadius: 8 }} />
                      <Bar dataKey={metricDataKey} fill={activeMetric.color} radius={[3, 3, 0, 0]} maxBarSize={28} />
                    </BarChart>
                  </ResponsiveContainer>
                ),
              },
              {
                key: 'keys',
                label: 'By Key',
                children: (
                  <Table<KeyRow>
                    dataSource={keyRows}
                    rowKey="key_id"
                    size="small"
                    pagination={false}
                    columns={[
                      { title: 'Key', dataIndex: 'name', key: 'name' },
                      { title: 'Max', dataIndex: 'max', key: 'max', align: 'right', width: 110, render: (v: number) => fmtValue(v) },
                      { title: 'Avg', dataIndex: 'avg', key: 'avg', align: 'right', width: 110, render: (v: number) => fmtValue(v) },
                      { title: 'Min', dataIndex: 'min', key: 'min', align: 'right', width: 110, render: (v: number) => fmtValue(v) },
                      { title: 'Sum', dataIndex: 'sum', key: 'sum', align: 'right', width: 120, render: (v: number) => <Text strong>{fmtValue(v)}</Text> },
                    ]}
                  />
                ),
              },
              {
                key: 'provider',
                label: 'By Provider',
                children: (
                  <Table
                    dataSource={providerRows}
                    rowKey="provider_id"
                    size="small"
                    pagination={false}
                    columns={[
                      { title: 'Provider', dataIndex: 'name', key: 'name' },
                      { title: 'Cost', dataIndex: 'cost', key: 'cost', align: 'right', width: 110, render: (v: number) => fmtUSD(v) },
                      { title: 'Tokens', dataIndex: 'tokens', key: 'tokens', align: 'right', width: 120, render: (v: number) => fmtTokens(v) },
                      { title: 'Requests', dataIndex: 'requests', key: 'requests', align: 'right', width: 90, render: (v: number) => fmtCompact(v) },
                    ]}
                  />
                ),
              },
            ]}
          />
        )}
      </Modal>
    </div>
  );
};

export default Stats;
