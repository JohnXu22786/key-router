import React, { useEffect, useState } from 'react';
import { Card, Typography, Spin, message, Segmented, Select, Space, Table, Tag } from 'antd';
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend } from 'recharts';
import { getActivity, ActivityResponse, ActivityGroupSummary } from '../api/client';
import { DateRange, fmtUSD, fmtTokens, fmtCompact, CHART_COLORS, GRID, AXIS } from './Activity';

const { Text } = Typography;

interface ExploreProps { range: DateRange; }

const METRICS = [
  { key: 'spend', label: 'Total Usage ($)' },
  { key: 'tokens', label: 'Total Tokens' },
  { key: 'requests', label: 'Request Count' },
  { key: 'cache', label: 'Cache Hits' },
];

const GROUP_BY = [
  { value: 'model', label: 'Model' },
  { value: 'key', label: 'API Key' },
  { value: 'app', label: 'App' },
];

const ROLLUP = [
  { value: 'day', label: 'Daily' },
  { value: 'hour', label: 'Hourly' },
];

// OpenRouter's Explore table: Min | Max | Avg | Sum | Value | % of Total
const fmtFor = (metric: string) => (v: number) =>
  metric === 'spend' ? fmtUSD(v) : metric === 'tokens' || metric === 'cache' ? fmtTokens(v) : fmtCompact(v);

function toChartData(resp: ActivityResponse): Array<Record<string, any>> {
  const groups = Array.from(new Set(resp.series.map(s => s.group)));
  const data: Array<Record<string, any>> = resp.buckets.map(b => {
    const row: Record<string, any> = { label: b };
    groups.forEach(g => { row[g] = 0; });
    return row;
  });
  const bucketIdx = new Map(resp.buckets.map((b, i) => [b, i]));
  for (const p of resp.series) {
    const i = bucketIdx.get(p.bucket);
    if (i !== undefined) data[i][p.group] = p.value;
  }
  return data;
}

const ActivityExplore: React.FC<ExploreProps> = ({ range }) => {
  const [metric, setMetric] = useState('spend');
  const [groupBy, setGroupBy] = useState('model');
  const [rollup, setRollup] = useState('day');
  const [topN, setTopN] = useState(10);
  const [expanded, setExpanded] = useState(false);
  const [data, setData] = useState<ActivityResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const fetch = async () => {
      setLoading(true);
      setError(false);
      try {
        const res = await getActivity({
          metric,
          group_by: groupBy,
          rollup,
          since: range.since.toISOString(),
          until: range.until.toISOString(),
        });
        if (cancelled) return;
        setData(res.data);
      } catch { if (!cancelled) { setError(true); message.error('Failed to load explore'); } }
      finally { if (!cancelled) setLoading(false); }
    };
    fetch();
    return () => { cancelled = true; };
  }, [range, metric, groupBy, rollup]);

  if (loading) return <Spin style={{ display: 'block', margin: '60px auto' }} />;
  if (error || !data) {
    return <Card><Text type="danger">Failed to load explore — check the log file.</Text></Card>;
  }

  const fmt = fmtFor(metric);
  const chartData = toChartData(data);
  const groups = Array.from(new Set(data.series.map(s => s.group))).slice(0, topN);
  const summary: ActivityGroupSummary[] = expanded ? data.summary : data.summary.slice(0, topN);

  return (
    <div>
      {/* Control row: metric | group by | rollup | top N | rank | expand */}
      <Space wrap style={{ marginBottom: 16 }}>
        <Select value={metric} onChange={setMetric} style={{ width: 180 }}
          options={METRICS.map(m => ({ value: m.key, label: m.label }))} />
        <Select value={groupBy} onChange={setGroupBy} style={{ width: 130 }}
          options={GROUP_BY} />
        <Select value={rollup} onChange={setRollup} style={{ width: 120 }}
          options={ROLLUP} />
        <Text type="secondary">Rollup: <Select value={rollup} onChange={setRollup} style={{ width: 110 }}
          options={ROLLUP} /></Text>
        <Text type="secondary">Top <Select value={topN} onChange={setTopN} style={{ width: 80 }}
          options={[5, 10, 15, 20].map(n => ({ value: n, label: String(n) }))} /></Text>
        <Text type="secondary">Rank by: <Tag color="default">Current metric</Tag></Text>
        <Text type="secondary" style={{ cursor: 'pointer' }} onClick={() => setExpanded(!expanded)}>
          {expanded ? 'Collapse ▲' : 'Expand ▼'}
        </Text>
      </Space>

      {/* Stacked area chart of the top groups */}
      <Card style={{ borderRadius: 12, marginBottom: 16 }}>
        <ResponsiveContainer width="100%" height={300}>
          <AreaChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
            <defs>
              {groups.map((g, i) => (
                <linearGradient key={g} id={`ex-grad-${i}`} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={CHART_COLORS[i % CHART_COLORS.length]} stopOpacity={0.35} />
                  <stop offset="100%" stopColor={CHART_COLORS[i % CHART_COLORS.length]} stopOpacity={0.02} />
                </linearGradient>
              ))}
            </defs>
            <CartesianGrid strokeDasharray="3 3" stroke={GRID} vertical={false} />
            <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={24} />
            <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={64} tickFormatter={fmt} />
            <Tooltip
              formatter={(v: any, name: any) => [fmt(Number(v)), String(name)]}
              labelStyle={{ color: AXIS }}
              contentStyle={{ borderRadius: 8, border: '1px solid ' + GRID }}
            />
            <Legend wrapperStyle={{ fontSize: 12 }} />
            {groups.map((g, i) => (
              <Area
                key={g}
                type="monotone"
                dataKey={g}
                stackId="1"
                stroke={CHART_COLORS[i % CHART_COLORS.length]}
                fill={`url(#ex-grad-${i})`}
                strokeWidth={1.5}
                dot={false}
              />
            ))}
          </AreaChart>
        </ResponsiveContainer>
      </Card>

      {/* Summary table: Min | Max | Avg | Sum | Value | % of Total */}
      <Card style={{ borderRadius: 12 }}>
        <Table<ActivityGroupSummary>
          dataSource={summary}
          rowKey="group"
          size="small"
          pagination={false}
          columns={[
            { title: 'Group', dataIndex: 'group', key: 'group' },
            { title: 'Min', dataIndex: 'min', key: 'min', align: 'right', width: 110, render: (v: number) => fmt(v) },
            { title: 'Max', dataIndex: 'max', key: 'max', align: 'right', width: 110, render: (v: number) => fmt(v) },
            { title: 'Avg', dataIndex: 'avg', key: 'avg', align: 'right', width: 110, render: (v: number) => fmt(v) },
            { title: 'Sum', dataIndex: 'sum', key: 'sum', align: 'right', width: 120, render: (v: number) => <Text strong>{fmt(v)}</Text> },
            { title: 'Value', dataIndex: 'value', key: 'value', align: 'right', width: 110, render: (v: number) => fmt(v) },
            { title: '% of Total', dataIndex: 'percent', key: 'percent', align: 'right', width: 100, render: (v: number) => `${v.toFixed(1)}%` },
          ]}
        />
        <Text type="secondary" style={{ fontSize: 12, marginTop: 8, display: 'block' }}>
          {data.summary.length} rows · {data.buckets.length} {rollup === 'day' ? 'days' : 'hours'}
        </Text>
      </Card>
    </div>
  );
};

export default ActivityExplore;
