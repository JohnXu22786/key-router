import React, { useEffect, useState } from 'react';
import { Card, Row, Col, Typography, Spin, message, Tag, Segmented } from 'antd';
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend } from 'recharts';
import { getActivity, ActivityResponse, ActivitySeriesPoint } from '../api/client';
import { DateRange, fmtUSD, fmtTokens, CHART_COLORS, GRID, AXIS } from './Activity';

const { Text } = Typography;

interface TrendsProps { range: DateRange; }

type GroupMode = 'model' | 'key' | 'app';

// Build chart data: bucket -> { label, [group]: value } for the stacked area.
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
          getActivity({ metric: 'spend', group_by: mode, rollup: 'day', since: range.since.toISOString(), until: range.until.toISOString() }),
          getActivity({ metric: 'spend', group_by: mode, rollup: 'day', since: prevSince.toISOString(), until: range.since.toISOString() }),
        ]);
        if (cancelled) return;
        setCur(curRes.data);
        setPrev(prevRes.data);
      } catch { if (!cancelled) { setError(true); message.error('Failed to load trends'); } }
      finally { if (!cancelled) setLoading(false); }
    };
    fetch();
    return () => { cancelled = true; };
  }, [range, mode]);

  if (loading) return <Spin style={{ display: 'block', margin: '60px auto' }} />;
  if (error || !cur || !prev) {
    return <Card><Text type="danger">Failed to load trends — check the log file.</Text></Card>;
  }

  const chartData = toChartData(cur);
  const groups = Array.from(new Set(cur.series.map(s => s.group)));

  // Compute per-group trend vs prev period.
  const prevSum = new Map(prev.summary.map(s => [s.group, s.sum]));
  const groupNames = new Set([...groups, ...Array.from(prevSum.keys())]);

  // Trending list: groups sorted by relative change (cur vs prev).
  const trending = Array.from(groupNames).map(g => {
    const c = cur.summary.find(s => s.group === g)?.sum ?? 0;
    const p = prevSum.get(g) ?? 0;
    const pct = p > 0 ? ((c - p) / p) * 100 : (c > 0 ? 100 : 0);
    const isNew = p === 0 && c > 0;
    return { group: g, pct, isNew };
  }).sort((a, b) => b.pct - a.pct).slice(0, 6);

  const fmt = fmtUSD;
  const title = mode === 'model' ? 'Models' : mode === 'key' ? 'API Keys' : 'Apps';

  return (
    <div>
      <Segmented
        style={{ marginBottom: 16 }}
        value={mode}
        onChange={(v) => setMode(v as GroupMode)}
        options={[
          { label: 'Models', value: 'model' },
          { label: 'API Keys', value: 'key' },
          { label: 'Apps', value: 'app' },
        ]}
      />

      <Row gutter={[16, 16]}>
        <Col xs={24} lg={16}>
          <Card style={{ borderRadius: 12 }} title={`Spend over time — ${title}`}>
            <ResponsiveContainer width="100%" height={320}>
              <AreaChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                <defs>
                  {groups.slice(0, 9).map((g, i) => (
                    <linearGradient key={g} id={`grad-${i}`} x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor={CHART_COLORS[i % CHART_COLORS.length]} stopOpacity={0.35} />
                      <stop offset="100%" stopColor={CHART_COLORS[i % CHART_COLORS.length]} stopOpacity={0.02} />
                    </linearGradient>
                  ))}
                </defs>
                <CartesianGrid strokeDasharray="3 3" stroke={GRID} vertical={false} />
                <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={24} />
                <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={56} tickFormatter={(v) => fmtUSD(Number(v))} />
                <Tooltip
                  formatter={(v: any, name: any) => [fmtUSD(Number(v)), String(name)]}
                  labelStyle={{ color: AXIS }}
                  contentStyle={{ borderRadius: 8, border: '1px solid ' + GRID }}
                />
                <Legend wrapperStyle={{ fontSize: 12 }} />
                {groups.slice(0, 9).map((g, i) => (
                  <Area
                    key={g}
                    type="monotone"
                    dataKey={g}
                    stackId="1"
                    stroke={CHART_COLORS[i % CHART_COLORS.length]}
                    fill={`url(#grad-${i})`}
                    strokeWidth={1.5}
                    dot={false}
                  />
                ))}
              </AreaChart>
            </ResponsiveContainer>
            {/* Move right hint (OpenRouter shows a "Move right" affordance) */}
            <Text type="secondary" style={{ fontSize: 12 }}>Move right →</Text>
          </Card>
        </Col>

        <Col xs={24} lg={8}>
          <Card style={{ borderRadius: 12 }} title="Trending">
            {trending.length === 0 && <Text type="secondary">No usage in this period.</Text>}
            {trending.map(t => (
              <div key={t.group} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '6px 0', borderBottom: `1px solid ${GRID}` }}>
                <Text>{t.group}</Text>
                {t.isNew
                  ? <Tag color="blue">New</Tag>
                  : <Text style={{ color: t.pct >= 0 ? '#22c1a3' : '#ff5f6d', fontSize: 13 }}>{t.pct >= 0 ? '▲' : '▼'} {Math.abs(t.pct).toFixed(0)}%</Text>}
              </div>
            ))}
          </Card>
        </Col>
      </Row>
    </div>
  );
};

export default ActivityTrends;
