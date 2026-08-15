import React, { useEffect, useRef, useState } from 'react';
import { Card, Row, Col, Typography, Spin, message, theme } from 'antd';
import { RightOutlined, PicRightOutlined, PicLeftOutlined } from '@ant-design/icons';
import {
  XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
  BarChart, Bar, LineChart, Line,
} from 'recharts';
import { getConsumptions, getKeys, Consumption, Key } from '../api/client';
import { DateRange, ActivityFilter, filterKey, ExploreOpts, fmtUSD, fmtTokens, fmtCompact, fmtTokensBare, fmtUSDInt, CHART_COLORS, OTHER_COLOR, GRID, AXIS, fmtPercent, fmtTick, fmtBucket, series, stackedData, groupTotals, Granularity, maskKey, cacheHitRate, rowWindowShare } from './activityShared';
import dayjs from 'dayjs';

const { Text } = Typography;

interface OverviewProps {
  range: DateRange;
  filter?: ActivityFilter | null;
  // Overview "Explore" links switch to the Explore tab, seeding it with the
  // section's metric/grouping (reference: every card links to
  // /activity/explore?metric=...&dimension=...).
  onNavigate?: (tab: 'explore', opts?: ExploreOpts) => void;
}

// keyValueFor is bound inside the component to the loaded keys.
function keyValueFor(name: string): string {
  return keysRefForOverview.get(name) || '';
}
let keysRefForOverview = new Map<string, string>();

const deltaPct = (cur: number, prev: number) =>
  prev > 0 ? ((cur - prev) / prev) * 100 : (cur > 0 ? 100 : 0);

// ExploreLink: the card-header "Explore ›" link, like the reference page's
// underlined Explore anchor on every chart/card header.
const ExploreLink: React.FC<{ onClick: () => void }> = ({ onClick }) => (
  <span
    role="link"
    tabIndex={0}
    onClick={onClick}
    onKeyDown={(e) => { if (e.key === 'Enter') onClick(); }}
    style={{ fontSize: 12, textDecoration: 'underline', textUnderlineOffset: 2, cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 2 }}
  >
    Explore<RightOutlined style={{ fontSize: 10 }} />
  </span>
);

interface LegendGroup { name: string; color: string; }

// ChartCard: a chart card with the reference's custom legend — the color
// swatch toggles a series hidden, clicking the name shows only it, and the
// move-right button relocates the legend next to the chart. Each card keeps
// its own legend state (hide/show-only/move-right), like the reference.
const ChartCard: React.FC<{
  title: string;
  extra?: React.ReactNode;
  groups: LegendGroup[];
  renderChart: (visibleGroups: string[]) => React.ReactNode;
}> = ({ title, extra, groups, renderChart }) => {
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  const [legendRight, setLegendRight] = useState(false);

  const toggleHidden = (g: string) => {
    setHidden(h => {
      const n = new Set(h);
      if (n.has(g)) n.delete(g); else n.add(g);
      return n;
    });
  };

  const showOnly = (g: string) => {
    const onlyThis = groups.every(x => (x.name === g) === !hidden.has(x.name));
    if (onlyThis) setHidden(new Set());
    else setHidden(new Set(groups.filter(x => x.name !== g).map(x => x.name)));
  };

  const visibleGroups = groups.filter(g => !hidden.has(g.name)).map(g => g.name);

  const chips = (right: boolean) => (
    <div style={{
      display: 'flex', flexDirection: right ? 'column' : 'row', flexWrap: right ? 'nowrap' : 'wrap',
      alignItems: right ? 'flex-start' : 'center', gap: 4,
      borderTop: right ? 'none' : `1px solid ${GRID}`, paddingTop: right ? 0 : 12,
    }}>
      {groups.map(g => {
        const isHidden = hidden.has(g.name);
        // True when g is the only visible series — the button then restores all.
        const isOnlyX = groups.every(x => (x.name === g.name) === !hidden.has(x.name));
        return (
          <span key={g.name} role="group" aria-label={g.name}
            style={{ display: 'inline-flex', alignItems: 'center', borderRadius: 3, opacity: isHidden ? 0.45 : 1, transition: 'opacity .15s' }}>
            <button type="button" title={isHidden ? `Show ${g.name}` : `Hide ${g.name}`} aria-pressed={!isHidden} onClick={() => toggleHidden(g.name)}
              style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '3px 4px' }}>
              <span style={{ display: 'inline-block', width: 10, height: 10, borderRadius: 2, background: g.color, flexShrink: 0 }} />
            </button>
            <button type="button"
              title={isOnlyX ? 'Show all' : `Show only ${g.name}`}
              onClick={() => showOnly(g.name)}
              style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '3px 4px', color: 'inherit', fontSize: 12, fontFamily: 'inherit' }}>
              <span style={{ maxWidth: 160, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'inline-block' }}>{g.name}</span>
            </button>
          </span>
        );
      })}
      <span style={{ marginLeft: right ? 0 : 'auto', display: 'inline-flex', alignItems: 'center', marginTop: right ? 8 : 0 }}>
        <button type="button" title={legendRight ? 'Move legend to bottom' : 'Move legend to right'}
          onClick={() => setLegendRight(v => !v)} style={{ background: 'none', border: 'none', cursor: 'pointer', padding: 4, color: 'inherit' }}>
          {legendRight ? <PicLeftOutlined style={{ fontSize: 14 }} /> : <PicRightOutlined style={{ fontSize: 14 }} />}
        </button>
      </span>
    </div>
  );

  return (
    <Card style={{ borderRadius: 12 }} title={title} extra={extra}>
      {legendRight ? (
        <div style={{ display: 'flex', gap: 12 }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            {visibleGroups.length === 0
              ? <Text type="secondary">No usage in this period.</Text>
              : renderChart(visibleGroups)}
          </div>
          <div style={{ width: 190, flexShrink: 0 }}>{chips(true)}</div>
        </div>
      ) : (
        <>
          {visibleGroups.length === 0
            ? <div style={{ padding: '12px 0' }}><Text type="secondary">No usage in this period.</Text></div>
            : renderChart(visibleGroups)}
          {chips(false)}
        </>
      )}
    </Card>
  );
};

const ActivityOverview: React.FC<OverviewProps> = ({ range, filter, onNavigate }) => {
  // Recharts tooltips default to a white card; paint them with theme tokens
  // so they match light/dark.
  const { token } = theme.useToken();
  const [curList, setCurList] = useState<Consumption[]>([]);
  const [prevList, setPrevList] = useState<Consumption[]>([]);
  const [keys, setKeys] = useState<Key[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  // Last SUCCESSFUL fetch snapshot: while a refetch fails (server down, the
  // 30s slide keeps sliding "now"), the stale data stays rendered against
  // the window AND the fetch time it actually covers instead of being
  // re-bucketed/prorated onto a slid window. cutoff is when the rows'
  // values were recorded (hourly rows accumulate live usage).
  const [win, setWin] = useState<{
    since: dayjs.Dayjs;
    until: dayjs.Dayjs;
    granularity: Granularity;
    prevSince: dayjs.Dayjs;
    cutoff: dayjs.Dayjs;
  } | null>(null);
  // Compares the fetch key INSIDE the effect (never during render, which
  // StrictMode's double render would defeat): a preset/window/filter switch
  // drops the stale data so the previous window's values are never shown
  // under the new axes; the 30s slide (same key) keeps them while refetching.
  const prevKeyRef = useRef<string | null>(null);
  const fetchKey = `${range.key}|${filterKey(filter)}`;
  // The previous period of equal length (for KPI deltas and its proration).
  const len = range.until.diff(range.since, 'millisecond');
  const prevSince = range.since.subtract(len, 'millisecond');

  useEffect(() => {
    let cancelled = false;
    const prevKey = prevKeyRef.current;
    prevKeyRef.current = fetchKey;
    if (prevKey !== null && prevKey !== fetchKey) {
      setCurList([]);
      setPrevList([]);
      setWin(null);
    }
    const fetch = async () => {
      setLoading(true);
      setError(false);
      try {
        const cutoff = dayjs();
        const [curRes, prevRes, keyRes] = await Promise.all([
          getConsumptions({ since: range.since.toISOString(), until: range.until.toISOString(), filter_type: filter?.type, filter_value: filter?.value }),
          getConsumptions({ since: prevSince.toISOString(), until: range.since.toISOString(), filter_type: filter?.type, filter_value: filter?.value }),
          getKeys(),
        ]);
        if (cancelled) return;
        setCurList(curRes.data);
        setPrevList(prevRes.data);
        setKeys(keyRes.data);
        setWin({ since: range.since, until: range.until, granularity: range.granularity, prevSince, cutoff });
        keysRefForOverview = new Map(keyRes.data.map(k => [k.name || `Key #${k.id}`, k.key_value || '']));
      } catch { if (!cancelled) { setError(true); message.error('Failed to load activity'); } }
      finally { if (!cancelled) setLoading(false); }
    };
    fetch();
    return () => { cancelled = true; };
  }, [range, filter]);

  // Only blank on the very first load: while refreshing (the range slides
  // every 30s) the previous charts stay visible until the new data arrives
  // — a refresh must never flash a white page.
  if (loading && curList.length === 0) return <Spin style={{ display: 'block', margin: '60px auto' }} />;
  if (error && curList.length === 0) {
    return <Card><Text type="danger">Failed to load activity — check the log file.</Text></Card>;
  }

  // Time series, bucketed at the range's granularity so the line follows
  // the selected view scale (15m/1h -> minute points, 24h -> hourly,
  // 1mo -> daily, 1y -> monthly). While a refetch is in flight or failing,
  // the axes stay on the last successful window so stale data is never
  // re-bucketed onto a slid axis.
  const axSince = win?.since ?? range.since;
  const axUntil = win?.until ?? range.until;
  const gran = win?.granularity ?? range.granularity;
  // cutNow is when the rows' values were recorded: hourly rows accumulate
  // live usage, so a PAST window shares its boundary hour with the live one
  // and proration must divide by the real coverage, not by the window's
  // own until.
  const cutNow = win?.cutoff ?? dayjs();
  // Sub-hour windows prorate every number (KPI sums, ranked tables) with
  // the same window math the minute charts use, so the cards always add up
  // to the chart; coarser granularities keep the raw boundary-hour sums.
  const subHour = gran === 'minute' || gran === 'min15';
  const curWin = subHour ? { since: axSince, until: axUntil, cutoff: cutNow } : null;
  const prevWin = subHour ? { since: win?.prevSince ?? prevSince, until: axSince, cutoff: cutNow } : null;
  const share = (c: Consumption): number => rowWindowShare(c.hour_bucket, axSince, axUntil, cutNow);

  const sum = (l: Consumption[], win: { since: dayjs.Dayjs; until: dayjs.Dayjs; cutoff: dayjs.Dayjs } | null) => l.reduce((a, c) => {
    const f = win ? rowWindowShare(c.hour_bucket, win.since, win.until, win.cutoff) : 1;
    return {
      spend: a.spend + c.cost_usd * f,
      requests: a.requests + c.request_count * f,
      tokens: a.tokens + (c.input_tokens + c.output_tokens) * f,
      cache: a.cache + c.cache_hit_tokens * f,
      input: a.input + c.input_tokens * f,
    };
  }, { spend: 0, requests: 0, tokens: 0, cache: 0, input: 0 });

  const cur = sum(curList, curWin);
  const prev = sum(prevList, prevWin);
  // Cache hit rate = cached / total input tokens (incl. cached) — one
  // consistent formula for both the KPI value and the sparkline.
  const rateFor = (l: Consumption[], win: { since: dayjs.Dayjs; until: dayjs.Dayjs; cutoff: dayjs.Dayjs } | null) => {
    const s = sum(l, win);
    return cacheHitRate(s.input, s.cache);
  };
  const curRate = rateFor(curList, curWin);
  const prevRate = rateFor(prevList, prevWin);
  const blended = cur.tokens > 0 ? (cur.spend / cur.tokens) * 1e6 : 0;
  const blendedPrev = prev.tokens > 0 ? (prev.spend / prev.tokens) * 1e6 : 0;

  const costSeries = series(curList, c => c.cost_usd, axSince, axUntil, cutNow, gran);
  const tokenSeries = series(curList, c => c.input_tokens + c.output_tokens, axSince, axUntil, cutNow, gran);
  const reqSeries = series(curList, c => c.request_count, axSince, axUntil, cutNow, gran);
  // Prompt caching per bucket (token sums, shared by the Cached/Uncached
  // chart and the rate sparkline below).
  const inSeries = series(curList, c => c.input_tokens, axSince, axUntil, cutNow, gran);
  const cacheSeries = series(curList, c => c.cache_hit_tokens, axSince, axUntil, cutNow, gran);
  // Blended $/1M per bucket (cost / tokens in the SAME bucket).
  const blendedSeries = costSeries.map((d, i) => ({
    label: d.label,
    value: (tokenSeries[i]?.value || 0) > 0 ? (d.value / (tokenSeries[i]?.value || 0)) * 1e6 : 0,
  }));
  // Cache-hit rate sparkline: divide the bucket's cached by the bucket's
  // input (token-weighted, like the KPI) — summing per-row rates would
  // over-read with several keys in one bucket.
  const rateSeries = inSeries.map((d, i) => ({
    label: d.label,
    value: cacheHitRate(d.value, cacheSeries[i]?.value || 0),
  }));

  // goExplore opens the Explore tab seeded with a metric/grouping. The
  // reference KPI cards and section headers link to /activity/explore with
  // the matching query params; our Explore tab supports spend/tokens/
  // requests/cache × model/key/app, so each link maps to the closest one.
  const goExplore = (opts?: ExploreOpts) => onNavigate?.('explore', opts);

  // deltaFor: for the Blended $/1M KPI a RISE is negative (cost per token up
  // = bad), so the "bad" flag inverts the color.
  const kpis = [
    { label: 'Total spend', value: fmtUSD(cur.spend), delta: deltaPct(cur.spend, prev.spend), badUp: false, series: costSeries, explore: { metric: 'spend' } },
    { label: 'Requests', value: fmtCompact(cur.requests), delta: deltaPct(cur.requests, prev.requests), badUp: false, series: reqSeries, explore: { metric: 'requests' } },
    { label: 'Token volume', value: fmtTokensBare(cur.tokens), delta: deltaPct(cur.tokens, prev.tokens), badUp: false, series: tokenSeries, explore: { metric: 'tokens' } },
    { label: 'Cache hit rate', value: fmtPercent(curRate), delta: deltaPct(curRate, prevRate), badUp: false, series: rateSeries, explore: { metric: 'cache' } },
    { label: 'Blended $/1M', value: `$${blended.toFixed(2)}`, delta: deltaPct(blended, blendedPrev), badUp: true, series: blendedSeries, explore: { metric: 'spend' } },
  ];

  // --- Charts ---
  // Usage by model (spend, stacked bars, top-5 + Other like OR)
  const modelSpend = groupTotals(curList, c => c.model_name || 'Unknown', c => c.cost_usd, subHour ? share : undefined);
  const topModels = modelSpend.slice(0, 5).map(([m]) => m);
  const usageByModel = stackedData(curList, [...topModels, 'Other'], c => c.model_name || 'Unknown', c => c.cost_usd, axSince, axUntil, cutNow, gran);
  // Fold everything below top-5 into "Other" per bucket.
  const otherModelSet = new Set(modelSpend.slice(5).map(([m]) => m));
  usageByModel.forEach(row => {
    let sum = 0;
    for (const [g, v] of Object.entries(row)) {
      if (g !== 'label' && g !== 'Other' && otherModelSet.has(g)) { sum += (v as number); delete (row as any)[g]; }
    }
    (row as any).Other = sum;
  });
  const modelGroups: LegendGroup[] = [...topModels, 'Other'].map((m, i) => ({
    name: m,
    color: m === 'Other' ? OTHER_COLOR : CHART_COLORS[i % CHART_COLORS.length],
  }));
  // Colors keyed by name so bars keep their color while other series hide.
  const modelColor = new Map(modelGroups.map(g => [g.name, g.color]));

  // Request volume by model (stacked bars, top-5 + Other)
  const modelReqs = groupTotals(curList, c => c.model_name || 'Unknown', c => c.request_count, subHour ? share : undefined);
  const topReqModels = modelReqs.slice(0, 5).map(([m]) => m);
  const reqByModel = stackedData(curList, [...topReqModels, 'Other'], c => c.model_name || 'Unknown', c => c.request_count, axSince, axUntil, cutNow, gran);
  const otherReqSet = new Set(modelReqs.slice(5).map(([m]) => m));
  reqByModel.forEach(row => {
    let sum = 0;
    for (const [g, v] of Object.entries(row)) {
      if (g !== 'label' && g !== 'Other' && otherReqSet.has(g)) { sum += (v as number); delete (row as any)[g]; }
    }
    (row as any).Other = sum;
  });
  const reqGroups: LegendGroup[] = [...topReqModels, 'Other'].map((m, i) => ({
    name: m,
    color: m === 'Other' ? OTHER_COLOR : CHART_COLORS[i % CHART_COLORS.length],
  }));
  const reqColor = new Map(reqGroups.map(g => [g.name, g.color]));

  // Token breakdown: Prompt / Completion (no reasoning field in the model;
  // cached tokens stay in Prompt so nothing is double-counted).
  const promptSeries = series(curList, c => c.input_tokens, axSince, axUntil, cutNow, gran);
  const compSeries = series(curList, c => c.output_tokens, axSince, axUntil, cutNow, gran);
  const tokenBreakdown = promptSeries.map((d, i) => ({
    label: d.label,
    Prompt: d.value,
    Completion: compSeries[i]?.value || 0,
  }));

  // Prompt caching: Cached vs Uncached (per bucket)
  const caching = cacheSeries.map((d, i) => ({
    label: d.label,
    Cached: d.value,
    Uncached: Math.max(0, (inSeries[i]?.value || 0) - d.value),
  }));

  // Top API Keys (tokens) and Top Apps (attribution headers, tokens)
  const keyTokens = groupTotals(curList, c => {
    const k = keys.find(x => x.id === c.key_id);
    return k?.name || `Key #${c.key_id}`;
  }, c => c.input_tokens + c.output_tokens, subHour ? share : undefined);
  const topKeys = keyTokens.slice(0, 5);

  const appTokens = groupTotals(curList, c => c.app_name || 'Unknown', c => c.input_tokens + c.output_tokens, subHour ? share : undefined);
  const topApps = appTokens.slice(0, 5);

  const kpiColors = ['#FF2D55', '#FF2D55', '#FF2D55', '#FF2D55', '#FF2D55'];

  return (
    <div>
      {/* KPI cards with vs-prev chips + sparkline (OR: all sparklines #FF2D55).
          The whole card is a link to Explore, like the reference. */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 12, marginBottom: 16 }}>
        {kpis.map((k, i) => {
          // Blended $/1M: a rise is bad (cost per token up), so positive
          // delta renders red; all other KPIs follow the normal polarity.
          const rising = k.delta >= 0;
          const negative = k.badUp ? rising : !rising;
          return (
            <div
              key={k.label}
              role="link"
              tabIndex={0}
              title={`Explore ${k.label}`}
              onClick={() => goExplore(k.explore)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); goExplore(k.explore); }
              }}
              style={{ cursor: 'pointer' }}
            >
              <Card size="small" hoverable style={{ borderRadius: 12 }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 8 }}>
                  <div>
                    <Text type="secondary" style={{ fontSize: 12 }}>{k.label}</Text>
                    <div style={{ fontSize: 20, fontWeight: 700, margin: '2px 0', fontVariantNumeric: 'tabular-nums' }}>{k.value}</div>
                    <span style={{ fontSize: 12, color: negative ? token.colorError : token.colorSuccess, display: 'inline-flex', alignItems: 'center', gap: 2 }}>
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
            </div>
          );
        })}
      </div>

      {/* Top API Keys + Top Apps — side by side like OR */}
      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        <Col xs={24} lg={12}>
          <Card style={{ borderRadius: 12 }} title="Top API Keys" extra={<ExploreLink onClick={() => goExplore({ metric: 'tokens', groupBy: 'key' })} />}>
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
          <Card style={{ borderRadius: 12 }} title="Top Apps" extra={<ExploreLink onClick={() => goExplore({ metric: 'tokens', groupBy: 'app' })} />}>
            {topApps.length === 0 && <Text type="secondary">No usage in this period.</Text>}
            {topApps.map(([name, val], idx) => (
              <div key={name} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '6px 0' }}>
                <Text type="secondary" style={{ width: 20, textAlign: 'right' }}>{idx + 1}</Text>
                <span style={{ width: 10, height: 10, borderRadius: '50%', background: CHART_COLORS[(idx + 3) % CHART_COLORS.length], flexShrink: 0 }} />
                <div style={{ flex: 1, minWidth: 0 }}>
                  <Text strong style={{ display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{name}</Text>
                </div>
                <Text strong style={{ width: 90, textAlign: 'right' }}>{fmtTokens(val)}</Text>
              </div>
            ))}
          </Card>
        </Col>
      </Row>

      {/* Usage by model — stacked bars */}
      <div style={{ marginBottom: 16 }}>
        <ChartCard
          title="Usage by model"
          extra={<ExploreLink onClick={() => goExplore({ metric: 'spend', groupBy: 'model' })} />}
          groups={modelGroups}
          renderChart={(vis) => (
            <ResponsiveContainer width="100%" height={260}>
              <BarChart data={usageByModel} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                <CartesianGrid strokeDasharray="3 3" stroke={GRID} />
                <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={20} tickFormatter={(v) => fmtTick(gran, String(v))} />
                <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={56} tickFormatter={(v) => fmtUSDInt(Number(v))} />
                <Tooltip formatter={(v: any, name: any) => [fmtUSD(Number(v)), String(name)]} contentStyle={{ borderRadius: 8, border: '1px solid ' + GRID, background: token.colorBgContainer, color: token.colorText }} labelFormatter={(l) => fmtBucket(gran, String(l))} />
                {vis.map((m, i) => (
                  <Bar key={m} dataKey={m} stackId="a" fill={modelColor.get(m)} maxBarSize={22} radius={i === vis.length - 1 ? [2, 2, 0, 0] : [0, 0, 0, 0]} />
                ))}
              </BarChart>
            </ResponsiveContainer>
          )}
        />
      </div>

      <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
        {/* Request volume by model — stacked bars */}
        <Col xs={24} lg={24}>
          <ChartCard
            title="Request volume by model"
            extra={<ExploreLink onClick={() => goExplore({ metric: 'requests', groupBy: 'model' })} />}
            groups={reqGroups}
            renderChart={(vis) => (
              <ResponsiveContainer width="100%" height={260}>
                <BarChart data={reqByModel} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke={GRID} />
                  <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={20} tickFormatter={(v) => fmtTick(gran, String(v))} />
                  <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={50} tickFormatter={(v) => fmtCompact(Number(v))} />
                  <Tooltip formatter={(v: any, name: any) => [fmtCompact(Number(v)), String(name)]} contentStyle={{ borderRadius: 8, background: token.colorBgContainer, color: token.colorText }} labelFormatter={(l) => fmtBucket(gran, String(l))} />
                  {vis.map((m, i) => (
                    <Bar key={m} dataKey={m} stackId="a" fill={reqColor.get(m)} maxBarSize={14} radius={i === vis.length - 1 ? [2, 2, 0, 0] : [0, 0, 0, 0]} />
                  ))}
                </BarChart>
              </ResponsiveContainer>
            )}
          />
        </Col>
      </Row>

      <Row gutter={[16, 16]}>
        {/* Token breakdown — stacked bars (Prompt + Completion; no
            reasoning field in the data model, and no Cached here to avoid
            double-counting cached tokens into the prompt bucket) */}
        <Col xs={24} lg={12}>
          <ChartCard
            title="Token breakdown"
            extra={<ExploreLink onClick={() => goExplore({ metric: 'tokens' })} />}
            groups={[{ name: 'Completion', color: '#a855f7' }, { name: 'Prompt', color: '#3b82f6' }]}
            renderChart={(vis) => (
              <ResponsiveContainer width="100%" height={260}>
                <BarChart data={tokenBreakdown} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke={GRID} />
                  <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={20} tickFormatter={(v) => fmtTick(gran, String(v))} />
                  <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={56} tickFormatter={(v) => fmtCompact(Number(v))} />
                  <Tooltip formatter={(v: any, name: any) => [fmtTokens(Number(v)), String(name)]} contentStyle={{ borderRadius: 8, background: token.colorBgContainer, color: token.colorText }} labelFormatter={(l) => fmtBucket(gran, String(l))} />
                  {vis.map((g, i) => (
                    <Bar key={g} dataKey={g} stackId="a" fill={g === 'Prompt' ? '#3b82f6' : '#a855f7'} maxBarSize={14} radius={i === vis.length - 1 ? [2, 2, 0, 0] : [0, 0, 0, 0]} />
                  ))}
                </BarChart>
              </ResponsiveContainer>
            )}
          />
        </Col>
        {/* Prompt token caching — stacked bars */}
        <Col xs={24} lg={12}>
          <ChartCard
            title="Prompt token caching"
            extra={<ExploreLink onClick={() => goExplore({ metric: 'tokens' })} />}
            groups={[{ name: 'Uncached', color: '#94a3b8' }, { name: 'Cached', color: '#f59e0b' }]}
            renderChart={(vis) => (
              <ResponsiveContainer width="100%" height={260}>
                <BarChart data={caching} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                  <CartesianGrid strokeDasharray="3 3" stroke={GRID} />
                  <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={20} tickFormatter={(v) => fmtTick(gran, String(v))} />
                  <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={56} tickFormatter={(v) => fmtCompact(Number(v))} />
                  <Tooltip formatter={(v: any, name: any) => [fmtTokens(Number(v)), String(name)]} contentStyle={{ borderRadius: 8, background: token.colorBgContainer, color: token.colorText }} labelFormatter={(l) => fmtBucket(gran, String(l))} />
                  {vis.map((g, i) => (
                    <Bar key={g} dataKey={g} stackId="a" fill={g === 'Cached' ? '#f59e0b' : '#94a3b8'} maxBarSize={14} radius={i === vis.length - 1 ? [2, 2, 0, 0] : [0, 0, 0, 0]} />
                  ))}
                </BarChart>
              </ResponsiveContainer>
            )}
          />
        </Col>
      </Row>
    </div>
  );
};

export default ActivityOverview;
