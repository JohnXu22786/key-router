import React, { useEffect, useRef, useState } from 'react';
import { Card, Row, Col, Typography, Spin, message, Select, Popover, Segmented, theme } from 'antd';
import { PicRightOutlined, PicLeftOutlined, SettingOutlined, RightOutlined } from '@ant-design/icons';
import {
  BarChart, Bar, AreaChart, Area, LineChart, Line, XAxis, YAxis, CartesianGrid,
  Tooltip as RTooltip, ResponsiveContainer,
} from 'recharts';
import { getActivity, getKeys, ActivityResponse } from '../api/client';
import { DateRange, fmtUSD, fmtUSDInt, fmtCompact, CHART_COLORS, OTHER_COLOR, GRID, AXIS, fmtTick, fmtBucket, ExploreOpts, maskKey, toChartData, computeTrending, modelFavicon } from './activityShared';
const { Text } = Typography;

interface TrendsProps {
  range: DateRange;
  onNavigate?: (tab: 'explore', opts?: ExploreOpts) => void;
}

type GroupMode = 'model' | 'key' | 'app';
type ChartType = 'bar' | 'area' | 'line';

const METRICS = [
  { key: 'spend', label: 'Spend' },
  { key: 'tokens', label: 'Tokens' },
  { key: 'requests', label: 'Requests' },
];

// UP / DOWN arrows like the reference's lucide arrow-up / arrow-down.
const UpArrow = () => (
  <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="m5 12 7-7 7 7" /><path d="M12 19V5" /></svg>
);
const DownArrow = () => (
  <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M12 5v14" /><path d="m19 12-7 7-7-7" /></svg>
);

// ModelIcon renders the row's icon: the vendor favicon (local /icons assets,
// like the Explore table) with a letter avatar fallback, matching the saved
// reference page's favicon-per-row layout.
const ModelIcon: React.FC<{ name: string; color: string }> = ({ name, color }) => {
  const fav = modelFavicon(name);
  if (fav.url) {
    return (
      <span style={{ width: 16, height: 16, borderRadius: 3, overflow: 'hidden', flexShrink: 0, display: 'inline-block', border: '1px solid rgba(120,120,140,0.25)' }}>
        <img src={fav.url} alt="" width={16} height={16} style={{ objectFit: 'cover', display: 'block' }} />
      </span>
    );
  }
  return (
    <span style={{ width: 16, height: 16, borderRadius: 3, background: fav.color || color, color: '#fff', fontSize: 9, display: 'inline-flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0, fontWeight: 600 }}>
      {fav.letter}
    </span>
  );
};

const fmtFor = (metric: string) => (v: number) => metric === 'spend' ? fmtUSD(v) : fmtCompact(v);
const fmtAxisFor = (metric: string) => (v: number) => metric === 'spend' ? fmtUSDInt(v) : fmtCompact(v);

const ExploreLink: React.FC<{ onClick: () => void }> = ({ onClick }) => (
  <span
    onClick={onClick}
    style={{ fontSize: 12, textDecoration: 'underline', textUnderlineOffset: 2, cursor: 'pointer', display: 'inline-flex', alignItems: 'center', gap: 2 }}
  >
    Explore<RightOutlined style={{ fontSize: 10 }} />
  </span>
);

interface SectionProps {
  title: string;
  groupBy: GroupMode;
  range: DateRange;
  onNavigate?: (tab: 'explore', opts?: ExploreOpts) => void;
  keysByName: Map<string, string>;
}

const TrendSection: React.FC<SectionProps> = ({ title, groupBy, range, onNavigate, keysByName }) => {
  // Recharts tooltips default to a white card; paint them with theme tokens.
  const { token } = theme.useToken();
  const [metric, setMetric] = useState('spend');
  const [cur, setCur] = useState<ActivityResponse | null>(null);
  const [prev, setPrev] = useState<ActivityResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);
  // A preset/metric switch drops the stale response (spinner); the 30s range
  // slide (same fetch key) keeps the previous chart while refetching.
  // Compared inside the effect: render-time ref writes would be defeated by
  // StrictMode's double render.
  const fetchKey = `${range.key}|${groupBy}|${metric}`;
  const prevKeyRef = useRef<string | null>(null);
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  const [chartType, setChartType] = useState<ChartType>('bar');
  const [legendRight, setLegendRight] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const prevKey = prevKeyRef.current;
    prevKeyRef.current = fetchKey;
    if (prevKey !== null && prevKey !== fetchKey) { setCur(null); setPrev(null); }
    const fetch = async () => {
      setLoading(true);
      try {
        const len = range.until.diff(range.since, 'millisecond');
        const prevSince = range.since.subtract(len, 'millisecond');
        // Roll up at the range's granularity: 24h -> hourly, a month ->
        // daily, a year -> monthly (OR buckets the chart by the view scale).
        const rollup = range.granularity;
        // Current chart folds beyond top-5 into "Other" like the reference;
        // the previous period is fetched unfiltered so every trending row has
        // a complete sparkline.
        const [curRes, prevRes] = await Promise.all([
          getActivity({ metric, group_by: groupBy, rollup, top: 5, since: range.since.toISOString(), until: range.until.toISOString() }),
          getActivity({ metric, group_by: groupBy, rollup, top: 0, since: prevSince.toISOString(), until: range.since.toISOString() }),
        ]);
        if (cancelled) return;
        setCur(curRes.data);
        setPrev(prevRes.data);
      } catch { if (!cancelled) { setError(true); message.error(`Failed to load ${title} trends`); } }
      finally { if (!cancelled) setLoading(false); }
    };
    fetch();
    return () => { cancelled = true; };
  }, [range, metric, groupBy, title]);

  const metricLabel = METRICS.find(m => m.key === metric)!.label;
  const fmt = fmtFor(metric);
  const fmtAxis = fmtAxisFor(metric);
  const goExplore = (opts?: ExploreOpts) => onNavigate?.('explore', { metric, groupBy, ...opts });

  const header = (
    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 12, marginBottom: 12 }}>
      <h3 style={{ fontSize: 16, fontWeight: 600, margin: 0 }}>{title}</h3>
      <Select value={metric} onChange={setMetric} style={{ width: 110 }}
        options={METRICS.map(m => ({ value: m.key, label: m.label }))} />
    </div>
  );

  // A background refetch (range slide, metric switch) keeps the previous
  // data on screen; the spinner only replaces an EMPTY panel and errors
  // only replace real content — a refresh must never flash a blank page.
  if (loading && !cur) {
    return (
      <div>
        {header}
        <Spin style={{ display: 'block', margin: '40px auto' }} />
      </div>
    );
  }
  if (error && !cur) {
    return (
      <div>
        {header}
        <Card style={{ borderRadius: 12 }}><Text type="danger">Failed to load {title} trends — check the log file.</Text></Card>
      </div>
    );
  }
  if (!cur || !prev) return null;

  const chartData = toChartData(cur);
  const groups = Array.from(new Set(cur.series.map(s => s.group)));
  const visibleGroups = groups.filter(g => !hidden.has(g));
  const trending = computeTrending(cur, prev);

  // Series colors: chart palette in series order; "Other" always slate.
  const colors = new Map<string, string>();
  let colorIdx = 0;
  for (const g of groups) {
    if (g === 'Other') colors.set(g, OTHER_COLOR);
    else colors.set(g, CHART_COLORS[colorIdx++ % CHART_COLORS.length]);
  }

  const toggleHidden = (g: string) => {
    setHidden(h => {
      const n = new Set(h);
      if (n.has(g)) n.delete(g); else n.add(g);
      return n;
    });
  };

  const showOnly = (g: string) => {
    const onlyThis = groups.every(x => (x === g) === !hidden.has(x));
    if (onlyThis) setHidden(new Set());
    else setHidden(new Set(groups.filter(x => x !== g)));
  };

  const axes = (
    <>
      <CartesianGrid strokeDasharray="3 3" stroke={GRID} vertical={false} />
      <XAxis dataKey="label" tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} minTickGap={24} tickFormatter={(v) => fmtTick(range.granularity, String(v))} />
      <YAxis tick={{ fill: AXIS, fontSize: 11 }} tickLine={false} axisLine={false} width={56} tickFormatter={fmtAxis} />
      <RTooltip
        formatter={(v: any, name: any) => [fmt(Number(v)), String(name)]}
        labelStyle={{ color: AXIS }}
        contentStyle={{ borderRadius: 8, border: '1px solid ' + GRID, background: token.colorBgContainer, color: token.colorText }}
        labelFormatter={(l) => fmtBucket(range.granularity, String(l))}
      />
    </>
  );

  const chart = (
    <div style={{ width: '100%', height: 300 }}>
      <ResponsiveContainer width="100%" height="100%">
        {chartType === 'bar' ? (
          <BarChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
            {axes}
            {visibleGroups.map((g, i) => (
              <Bar key={g} dataKey={g} stackId="1" fill={colors.get(g)} maxBarSize={12}
                radius={i === visibleGroups.length - 1 ? [2, 2, 0, 0] : [0, 0, 0, 0]} />
            ))}
          </BarChart>
        ) : chartType === 'area' ? (
          <AreaChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
            {axes}
            {visibleGroups.map(g => (
              <Area key={g} dataKey={g} stackId="1" stroke={colors.get(g)} fill={colors.get(g)} fillOpacity={0.4} />
            ))}
          </AreaChart>
        ) : (
          <LineChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
            {axes}
            {visibleGroups.map(g => (
              <Line key={g} dataKey={g} type="monotone" stroke={colors.get(g)} strokeWidth={2} dot={false} />
            ))}
          </LineChart>
        )}
      </ResponsiveContainer>
    </div>
  );

  // Legend chips with hide / show-only buttons, plus chart settings and the
  // legend placement toggle (reference: "Show only Other", gear, move-right).
  const chips = (right: boolean) => (
    <div style={{
      display: 'flex', flexDirection: right ? 'column' : 'row', flexWrap: right ? 'nowrap' : 'wrap',
      alignItems: right ? 'flex-start' : 'center', gap: 4,
      borderTop: right ? 'none' : `1px solid ${GRID}`, paddingTop: right ? 0 : 12,
    }}>
      {groups.map(g => {
        const isHidden = hidden.has(g);
        // True when g is the only visible series — the button then restores all.
        const isOnlyX = groups.every(x => (x === g) === !hidden.has(x));
        return (
          <span key={g} role="group" aria-label={g}
            style={{ display: 'inline-flex', alignItems: 'center', borderRadius: 3, opacity: isHidden ? 0.45 : 1, transition: 'opacity .15s' }}>
            <button type="button" title={isHidden ? `Show ${g}` : `Hide ${g}`} onClick={() => toggleHidden(g)}
              style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '3px 4px' }}>
              <span style={{ display: 'inline-block', width: 10, height: 10, borderRadius: 2, background: colors.get(g), flexShrink: 0 }} />
            </button>
            <button type="button"
              title={isOnlyX ? 'Show all' : g === 'Other' ? 'Show only Other' : `Show only ${g}`}
              onClick={() => showOnly(g)}
              style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '3px 4px', color: 'inherit', fontSize: 12, fontFamily: 'inherit' }}>
              <span style={{ maxWidth: 160, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'inline-block' }}>{g}</span>
            </button>
          </span>
        );
      })}
      <span style={{ marginLeft: right ? 0 : 'auto', display: 'inline-flex', alignItems: 'center', gap: 2, marginTop: right ? 8 : 0 }}>
        <Popover
          trigger="click"
          placement="bottomRight"
          content={(
            <div style={{ minWidth: 180 }}>
              <div style={{ fontSize: 12, marginBottom: 8 }}>Chart type</div>
              <Segmented block size="small" value={chartType} onChange={(v) => setChartType(v as ChartType)}
                options={[{ label: 'Bars', value: 'bar' }, { label: 'Area', value: 'area' }, { label: 'Line', value: 'line' }]} />
            </div>
          )}
        >
          <button type="button" title="Chart settings" style={{ background: 'none', border: 'none', cursor: 'pointer', padding: 4, color: 'inherit' }}>
            <SettingOutlined style={{ fontSize: 14 }} />
          </button>
        </Popover>
        <button type="button" title={legendRight ? 'Move legend to bottom' : 'Move legend to right'}
          onClick={() => setLegendRight(v => !v)} style={{ background: 'none', border: 'none', cursor: 'pointer', padding: 4, color: 'inherit' }}>
          {legendRight ? <PicLeftOutlined style={{ fontSize: 14 }} /> : <PicRightOutlined style={{ fontSize: 14 }} />}
        </button>
      </span>
    </div>
  );

  const empty = <Text type="secondary">No usage in this period.</Text>;

  return (
    <div>
      {header}
      <Row gutter={[16, 16]}>
        <Col xs={24} lg={16}>
          <Card
            style={{ borderRadius: 12 }}
            styles={{ header: { padding: '12px 12px 8px' }, body: { padding: '8px 12px 12px' } }}
            title={`${metricLabel} over time`}
            extra={<ExploreLink onClick={() => goExplore()} />}
          >
            {cur.summary.length === 0 ? empty : legendRight ? (
              <div style={{ display: 'flex', gap: 12 }}>
                <div style={{ flex: 1, minWidth: 0 }}>
                  {visibleGroups.length === 0 ? <div style={{ padding: '12px 0' }}>{empty}</div> : chart}
                </div>
                <div style={{ width: 190, flexShrink: 0 }}>{chips(true)}</div>
              </div>
            ) : (
              <>
                {visibleGroups.length === 0 ? <div style={{ padding: '12px 0' }}>{empty}</div> : chart}
                {chips(false)}
              </>
            )}
          </Card>
        </Col>

        <Col xs={24} lg={8}>
          <Card
            style={{ borderRadius: 12 }}
            styles={{ header: { padding: '12px 16px 8px' }, body: { padding: '0 0 8px' } }}
            title="Trending"
            extra={<ExploreLink onClick={() => goExplore()} />}
          >
            {trending.length === 0 && <div style={{ padding: '0 16px' }}>{empty}</div>}
            {trending.map(t => {
              const provider = groupBy === 'model' && t.group.includes('/') ? t.group.slice(0, t.group.indexOf('/')) : '';
              const name = provider ? t.group.slice(t.group.indexOf('/') + 1) : t.group;
              const subtitle = groupBy === 'key'
                ? (maskKey(keysByName.get(t.group) ?? '') || undefined)
                : provider ? `by ${provider}` : undefined;
              const up = t.pct >= 0;
              const color = up ? '#34dfaa' : '#e1481d';
              return (
                <div
                  key={t.group}
                  role="button"
                  tabIndex={0}
                  title={groupBy === 'model' ? t.group : undefined}
                  onClick={() => goExplore()}
                  onKeyDown={(e) => { if (e.key === 'Enter') goExplore(); }}
                  style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '10px 16px', cursor: 'pointer', minHeight: 36 }}
                >
                  {/* Reference: API-key rows use a small colored circle (no
                      favicon); model/app rows use a 16px icon. */}
                  {groupBy === 'key'
                    ? <span style={{ width: 10, height: 10, borderRadius: '50%', background: colors.get(t.group) ?? OTHER_COLOR, flexShrink: 0, display: 'inline-block' }} />
                    : <ModelIcon name={t.group} color={colors.get(t.group) ?? OTHER_COLOR} />}
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ fontSize: 12, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', lineHeight: 1.4 }}>{name}</div>
                    {subtitle && <div style={{ fontSize: 12, color: '#8b929c', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', lineHeight: 1.4 }}>{subtitle}</div>}
                  </div>
                  <div style={{ width: 48, height: 20, flexShrink: 0 }}>
                    <ResponsiveContainer width="100%" height="100%">
                      <LineChart data={t.spark.map((v, i) => ({ i, v }))} margin={{ top: 1, right: 0, bottom: 0, left: 0 }}>
                        <Line type="monotone" dataKey="v" stroke={color} strokeWidth={1.5} dot={false} isAnimationActive={false} />
                      </LineChart>
                    </ResponsiveContainer>
                  </div>
                  <span style={{ width: 56, flexShrink: 0, display: 'flex', justifyContent: 'flex-end', alignItems: 'center', gap: 4, fontSize: 13, fontWeight: 500, fontVariantNumeric: 'tabular-nums', color }}>
                    {up ? <UpArrow /> : <DownArrow />}
                    {t.isNew ? 'New' : `${Math.abs(t.pct).toFixed(0)}%`}
                  </span>
                </div>
              );
            })}
          </Card>
        </Col>
      </Row>
    </div>
  );
};

const ActivityTrends: React.FC<TrendsProps> = ({ range, onNavigate }) => {
  const [keysByName, setKeysByName] = useState<Map<string, string>>(new Map());

  useEffect(() => {
    let cancelled = false;
    getKeys()
      .then(res => { if (!cancelled) setKeysByName(new Map(res.data.map(k => [k.name || `Key #${k.id}`, k.key_value || '']))); })
      .catch(() => {});
    return () => { cancelled = true; };
  }, []);

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 32 }}>
      <TrendSection title="Models" groupBy="model" range={range} onNavigate={onNavigate} keysByName={keysByName} />
      <TrendSection title="API Keys" groupBy="key" range={range} onNavigate={onNavigate} keysByName={keysByName} />
      <TrendSection title="Apps" groupBy="app" range={range} onNavigate={onNavigate} keysByName={keysByName} />
    </div>
  );
};

export default ActivityTrends;
