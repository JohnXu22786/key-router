import React, { useEffect, useState } from 'react';
import { Card, Typography, Spin, message, Segmented, Select, Space, Table, Tag, Progress } from 'antd';
import { BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend } from 'recharts';
import { getActivity, ActivityResponse, ActivityGroupSummary } from '../api/client';
import { DateRange, fmtUSD, fmtTokens, fmtCompact, CHART_COLORS, OTHER_COLOR, GRID, AXIS, fmtPercent, fmt3sig } from './activityShared';

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

// Table formatter: 3 significant figures for spend, compact for counts.
const fmtForTable = (metric: string) => (v: number) =>
  metric === 'spend' ? fmt3sig(v) : metric === 'tokens' || metric === 'cache' ? fmtTokens(v) : fmtCompact(v);

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
          top: topN,
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
  }, [range, metric, groupBy, rollup, topN]);

  if (loading) return <Spin style={{ display: 'block', margin: '60px auto' }} />;
  if (error || !data) {
    return <Card><Text type="danger">Failed to load explore — check the log file.</Text></Card>;
  }

  const fmtAxis = (metric === 'spend' ? fmtUSD : metric === 'tokens' || metric === 'cache' ? fmtTokens : fmtCompact);
  const fmtTable = fmtForTable(metric);
  const chartData = toChartData(data);
  const groups = Array.from(new Set(data.series.map(s => s.group)));
  const summary: ActivityGroupSummary[] = data.summary.slice(0, topN);
  const metricLabel = METRICS.find(m => m.key === metric)!.label;
  const groupLabel = GROUP_BY.find(g => g.value === groupBy)!.label;
  const maxPercent = Math.max(...summary.map(s => s.percent), 1);

  return (
    <div>
      {/* Control row — OR style: "Total Usage ($) by Model | Rollup: Daily | Top 10 | Rank by: Current metric" */}
      <Space wrap style={{ marginBottom: 16 }}>
        <Select value={metric} onChange={setMetric} style={{ width: 170 }}
          options={METRICS.map(m => ({ value: m.key, label: m.label }))} />
        <Text type="secondary">by</Text>
        <Select value={groupBy} onChange={setGroupBy} style={{ width: 120 }}
          options={GROUP_BY} />
        <Text type="secondary">Rollup:</Text>
        <Select value={rollup} onChange={setRollup} style={{ width: 100 }}
          options={ROLLUP} />
        <Text type="secondary">Top</Text>
        <Select value={topN} onChange={setTopN} style={{ width: 80 }}
          options={[5, 10, 15, 20].map(n => ({ value: n, label: String(n) }))} />
        <Text type="secondary">Rank by: <Tag color="default">Current metric</Tag></Text>
      </Space>

      {/* Stacked BAR chart (OR uses bars, not area) */}
      <Card style={{ borderRadius: 12, marginBottom: 16 }}>
        <ResponsiveContainer width="100%" height={300}>
          <BarChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
            <CartesianGrid strokeDasharray="3 3" stroke={GRID} vertical={false} />
            <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={24} />
            <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={64} tickFormatter={fmtAxis} />
            <Tooltip
              formatter={(v: any, name: any) => [fmtTable(Number(v)), String(name)]}
              labelStyle={{ color: AXIS }}
              contentStyle={{ borderRadius: 8, border: '1px solid ' + GRID }}
            />
            <Legend wrapperStyle={{ fontSize: 12 }} />
            {groups.map((g, i) => {
              const isOther = g === 'Other';
              const last = i === groups.length - 1;
              return (
                <Bar
                  key={g}
                  dataKey={g}
                  stackId="1"
                  fill={isOther ? OTHER_COLOR : CHART_COLORS[i % CHART_COLORS.length]}
                  maxBarSize={14}
                  radius={last ? [2, 2, 0, 0] : [0, 0, 0, 0]}
                />
              );
            })}
          </BarChart>
        </ResponsiveContainer>
      </Card>

      {/* Summary table: Min | Max | Avg | Sum | Value | % of Total (3-sig-fig money, % bar) */}
      <Card style={{ borderRadius: 12 }}>
        <Table<ActivityGroupSummary>
          dataSource={summary}
          rowKey="group"
          size="small"
          pagination={false}
          columns={[
            { title: groupLabel, dataIndex: 'group', key: 'group' },
            { title: 'Min', dataIndex: 'min', key: 'min', align: 'right', width: 100, render: (v: number) => fmtTable(v) },
            { title: 'Max', dataIndex: 'max', key: 'max', align: 'right', width: 100, render: (v: number) => fmtTable(v) },
            { title: 'Avg', dataIndex: 'avg', key: 'avg', align: 'right', width: 100, render: (v: number) => fmtTable(v) },
            { title: 'Sum', dataIndex: 'sum', key: 'sum', align: 'right', width: 110, render: (v: number) => <Text strong>{fmtTable(v)}</Text> },
            { title: 'Value', dataIndex: 'value', key: 'value', align: 'right', width: 100, render: (v: number) => fmtTable(v) },
            {
              title: '% of Total', dataIndex: 'percent', key: 'percent', align: 'right', width: 140,
              render: (v: number) => (
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, justifyContent: 'flex-end', width: '100%' }}>
                  <Progress percent={(v / maxPercent) * 100} showInfo={false} size={{ height: 6 }} style={{ width: 60 }} strokeColor={CHART_COLORS[0]} trailColor={GRID} />
                  <span style={{ width: 44, textAlign: 'right', fontVariantNumeric: 'tabular-nums' }}>{fmtPercent(v)}</span>
                </span>
              ),
            },
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

