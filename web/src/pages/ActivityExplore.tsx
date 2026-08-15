import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Card, Typography, Spin, message, Select, Dropdown, Button, Space, Tooltip, Table, theme } from 'antd';
import type { TableProps } from 'antd';
import {
  BarChart, Bar, AreaChart, Area, LineChart, Line, XAxis, YAxis, CartesianGrid,
  Tooltip as ChartTip, ResponsiveContainer,
} from 'recharts';
import {
  PlusOutlined, EllipsisOutlined, ExpandOutlined, ShrinkOutlined,
  CaretUpOutlined, CaretDownOutlined, SwapOutlined,
  ArrowRightOutlined, ArrowDownOutlined, CheckOutlined,
} from '@ant-design/icons';
import { getActivity, ActivityResponse, ActivityGroupSummary } from '../api/client';
import {
  DateRange, ActivityFilter, filterKey, fmtUSDInt, fmtTokens, fmtCompact, CHART_COLORS, OTHER_COLOR, GRID, AXIS,
  fmtPercent, fmt3sig, fmtTick, fmtBucket, modelFavicon, Granularity, exclusiveUntil,
} from './activityShared';
import './explore.css';

const { Text } = Typography;

interface ExploreProps {
  range: DateRange;
  filter?: ActivityFilter | null;
  // Seeded by the Trends "Explore" links (metric/grouping of the section).
  initialMetric?: string;
  initialGroupBy?: string;
}

const METRICS = [
  { key: 'spend', label: 'Total Usage ($)' },
  { key: 'blended', label: 'Blended $/1M' },
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
  { value: 'hour', label: 'Hourly' },
  { value: 'day', label: 'Daily' },
  { value: 'week', label: 'Weekly' },
  { value: 'month', label: 'Monthly' },
  { value: 'total', label: 'Total' },
];

// RANK_BY mirrors OpenRouter's "Rank by" dropdown: rank the Top-N (and the
// table's default order) by the current metric or by another metric.
const RANK_BY = [
  { value: 'current', label: 'Current metric' },
  { value: 'blended', label: 'Blended $/1M' },
  { value: 'spend', label: 'Total Usage ($)' },
  { value: 'tokens', label: 'Total Tokens' },
  { value: 'requests', label: 'Request Count' },
  { value: 'cache', label: 'Cache Hits' },
];

const TOPS = [5, 10, 15, 20];

// Second dimension available per primary group (must differ from group_by).
const SUBGROUP_OPTIONS: Record<string, { value: string; label: string }[]> = {
  model: [{ value: 'key', label: 'API Key' }, { value: 'app', label: 'App' }],
  key: [{ value: 'model', label: 'Model' }, { value: 'app', label: 'App' }],
  app: [{ value: 'model', label: 'Model' }, { value: 'key', label: 'API Key' }],
};

const CHART_TYPES = [
  { key: 'bar', label: 'Bar chart' },
  { key: 'area', label: 'Area chart' },
  { key: 'line', label: 'Line chart' },
] as const;

// Separator between a group and its subgroup in chart row keys. Names can
// contain most printable chars, so a control char is safe.
const SEP = '\u0001';
const keyFor = (g: string, sg: string) => (sg ? g + SEP + sg : g);
const displayFor = (g: string, sg: string) => (sg ? `${g} · ${sg}` : g);

// rollupGran maps the API rollup value to a chart-label granularity. The
// "total" rollup renders one bucket labeled "Total": 'day' is safe because
// fmtTick/fmtBucket pass non-date labels through unchanged (they only
// reformat strings shaped like dates).
const rollupGran = (rollup: string): Granularity =>
  rollup === 'hour' ? 'hour' : rollup === 'month' ? 'month' : 'day';

const fmtForTable = (metric: string) => (v: number) =>
  metric === 'spend' || metric === 'blended' ? fmt3sig(v) : metric === 'tokens' || metric === 'cache' ? fmtTokens(v) : fmtCompact(v);

// Numeric summary columns of the table, in display order.
const NUM_COLS: { key: keyof ActivityGroupSummary; label: string }[] = [
  { key: 'min', label: 'Min' },
  { key: 'max', label: 'Max' },
  { key: 'avg', label: 'Avg' },
  { key: 'sum', label: 'Sum' },
  { key: 'value', label: 'Value' },
];

const ActivityExplore: React.FC<ExploreProps> = ({ range, filter, initialMetric, initialGroupBy }) => {
  // Hand-drawn control chips ("by", "Top") must follow the theme too.
  const { token } = theme.useToken();
  const [metric, setMetric] = useState(initialMetric ?? 'spend');
  const [groupBy, setGroupBy] = useState(initialGroupBy ?? 'model');
  const [subgroup, setSubgroup] = useState('');
  const [rollup, setRollup] = useState('day');
  const [topN, setTopN] = useState(10);
  // Backend rank: 'current' = the chart metric's total; other values rank
  // the Top-N (and the table's default order) by a different metric.
  const [rankBy, setRankBy] = useState('current');
  const [chartType, setChartType] = useState<'bar' | 'area' | 'line'>('bar');
  const [legendPos, setLegendPos] = useState<'bottom' | 'right'>('bottom');
  const [expanded, setExpanded] = useState(false);
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  // Table sort: null = backend order, i.e. the "Rank by" selection (the
  // chart metric's total by default). Clicking a header re-sorts client-side.
  const [sortKey, setSortKey] = useState<keyof ActivityGroupSummary | 'group' | null>(null);
  const [sortOrder, setSortOrder] = useState<'asc' | 'desc'>('asc');
  const [data, setData] = useState<ActivityResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  const [loadMs, setLoadMs] = useState(0);
  // A preset/control/filter switch drops the stale response (spinner); the
  // 60s range slide (same fetch key) keeps the previous chart while
  // refetching. Compared inside the effect: render-time ref writes would be
  // defeated by StrictMode's double render.
  const fetchKey = `${range.key}|${metric}|${groupBy}|${subgroup}|${rollup}|${topN}|${rankBy}|${filterKey(filter)}`;
  const prevKeyRef = useRef<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    const prevKey = prevKeyRef.current;
    prevKeyRef.current = fetchKey;
    if (prevKey !== null && prevKey !== fetchKey) { setData(null); }
    const fetch = async () => {
      setLoading(true);
      setError(false);
      const t0 = performance.now();
      try {
        // The current window ends at the last complete bucket (the range's
        // until is floored). Pass one second before it so the server
        // excludes the live bucket from the response — except for sub-hour
        // ranges, whose rows live in the current hour and are the data
        // source for the client's fine axis. rollup=week/total fall back to
        // the day floor: the week bucket is anchored to Monday and the
        // total bucket aggregates everything, so excluding via the day
        // boundary is the closest safe cut.
        const subHourRange = range.granularity === 'minute' || range.granularity === 'min15';
        const rollupGran: Granularity = rollup === 'hour' ? 'hour' : rollup === 'month' ? 'month' : 'day';
        const curUntil = subHourRange ? range.until : exclusiveUntil(range.until, rollupGran);
        const res = await getActivity({
          metric,
          group_by: groupBy,
          subgroup: subgroup || undefined,
          rollup,
          rank_by: rankBy,
          top: topN,
          since: range.since.toISOString(),
          until: curUntil.toISOString(),
          filter_type: filter?.type,
          filter_value: filter?.value,
        });
        if (cancelled) return;
        setData(res.data);
        setLoadMs(Math.max(1, Math.round(performance.now() - t0)));
      } catch { if (!cancelled) { setError(true); message.error('Failed to load explore'); } }
      finally { if (!cancelled) setLoading(false); }
    };
    fetch();
    return () => { cancelled = true; };
  }, [range, metric, groupBy, subgroup, rollup, topN, rankBy, filter]);

  // Ordered list of chart series (group, subgroup) as they appear in the
  // backend response (rank-metric desc — "Rank by"; per-group subgroups
  // when set).
  const seriesKeys = useMemo(() => {
    const out: { key: string; group: string; subgroup: string }[] = [];
    const seen = new Set<string>();
    for (const p of data?.series ?? []) {
      const key = keyFor(p.group, p.subgroup || '');
      if (!seen.has(key)) {
        seen.add(key);
        out.push({ key, group: p.group, subgroup: p.subgroup || '' });
      }
    }
    return out;
  }, [data]);

  // Color per chart series (Other keeps its dedicated slate color).
  const seriesColor = (i: number, group: string) =>
    group === 'Other' ? OTHER_COLOR : CHART_COLORS[i % CHART_COLORS.length];
  const groupColors = useMemo(() => {
    const m = new Map<string, string>();
    seriesKeys.forEach((sk, i) => { if (!m.has(sk.group)) m.set(sk.group, seriesColor(i, sk.group)); });
    return m;
  }, [seriesKeys]);

  // Chart rows: bucket -> { label, [seriesKey]: value }. Hidden series are
  // dropped entirely (stacked bars/areas stay intact; lines vanish).
  const chartData = useMemo(() => {
    if (!data) return [];
    const bucketIdx = new Map(data.buckets.map((b, i) => [b, i]));
    const rows: Array<Record<string, any>> = data.buckets.map(b => {
      const row: Record<string, any> = { label: b };
      seriesKeys.forEach(sk => { if (!hidden.has(sk.key)) row[sk.key] = 0; });
      return row;
    });
    for (const p of data.series) {
      const i = bucketIdx.get(p.bucket);
      const key = keyFor(p.group, p.subgroup || '');
      if (i === undefined || hidden.has(key)) continue;
      rows[i][key] = p.value;
    }
    return rows;
  }, [data, seriesKeys, hidden]);

  // NOTE: all hooks must be called before any early return below.
  const summary = (data?.summary ?? []).slice(0, topN);
  const sorted = useMemo(() => {
    if (!sortKey) return summary;
    const dir = sortOrder === 'asc' ? 1 : -1;
    return [...summary].sort((a, b) => {
      if (sortKey === 'group') return a.group.localeCompare(b.group) * dir;
      return ((a[sortKey] as number) - (b[sortKey] as number)) * dir;
    });
  }, [data, topN, sortKey, sortOrder]);

  // Only blank on the very first load: while refreshing (range slide,
  // metric/group switch) the previous data stays visible until the new data
  // arrives — a refresh must never flash a white page.
  if (loading && !data) return <Spin style={{ display: 'block', margin: '60px auto' }} />;
  if (!data) {
    return <Card><Text type="danger">Failed to load explore — check the log file.</Text></Card>;
  }

  const fmtAxis = (v: number) => (metric === 'spend' ? fmtUSDInt(v) : metric === 'blended' ? fmt3sig(v) : fmtCompact(v));
  const fmtTable = fmtForTable(metric);
  const groupLabel = GROUP_BY.find(g => g.value === groupBy)!.label;
  const subgroupOptions = SUBGROUP_OPTIONS[groupBy];
  // Rates (blended $/1M) must never stack — stacked rates are meaningless.
  const stackId = metric === 'blended' ? undefined : '1';

  // --- Legend interactions (OR: dot toggles hide, name = show only) ---
  const toggleHidden = (key: string) => {
    setHidden(prev => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key); else next.add(key);
      return next;
    });
  };
  const showOnly = (key: string) => {
    setHidden(prev => {
      const othersVisible = prev.size === seriesKeys.length - 1 && !prev.has(key);
      if (othersVisible) return new Set(); // already show-only: restore all
      return new Set(seriesKeys.filter(sk => sk.key !== key).map(sk => sk.key));
    });
  };

  const handleSort = (key: keyof ActivityGroupSummary | 'group') => {
    if (sortKey !== key) { setSortKey(key); setSortOrder('asc'); }
    else if (sortOrder === 'asc') setSortOrder('desc');
    else { setSortKey(null); setSortOrder('asc'); }
  };

  const sortHeader = (label: string, key: keyof ActivityGroupSummary | 'group') => {
    const active = sortKey === key;
    const icon = !active
      ? <SwapOutlined style={{ fontSize: 11, color: AXIS }} />
      : sortOrder === 'asc'
        ? <CaretUpOutlined style={{ fontSize: 11, color: CHART_COLORS[0] }} />
        : <CaretDownOutlined style={{ fontSize: 11, color: CHART_COLORS[0] }} />;
    return (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, cursor: 'pointer' }} onClick={() => handleSort(key)}>
        {label}{icon}
      </span>
    );
  };

  const columns: TableProps<ActivityGroupSummary>['columns'] = [
    {
      title: sortHeader(groupLabel, 'group'),
      dataIndex: 'group',
      key: 'group',
      render: (g: string) => <ModelCell name={g} />,
    },
    ...NUM_COLS.map(c => ({
      title: sortHeader(c.label, c.key),
      dataIndex: c.key,
      key: c.key,
      align: 'right' as const,
      width: 100,
      render: (v: number) => fmtTable(v),
    })),
    {
      title: sortHeader('% of Total', 'percent'),
      dataIndex: 'percent',
      key: 'percent',
      align: 'right' as const,
      width: 160,
      render: (v: number, row: ActivityGroupSummary) => (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, justifyContent: 'flex-end', width: '100%' }}>
          <div style={{ width: 64, height: 8, borderRadius: 999, background: GRID, overflow: 'hidden' }}>
            {/* OR scales the bar to the percent itself (not the row max) */}
            <div style={{ width: `${Math.min(v, 100)}%`, height: '100%', borderRadius: 999, background: groupColors.get(row.group) || CHART_COLORS[0] }} />
          </div>
          <span style={{ width: 56, textAlign: 'right', fontVariantNumeric: 'tabular-nums' }}>{fmtPercent(v)}</span>
        </span>
      ),
    },
  ];

  const chartHeight = expanded ? 400 : 150;

  const chart = (
    <ResponsiveContainer width="100%" height={chartHeight}>
      {chartType === 'bar' && (
        <BarChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
          <CartesianGrid strokeDasharray="3 3" stroke={GRID} />
          <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 12 }} tickLine={false} axisLine={false} minTickGap={48} tickFormatter={(v) => fmtTick(rollupGran(rollup), String(v))} />
          <YAxis tick={{ fill: AXIS, fontSize: 12 }} tickLine={false} axisLine={false} width={60} tickFormatter={fmtAxis} />
          <ChartTip formatter={(v: any, name: any) => [fmtTable(Number(v)), String(name)]} labelStyle={{ color: AXIS }} contentStyle={{ borderRadius: 8, border: '1px solid ' + GRID, background: token.colorBgContainer, color: token.colorText }} labelFormatter={(l) => fmtBucket(rollupGran(rollup), String(l))} />
          {/* dataKey is a function accessor: recharts resolves string keys via
              lodash paths, so dots in names like "claude-3.5" would break */}
          {seriesKeys.map((sk, i) => (
            <Bar key={sk.key} dataKey={(d: any) => d[sk.key]} name={displayFor(sk.group, sk.subgroup)} stackId={stackId} fill={seriesColor(i, sk.group)} maxBarSize={21} isAnimationActive={false} />
          ))}
        </BarChart>
      )}
      {chartType === 'area' && (
        <AreaChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
          <CartesianGrid strokeDasharray="3 3" stroke={GRID} />
          <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 12 }} tickLine={false} axisLine={false} minTickGap={48} tickFormatter={(v) => fmtTick(rollupGran(rollup), String(v))} />
          <YAxis tick={{ fill: AXIS, fontSize: 12 }} tickLine={false} axisLine={false} width={60} tickFormatter={fmtAxis} />
          <ChartTip formatter={(v: any, name: any) => [fmtTable(Number(v)), String(name)]} labelStyle={{ color: AXIS }} contentStyle={{ borderRadius: 8, border: '1px solid ' + GRID, background: token.colorBgContainer, color: token.colorText }} labelFormatter={(l) => fmtBucket(rollupGran(rollup), String(l))} />
          {seriesKeys.map((sk, i) => (
            <Area key={sk.key} dataKey={(d: any) => d[sk.key]} name={displayFor(sk.group, sk.subgroup)} type="monotone" stackId={stackId} stroke={seriesColor(i, sk.group)} strokeWidth={1.5} fill={seriesColor(i, sk.group)} fillOpacity={0.35} dot={false} />
          ))}
        </AreaChart>
      )}
      {chartType === 'line' && (
        <LineChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
          <CartesianGrid strokeDasharray="3 3" stroke={GRID} />
          <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 12 }} tickLine={false} axisLine={false} minTickGap={48} tickFormatter={(v) => fmtTick(rollupGran(rollup), String(v))} />
          <YAxis tick={{ fill: AXIS, fontSize: 12 }} tickLine={false} axisLine={false} width={60} tickFormatter={fmtAxis} />
          <ChartTip formatter={(v: any, name: any) => [fmtTable(Number(v)), String(name)]} labelStyle={{ color: AXIS }} contentStyle={{ borderRadius: 8, border: '1px solid ' + GRID, background: token.colorBgContainer, color: token.colorText }} labelFormatter={(l) => fmtBucket(rollupGran(rollup), String(l))} />
          {seriesKeys.map((sk, i) => (
            <Line key={sk.key} dataKey={(d: any) => d[sk.key]} name={displayFor(sk.group, sk.subgroup)} type="monotone" stroke={seriesColor(i, sk.group)} strokeWidth={1.5} dot={false} />
          ))}
        </LineChart>
      )}
    </ResponsiveContainer>
  );

  const legendEntry = (sk: { key: string; group: string; subgroup: string }, i: number) => {
    const isHidden = hidden.has(sk.key);
    const display = displayFor(sk.group, sk.subgroup);
    return (
      <span key={sk.key} className="explore-legend-item" style={{ display: 'inline-flex', alignItems: 'center', borderRadius: 4, fontSize: 12, color: 'inherit', opacity: isHidden ? 0.4 : 1, transition: 'opacity .15s, background-color .15s' }}>
        <Tooltip title={isHidden ? `Show ${display}` : `Hide ${display}`}>
          <button onClick={() => toggleHidden(sk.key)} style={{ border: 'none', background: 'none', cursor: 'pointer', padding: '2px 4px' }}>
            <span style={{ display: 'inline-block', width: 10, height: 10, borderRadius: 2, background: seriesColor(i, sk.group) }} />
          </button>
        </Tooltip>
        <Tooltip title={`Show only ${display}`}>
          <button onClick={() => showOnly(sk.key)} style={{ border: 'none', background: 'none', cursor: 'pointer', padding: '2px 4px', color: 'inherit' }}>
            <span style={{ maxWidth: 160, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{display}</span>
          </button>
        </Tooltip>
      </span>
    );
  };

  const moveLegendBtn = (
    <Tooltip title={legendPos === 'bottom' ? 'Move legend to right' : 'Move legend to bottom'}>
      <button onClick={() => setLegendPos(p => (p === 'bottom' ? 'right' : 'bottom'))} style={{ marginLeft: 'auto', border: 'none', background: 'none', cursor: 'pointer', color: AXIS, padding: 4 }}>
        {legendPos === 'bottom' ? <ArrowRightOutlined /> : <ArrowDownOutlined />}
      </button>
    </Tooltip>
  );

  const chartTypeItems = CHART_TYPES.map(t => ({
    key: t.key,
    label: t.label,
    icon: chartType === t.key ? <CheckOutlined /> : undefined,
  }));

  return (
    <div>
      {/* Control row — OR layout: [Metric] by [Group] [+Subgroup] | [Rollup] | [Top][N][Rank by] ... [chart settings][Expand] */}
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6, marginBottom: 16 }}>
        <Select size="small" value={metric} onChange={(v: string) => setMetric(v)} style={{ minWidth: 150 }}
          options={METRICS.map(m => ({ value: m.key, label: m.label }))} />
        <span style={{ height: 24, display: 'inline-flex', alignItems: 'center', padding: '0 8px', borderRadius: 6, border: '1px solid ' + GRID, background: token.colorFillAlter, fontSize: 12, color: AXIS }}>by</span>
        <Select size="small" value={groupBy} onChange={(v: string) => { setGroupBy(v); setSubgroup(''); setHidden(new Set()); }} style={{ minWidth: 110 }}
          options={GROUP_BY} />
        <Dropdown menu={{
          items: subgroupOptions.map(o => ({ key: o.value, label: o.label })),
          onClick: ({ key }) => { setSubgroup(key); setHidden(new Set()); },
        }}>
          <Button size="small" icon={<PlusOutlined />}>Subgroup</Button>
        </Dropdown>
        {subgroup && (
          <Select size="small" value={subgroup} onChange={(v?: string) => { setSubgroup(v || ''); setHidden(new Set()); }} allowClear style={{ minWidth: 110 }} options={subgroupOptions} />
        )}
        <span style={{ width: 1, height: 16, background: GRID, margin: '0 2px' }} />
        <Select size="small" value={rollup} onChange={(v: string) => setRollup(v)} style={{ minWidth: 130 }}
          options={ROLLUP.map(r => ({ value: r.value, label: r.label }))}
          labelRender={() => (
            <span><span style={{ color: AXIS, marginRight: 4 }}>Rollup:</span>{ROLLUP.find(r => r.value === rollup)!.label}</span>
          )} />
        <span style={{ width: 1, height: 16, background: GRID, margin: '0 2px' }} />
        {/* OR joins Top / N / Rank by into one segmented group */}
        <Space.Compact>
          <Select size="small" value="top" style={{ minWidth: 50 }}
            options={[{ value: 'top', label: 'Top' }]} />
          <Select size="small" value={topN} onChange={(v: number) => setTopN(v)} style={{ minWidth: 55 }}
            options={TOPS.map(n => ({ value: n, label: String(n) }))} />
          <Select size="small" value={rankBy}
            onChange={(v: string) => { setRankBy(v); setSortKey(null); setHidden(new Set()); }}
            style={{ minWidth: 168 }}
            options={RANK_BY.map(r => ({ value: r.value, label: r.label }))}
            labelRender={() => (
              <span><span style={{ color: AXIS, marginRight: 4 }}>Rank by:</span>{RANK_BY.find(r => r.value === rankBy)!.label}</span>
            )} />
        </Space.Compact>
        <div style={{ marginLeft: 'auto', display: 'flex' }}>
          <Space.Compact>
            <Dropdown menu={{ items: chartTypeItems, onClick: ({ key }) => setChartType(key as 'bar' | 'area' | 'line') }}>
              <Button size="small" icon={<EllipsisOutlined />} aria-label="Chart display settings" />
            </Dropdown>
            <Button size="small" onClick={() => setExpanded(!expanded)}>
              {expanded ? <ShrinkOutlined /> : <ExpandOutlined />}
              <span style={{ width: 56, textAlign: 'center' }}>{expanded ? 'Collapse' : 'Expand'}</span>
            </Button>
          </Space.Compact>
        </div>
      </div>

      {/* Chart + custom legend (OR: no inline legend, hide/show-only dots below) */}
      <Card style={{ borderRadius: 12, marginBottom: 16 }}>
        {legendPos === 'right' ? (
          <div style={{ display: 'flex', gap: 16 }}>
            <div style={{ flex: 1, minWidth: 0 }}>{chart}</div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 180, maxHeight: chartHeight, overflowY: 'auto' }}>
              {seriesKeys.map((sk, i) => legendEntry(sk, i))}
              <div style={{ marginTop: 'auto', display: 'flex' }}>{moveLegendBtn}</div>
            </div>
          </div>
        ) : (
          <>
            {chart}
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'flex-start', gap: '2px 4px', paddingTop: 12, marginTop: 12, borderTop: '1px solid ' + GRID }}>
              {seriesKeys.map((sk, i) => legendEntry(sk, i))}
              {moveLegendBtn}
            </div>
          </>
        )}
      </Card>

      {/* Summary table: favicon | Min | Max | Avg | Sum | Value | % of Total */}
      {summary.length === 0 ? (
        <Card style={{ borderRadius: 12 }}>
          <Text type="secondary">No usage in this period.</Text>
        </Card>
      ) : (
        <Card style={{ borderRadius: 12 }}>
          <Table<ActivityGroupSummary>
            dataSource={sorted}
            rowKey="group"
            size="small"
            pagination={false}
            sticky
            scroll={{ y: 400, x: 800 }}
            columns={columns}
          />
          <Text type="secondary" style={{ fontSize: 12, marginTop: 8, display: 'block' }}>
            {data.summary.length} rows · {loadMs}ms
          </Text>
        </Card>
      )}
    </div>
  );
};

// ModelCell renders the row's favicon (vendor icon when recognizable,
// letter avatar otherwise) + truncated name, like OpenRouter.
const ModelCell: React.FC<{ name: string }> = ({ name }) => {
  const fav = modelFavicon(name);
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, maxWidth: '100%' }}>
      {fav.url ? (
        <span style={{ width: 14, height: 14, borderRadius: 3, overflow: 'hidden', border: '1px solid ' + GRID, flexShrink: 0, display: 'inline-flex' }}>
          <img src={fav.url} alt="" width={14} height={14} style={{ objectFit: 'cover' }} />
        </span>
      ) : (
        <span style={{ width: 14, height: 14, borderRadius: 3, background: fav.color, color: '#fff', fontSize: 9, display: 'inline-flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0, fontWeight: 600 }}>
          {fav.letter}
        </span>
      )}
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{name}</span>
    </span>
  );
};

export default ActivityExplore;

