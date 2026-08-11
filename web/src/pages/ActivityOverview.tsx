import React, { useEffect, useState } from 'react';
import { Card, Row, Col, Typography, Spin, message, Space } from 'antd';
import {
  AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
  BarChart, Bar, Legend, LineChart, Line,
} from 'recharts';
import { getConsumptions, getKeys, Consumption, Key } from '../api/client';
import { DateRange, fmtUSD, fmtTokens, fmtCompact, fmtTokensBare, fmtDayLabel, fmtUSDInt, CHART_COLORS, OTHER_COLOR, GRID, AXIS, fmtPercent } from './activityShared';
import dayjs from 'dayjs';

const { Text } = Typography;

interface OverviewProps { range: DateRange; }

// maskKey renders a masked key like "sk-or-v1-063...f48" (first ~12 + last 3).
const maskKey = (raw: string): string => {
  if (!raw) return '';
  if (raw.length <= 16) return raw;
  return `${raw.slice(0, 12)}...${raw.slice(-3)}`;
};

// keyValueFor is bound inside the component to the loaded keys.
function keyValueFor(name: string): string {
  return keysRefForOverview.get(name) || '';
}
let keysRefForOverview = new Map<string, string>();

const deltaPct = (cur: number, prev: number) =>
  prev > 0 ? ((cur - prev) / prev) * 100 : (cur > 0 ? 100 : 0);

// OR palette (from the saved page): chart-N tokens + semantic colors.


// Build a per-day series from raw consumption records.
function dailySeries(list: Consumption[], keyFn: (c: Consumption) => number, label = 'MM-DD') {
  const acc = new Map<string, { label: string; sort: string; value: number }>();
  for (const c of list) {
    const d = dayjs(c.hour_bucket).format('YYYY-MM-DD');
    const row = acc.get(d) || { label: dayjs(c.hour_bucket).format(label), sort: d, value: 0 };
    row.value += keyFn(c);
    acc.set(d, row);
  }
  return [...acc.values()].sort((a, b) => a.sort.localeCompare(b.sort));
}

// Group total by a key, returning sorted desc.
function groupTotals(list: Consumption[], keyFn: (c: Consumption) => string, valFn: (c: Consumption) => number) {
  const acc = new Map<string, number>();
  for (const c of list) {
    acc.set(keyFn(c), (acc.get(keyFn(c)) || 0) + valFn(c));
  }
  return [...acc.entries()].sort((a, b) => b[1] - a[1]);
}

// Stacked chart data: bucket -> { label, [group]: value }.
function stackedData(list: Consumption[], groups: string[], keyFn: (c: Consumption) => string, valFn: (c: Consumption) => number) {
  const bucket = (c: Consumption) => dayjs(c.hour_bucket).format('YYYY-MM-DD');
  const label = (c: Consumption) => dayjs(c.hour_bucket).format('MM-DD');
  const acc = new Map<string, Record<string, any>>();
  for (const c of list) {
    const b = bucket(c);
    if (!acc.has(b)) {
      const row: Record<string, any> = { label: label(c), sort: b };
      groups.forEach(g => { row[g] = 0; });
      acc.set(b, row);
    }
    const row = acc.get(b)!;
    row[keyFn(c)] += valFn(c);
  }
  return [...acc.values()].sort((a, b) => a.sort.localeCompare(b.sort));
}

const ActivityOverview: React.FC<OverviewProps> = ({ range }) => {
  const [curList, setCurList] = useState<Consumption[]>([]);
  const [prevList, setPrevList] = useState<Consumption[]>([]);
  const [keys, setKeys] = useState<Key[]>([]);
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
        const [curRes, prevRes, keyRes] = await Promise.all([
          getConsumptions({ since: range.since.toISOString(), until: range.until.toISOString() }),
          getConsumptions({ since: prevSince.toISOString(), until: range.since.toISOString() }),
          getKeys(),
        ]);
        if (cancelled) return;
        setCurList(curRes.data);
        setPrevList(prevRes.data);
        setKeys(keyRes.data);
        keysRefForOverview = new Map(keyRes.data.map(k => [k.name || `Key #${k.id}`, k.key_value || '']));
      } catch { if (!cancelled) { setError(true); message.error('Failed to load activity'); } }
      finally { if (!cancelled) setLoading(false); }
    };
    fetch();
    return () => { cancelled = true; };
  }, [range]);

  if (loading) return <Spin style={{ display: 'block', margin: '60px auto' }} />;
  if (error) {
    return <Card><Text type="danger">Failed to load activity — check the log file.</Text></Card>;
  }

  const sum = (l: Consumption[]) => l.reduce((a, c) => ({
    spend: a.spend + c.cost_usd,
    requests: a.requests + c.request_count,
    tokens: a.tokens + c.input_tokens + c.output_tokens,
    cache: a.cache + c.cache_hit_tokens,
    input: a.input + c.input_tokens,
  }), { spend: 0, requests: 0, tokens: 0, cache: 0, input: 0 });

  const cur = sum(curList);
  const prev = sum(prevList);
  // Cache hit rate = cached / total input tokens (incl. cached) — one
  // consistent formula for both the KPI value and the sparkline.
  const rateFor = (l: Consumption[]) => {
    const s = sum(l);
    const tot = s.input + s.cache;
    return tot > 0 ? (s.cache / tot) * 100 : 0;
  };
  const curRate = rateFor(curList);
  const prevRate = rateFor(prevList);
  const blended = cur.tokens > 0 ? (cur.spend / cur.tokens) * 1e6 : 0;
  const blendedPrev = prev.tokens > 0 ? (prev.spend / prev.tokens) * 1e6 : 0;
  // Daily blended $/1M series for the sparkline.
  const blendedSeries = dailySeries(curList, c => c.cost_usd).map((d, i) => {
    const t = dailySeries(curList, c => c.input_tokens + c.output_tokens);
    return { label: d.label, value: (t[i]?.value || 0) > 0 ? (d.value / (t[i]?.value || 0)) * 1e6 : 0 };
  });
  const rateSeries = dailySeries(curList, c => (c.cache_hit_tokens / Math.max(1, c.input_tokens + c.cache_hit_tokens)) * 100);

  // deltaFor: for the Blended $/1M KPI a RISE is negative (cost per token up
  // = bad), so the "bad" flag inverts the color.
  const kpis = [
    { label: 'Total spend', value: fmtUSD(cur.spend), delta: deltaPct(cur.spend, prev.spend), badUp: false, series: dailySeries(curList, c => c.cost_usd) },
    { label: 'Requests', value: fmtCompact(cur.requests), delta: deltaPct(cur.requests, prev.requests), badUp: false, series: dailySeries(curList, c => c.request_count) },
    { label: 'Token volume', value: fmtTokensBare(cur.tokens), delta: deltaPct(cur.tokens, prev.tokens), badUp: false, series: dailySeries(curList, c => c.input_tokens + c.output_tokens) },
    { label: 'Cache hit rate', value: fmtPercent(curRate), delta: prevRate > 0 ? ((curRate - prevRate) / prevRate) * 100 : (curRate > 0 ? 100 : 0), badUp: false, series: rateSeries },
    { label: 'Blended $/1M', value: `$${blended.toFixed(2)}`, delta: deltaPct(blended, blendedPrev), badUp: true, series: blendedSeries },
  ];

  // --- Charts ---
  // Usage by model (spend, stacked bars, top-5 + Other like OR)
  const modelSpend = groupTotals(curList, c => c.model_name || 'Unknown', c => c.cost_usd);
  const topModels = modelSpend.slice(0, 5).map(([m]) => m);
  const usageByModel = stackedData(curList, [...topModels, 'Other'], c => c.model_name || 'Unknown', c => c.cost_usd);
  // Fold everything below top-5 into "Other" per bucket.
  const otherModelSet = new Set(modelSpend.slice(5).map(([m]) => m));
  usageByModel.forEach(row => {
    const other = otherModelSet;
    let sum = 0;
    for (const [g, v] of Object.entries(row)) {
      if (g !== 'label' && g !== 'Other' && other.has(g)) { sum += (v as number); delete (row as any)[g]; }
    }
    (row as any).Other = sum;
  });

  // Request volume by model (stacked bars, top-5 + Other)
  const modelReqs = groupTotals(curList, c => c.model_name || 'Unknown', c => c.request_count);
  const topReqModels = modelReqs.slice(0, 5).map(([m]) => m);
  const reqByModel = stackedData(curList, [...topReqModels, 'Other'], c => c.model_name || 'Unknown', c => c.request_count);
  const otherReqSet = new Set(modelReqs.slice(5).map(([m]) => m));
  reqByModel.forEach(row => {
    let sum = 0;
    for (const [g, v] of Object.entries(row)) {
      if (g !== 'label' && g !== 'Other' && otherReqSet.has(g)) { sum += (v as number); delete (row as any)[g]; }
    }
    (row as any).Other = sum;
  });

  // Usage type: BYOK vs spend — we split cost into "OpenRouter Credits"
  // (the gateway's own keys) vs everything else. Simpler: total spend only.
  const usageType = dailySeries(curList, c => c.cost_usd).map(d => ({ ...d, Spend: d.value }));

  // Token breakdown: Prompt / Completion (no reasoning field in the model;
  // cached tokens stay in Prompt so nothing is double-counted).
  const promptSeries = dailySeries(curList, c => c.input_tokens);
  const compSeries = dailySeries(curList, c => c.output_tokens);
  const cacheSeries = dailySeries(curList, c => c.cache_hit_tokens);
  const tokenBreakdown = promptSeries.map((d, i) => ({
    label: d.label,
    Prompt: d.value,
    Completion: compSeries[i]?.value || 0,
  }));

  // Prompt caching: Cached vs Uncached (per day)
  const inSeries = dailySeries(curList, c => c.input_tokens);
  const caching = cacheSeries.map((d, i) => ({
    label: d.label,
    Cached: d.value,
    Uncached: Math.max(0, (inSeries[i]?.value || 0) - d.value),
  }));

  // Top API Keys (tokens) and Top Apps (X-App header, tokens)
  const keyTokens = groupTotals(curList, c => {
    const k = keys.find(x => x.id === c.key_id);
    return k?.name || `Key #${c.key_id}`;
  }, c => c.input_tokens + c.output_tokens);
  const topKeys = keyTokens.slice(0, 5);
  const maxKeyTokens = topKeys.length ? Math.max(...topKeys.map(([, v]) => v)) : 1;

  const appTokens = groupTotals(curList, c => c.app_name || 'Unknown', c => c.input_tokens + c.output_tokens);
  const topApps = appTokens.slice(0, 5);
  const maxAppTokens = topApps.length ? Math.max(...topApps.map(([, v]) => v)) : 1;

  const kpiColors = ['#FF2D55', '#FF2D55', '#FF2D55', '#FF2D55', '#FF2D55'];

  return (
    <div>
      {/* KPI cards with vs-prev chips + sparkline (OR: all sparklines #FF2D55) */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 12, marginBottom: 16 }}>
        {kpis.map((k, i) => {
          // Blended $/1M: a rise is bad (cost per token up), so positive
          // delta renders red; all other KPIs follow the normal polarity.
          const rising = k.delta >= 0;
          const negative = k.badUp ? rising : !rising;
          return (
            <Card key={k.label} size="small" style={{ borderRadius: 12 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 8 }}>
                <div>
                  <Text type="secondary" style={{ fontSize: 12 }}>{k.label}</Text>
                  <div style={{ fontSize: 20, fontWeight: 700, margin: '2px 0', fontVariantNumeric: 'tabular-nums' }}>{k.value}</div>
                  <span style={{ fontSize: 12, color: negative ? '#bf0024' : '#007544', display: 'inline-flex', alignItems: 'center', gap: 2 }}>
                    {rising
                      ? <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><path d="M7 17L17 7M17 7H7M17 7V17" /></svg>
                      : <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"><path d="M7 7l10 10M17 17H7M17 7v10" /></svg>}
                    <span className="tabular-nums">{Math.abs(k.delta).toFixed(1)}%</span>
                    <span style={{ color: '#6b7280', marginLeft: 2 }}>vs prev period</span>
                  </span>
                </div>
                <div style={{ width: 84, height: 40 }}>
                  <ResponsiveContainer width="100%" height="100%">
                    <LineChart data={k.series} margin={{ top: 3, right: 0, bottom: 0, left: 0 }}>
                      <Line type="monotone" dataKey="value" stroke={kpiColors[i]} strokeWidth={1.5} dot={false} isAnimationActive={false} />
                    </LineChart>
                  </ResponsiveContainer>
                </div>
              </div>
            </Card>
          );
        })}
      </div>

      {/* Top API Keys + Top Apps */}
      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        <Col xs={24} lg={12}>
          <Card style={{ borderRadius: 12 }} title="Top API Keys" extra={<Text type="secondary" style={{ fontSize: 12 }}>by tokens</Text>}>
            {topKeys.length === 0 && <Text type="secondary">No usage in this period.</Text>}
            {topKeys.map(([name, val], idx) => (
              <div key={name} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '6px 0' }}>
                <Text type="secondary" style={{ width: 20, textAlign: 'right' }}>{idx + 1}</Text>
                <span style={{ width: 10, height: 10, borderRadius: '50%', background: CHART_COLORS[idx % CHART_COLORS.length], flexShrink: 0 }} />
                <div style={{ flex: 1, minWidth: 0 }}>
                  <Text strong style={{ display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{name}</Text>
                  <Text type="secondary" style={{ fontSize: 12 }}>{maskKey(keyValueFor(name))}</Text>
                </div>
                <Text strong style={{ width: 90, textAlign: 'right' }}>{fmtTokens(val)}</Text>
              </div>
            ))}
          </Card>
        </Col>
        <Col xs={24} lg={12}>
          <Card style={{ borderRadius: 12 }} title="Top Apps" extra={<Text type="secondary" style={{ fontSize: 12 }}>by tokens · X-OpenRouter-Title / Referer / UA</Text>}>
            {topApps.length === 0 && <Text type="secondary">No usage in this period.</Text>}
            {topApps.map(([name, val], idx) => (
              <div key={name} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '6px 0' }}>
                <Text type="secondary" style={{ width: 20, textAlign: 'right' }}>{idx + 1}</Text>
                <span style={{ width: 10, height: 10, borderRadius: '50%', background: CHART_COLORS[(idx + 3) % CHART_COLORS.length], flexShrink: 0 }} />
                <div style={{ flex: 1, minWidth: 0 }}>
                  <Text strong style={{ display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{name}</Text>
                  <Text type="secondary" style={{ fontSize: 12 }}>{maskKey(keyValueFor(name))}</Text>
                </div>
                <Text strong style={{ width: 90, textAlign: 'right' }}>{fmtTokens(val)}</Text>
              </div>
            ))}
          </Card>
        </Col>
      </Row>

      {/* Usage by model — stacked bars */}
      <Card style={{ borderRadius: 12, marginBottom: 16 }} title="Usage by model">
        <ResponsiveContainer width="100%" height={260}>
          <BarChart data={usageByModel} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
            <CartesianGrid strokeDasharray="3 3" stroke={GRID} />
            <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={20} tickFormatter={fmtDayLabel} />
            <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={56} tickFormatter={(v) => fmtUSDInt(Number(v))} />
            <Tooltip formatter={(v: any, name: any) => [fmtUSD(Number(v)), String(name)]} contentStyle={{ borderRadius: 8, border: '1px solid ' + GRID }} labelFormatter={(l) => fmtDayLabel(String(l))} />
            <Legend wrapperStyle={{ fontSize: 12 }} />
            {topModels.map((m, i) => (
              <Bar key={m} dataKey={m} stackId="a" fill={m === 'Other' ? OTHER_COLOR : CHART_COLORS[i % CHART_COLORS.length]} maxBarSize={22} radius={i === topModels.length - 1 ? [2, 2, 0, 0] : [0, 0, 0, 0]} />
            ))}
          </BarChart>
        </ResponsiveContainer>
      </Card>

      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        {/* Usage type — stacked area */}
        <Col xs={24} lg={12}>
          <Card style={{ borderRadius: 12 }} title="Usage type">
            <ResponsiveContainer width="100%" height={260}>
              <AreaChart data={usageType} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke={GRID} />
                <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={20} tickFormatter={fmtDayLabel} />
                <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={56} tickFormatter={(v) => fmtUSDInt(Number(v))} />
                <Tooltip formatter={(v: any) => [fmtUSD(Number(v)), 'Spend']} contentStyle={{ borderRadius: 8 }} labelFormatter={(l) => fmtDayLabel(String(l))} />
                <Area type="monotone" dataKey="Spend" stroke="#8b5cf6" strokeWidth={2} fill="#8b5cf6" fillOpacity={0.4} dot={false} />
              </AreaChart>
            </ResponsiveContainer>
          </Card>
        </Col>
        {/* Request volume by model — stacked bars */}
        <Col xs={24} lg={12}>
          <Card style={{ borderRadius: 12 }} title="Request volume by model">
            <ResponsiveContainer width="100%" height={260}>
              <BarChart data={reqByModel} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke={GRID} />
                <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={20} tickFormatter={fmtDayLabel} />
                <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={50} tickFormatter={(v) => fmtCompact(Number(v))} />
                <Tooltip formatter={(v: any, name: any) => [fmtCompact(Number(v)), String(name)]} contentStyle={{ borderRadius: 8 }} labelFormatter={(l) => fmtDayLabel(String(l))} />
                <Legend wrapperStyle={{ fontSize: 12 }} />
                {topReqModels.map((m, i) => (
                  <Bar key={m} dataKey={m} stackId="a" fill={m === 'Other' ? OTHER_COLOR : CHART_COLORS[i % CHART_COLORS.length]} maxBarSize={14} radius={i === topReqModels.length - 1 ? [2, 2, 0, 0] : [0, 0, 0, 0]} />
                ))}
              </BarChart>
            </ResponsiveContainer>
          </Card>
        </Col>
      </Row>

      <Row gutter={[16, 16]}>
        {/* Token breakdown — stacked bars (Prompt + Completion; no
            reasoning field in the data model, and no Cached here to avoid
            double-counting cached tokens into the prompt bucket) */}
        <Col xs={24} lg={12}>
          <Card style={{ borderRadius: 12 }} title="Token breakdown">
            <ResponsiveContainer width="100%" height={260}>
              <BarChart data={tokenBreakdown} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke={GRID} />
                <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={20} tickFormatter={fmtDayLabel} />
                <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={56} tickFormatter={(v) => fmtCompact(Number(v))} />
                <Tooltip formatter={(v: any, name: any) => [fmtTokens(Number(v)), String(name)]} contentStyle={{ borderRadius: 8 }} labelFormatter={(l) => fmtDayLabel(String(l))} />
                <Legend wrapperStyle={{ fontSize: 12 }} />
                <Bar dataKey="Completion" stackId="a" fill="#a855f7" maxBarSize={14} />
                <Bar dataKey="Prompt" stackId="a" fill="#3b82f6" maxBarSize={14} radius={[2, 2, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          </Card>
        </Col>
        {/* Prompt token caching — stacked bars */}
        <Col xs={24} lg={12}>
          <Card style={{ borderRadius: 12 }} title="Prompt token caching">
            <ResponsiveContainer width="100%" height={260}>
              <BarChart data={caching} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke={GRID} />
                <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={20} tickFormatter={fmtDayLabel} />
                <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={56} tickFormatter={(v) => fmtCompact(Number(v))} />
                <Tooltip formatter={(v: any, name: any) => [fmtTokens(Number(v)), String(name)]} contentStyle={{ borderRadius: 8 }} labelFormatter={(l) => fmtDayLabel(String(l))} />
                <Legend wrapperStyle={{ fontSize: 12 }} />
                <Bar dataKey="Uncached" stackId="a" fill="#94a3b8" maxBarSize={14} />
                <Bar dataKey="Cached" stackId="a" fill="#f59e0b" maxBarSize={14} radius={[2, 2, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          </Card>
        </Col>
      </Row>
    </div>
  );
};

export default ActivityOverview;
