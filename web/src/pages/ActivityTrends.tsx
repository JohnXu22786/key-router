import React, { useEffect, useState } from 'react';
import { Card, Row, Col, Typography, Spin, message, Tag, Segmented, Select, Space } from 'antd';
import { BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend, LineChart, Line } from 'recharts';
import { getActivity, ActivityResponse } from '../api/client';
import { DateRange, fmtUSD, CHART_COLORS, GRID, AXIS } from './Activity';

const { Text } = Typography;

interface TrendsProps { range: DateRange; }

type GroupMode = 'model' | 'key' | 'app';

const METRICS = [
  { key: 'spend', label: 'Spend', fmt: fmtUSD },
  { key: 'tokens', label: 'Tokens', fmt: (v: number) => fmtTokens2(v) },
  { key: 'requests', label: 'Requests', fmt: (v: number) => String(v) },
];

function fmtTokens2(v: number): string {
  const abs = Math.abs(v);
  if (abs >= 1e9) return (v / 1e9).toFixed(1).replace(/\.0$/, '') + 'B';
  if (abs >= 1e6) return (v / 1e6).toFixed(1).replace(/\.0$/, '') + 'M';
  if (abs >= 1e3) return (v / 1e3).toFixed(1).replace(/\.0$/, '') + 'k';
  return String(Math.round(v));
}

// Build chart data: bucket -> { label, [group]: value } for the stacked chart.
function toChartData(resp: ActivityResponse): Array<Record<string, any>> {
  const buckets = resp.buckets;
  const groups = Array.from(new Set(resp.series.map(s => s.group)));
  const data: Array<Record<string, any>> = buckets.map(b => {
    const row: Record<string, any> = { label: b };
    groups.forEach(g => { row[g] = 0; });
    return row;
  });
  const bucketIdx = new Map(buckets.map((b, i) => [b, i]));
  for (const p of resp.series) {
    const i = bucketIdx.get(p.bucket);
    if (i !== undefined) data[i][p.group] = p.value;
  }
  return data;
}

const ActivityTrends: React.FC<TrendsProps> = ({ range }) => {
  const [mode, setMode] = useState<GroupMode>('model');
  const [metric, setMetric] = useState('spend');
  const [cur, setCur] = useState<ActivityResponse | null>(null);
  const [prev, setPrev] = useState<ActivityResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const fetch = async () => {
      setLoading(true);
      setError(false);
      try {
        const len = range.until.diff(range.since, 'millisecond');
        const prevSince = range.since.subtract(len, 'millisecond');
        const [curRes, prevRes] = await Promise.all([
          getActivity({ metric, group_by: mode, rollup: 'day', since: range.since.toISOString(), until: range.until.toISOString() }),
          getActivity({ metric, group_by: mode, rollup: 'day', since: prevSince.toISOString(), until: range.since.toISOString() }),
        ]);
        if (cancelled) return;
        setCur(curRes.data);
        setPrev(prevRes.data);
      } catch { if (!cancelled) { setError(true); message.error('Failed to load trends'); } }
      finally { if (!cancelled) setLoading(false); }
    };
    fetch();
    return () => { cancelled = true; };
  }, [range, mode, metric]);

  if (loading) return <Spin style={{ display: 'block', margin: '60px auto' }} />;
  if (error || !cur || !prev) {
    return <Card><Text type="danger">Failed to load trends — check the log file.</Text></Card>;
  }

  const chartData = toChartData(cur);
  const groups = Array.from(new Set(cur.series.map(s => s.group)));
  const fmt = METRICS.find(m => m.key === metric)!.fmt;

  // Trending: relative change vs prev period per group.
  const prevSum = new Map(prev.summary.map(s => [s.group, s.sum]));
  const allNames = new Set([...groups, ...Array.from(prevSum.keys())]);
  const trending = Array.from(allNames).map(g => {
    const c = cur.summary.find(s => s.group === g)?.sum ?? 0;
    const p = prevSum.get(g) ?? 0;
    const pct = p > 0 ? ((c - p) / p) * 100 : (c > 0 ? 100 : 0);
    const isNew = p === 0 && c > 0;
    // Sparkline from the current series.
    const spark = cur.series.filter(s => s.group === g).map(s => s.value);
    return { group: g, pct, isNew, spark };
  }).sort((a, b) => b.pct - a.pct).slice(0, 6);

  const title = mode === 'model' ? 'Models' : mode === 'key' ? 'API Keys' : 'Apps';

  return (
    <div>
      <Space wrap style={{ marginBottom: 16 }}>
        <Segmented
          value={mode}
          onChange={(v) => setMode(v as GroupMode)}
          options={[
            { label: 'Models', value: 'model' },
            { label: 'API Keys', value: 'key' },
            { label: 'Apps', value: 'app' },
          ]}
        />
        <Select value={metric} onChange={setMetric} style={{ width: 110 }}
          options={METRICS.map(m => ({ value: m.key, label: m.label }))} />
      </Space>

      <Row gutter={[16, 16]}>
        <Col xs={24} lg={16}>
          <Card style={{ borderRadius: 12 }} title={`Spend over time — ${title}`}>
            <ResponsiveContainer width="100%" height={300}>
              <BarChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke={GRID} vertical={false} />
                <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={24} />
                <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={56} tickFormatter={(v) => fmt(Number(v))} />
                <Tooltip formatter={(v: any, name: any) => [fmt(Number(v)), String(name)]} labelStyle={{ color: AXIS }} contentStyle={{ borderRadius: 8, border: '1px solid ' + GRID }} />
                <Legend wrapperStyle={{ fontSize: 12 }} />
                {groups.slice(0, 9).map((g, i) => (
                  <Bar
                    key={g}
                    dataKey={g}
                    stackId="1"
                    fill={g === 'Other' ? '#94a3b8' : CHART_COLORS[i % CHART_COLORS.length]}
                    maxBarSize={16}
                    radius={i === Math.min(groups.length, 9) - 1 ? [2, 2, 0, 0] : [0, 0, 0, 0]}
                  />
                ))}
              </BarChart>
            </ResponsiveContainer>
          </Card>
        </Col>

        <Col xs={24} lg={8}>
          <Card style={{ borderRadius: 12 }} title="Trending">
            {trending.length === 0 && <Text type="secondary">No usage in this period.</Text>}
            {trending.map(t => (
              <div key={t.group} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 0', borderBottom: `1px solid ${GRID}` }}>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <Text style={{ display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontSize: 13 }}>{t.group}</Text>
                </div>
                <div style={{ width: 44, height: 18 }}>
                  <ResponsiveContainer width="100%" height="100%">
                    <LineChart data={t.spark.map((v, i) => ({ i, v }))} margin={{ top: 1, right: 0, bottom: 0, left: 0 }}>
                      <Line type="monotone" dataKey="v" stroke={t.isNew || t.pct >= 0 ? '#34dfaa' : '#e51d48'} strokeWidth={1.5} dot={false} isAnimationActive={false} />
                    </LineChart>
                  </ResponsiveContainer>
                </div>
                {t.isNew
                  ? <Tag color="green" style={{ marginRight: 0 }}>New</Tag>
                  : <Text style={{ color: t.pct >= 0 ? '#34dfaa' : '#e51d48', fontSize: 13, width: 52, textAlign: 'right', fontVariantNumeric: 'tabular-nums' }}>
                      {t.pct >= 0 ? '▲' : '▼'}{Math.abs(t.pct).toFixed(0)}%
                    </Text>}
              </div>
            ))}
          </Card>
        </Col>
      </Row>
    </div>
  );
};

export default ActivityTrends;
