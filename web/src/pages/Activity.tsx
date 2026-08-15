import React, { useEffect, useState, useCallback, useMemo, useRef } from 'react';
import { Typography, Space, Button, Spin, Popover, Segmented, Select, DatePicker, theme, message } from 'antd';
import { ReloadOutlined, CalendarOutlined, FilterOutlined, DownOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import ActivityOverview from './ActivityOverview';
import ActivityTrends from './ActivityTrends';
import ActivityExplore from './ActivityExplore';
// Shared helpers live in activityShared.ts (a leaf module importing only
// dayjs and the API client), so pages never import from Activity.tsx — a
// cycle (Activity -> child -> Activity) can hand the children undefined
// constants and crash the page.
import {
  makeRanges, customRange, CUSTOM_KEY, CUSTOM_LABEL, floorWindowUntil, ExploreOpts,
  ActivityFilter, ActivityFilterType, FILTER_TYPES, modelFavicon, maskKey,
} from './activityShared';
import type { DateRange } from './activityShared';
import { getConsumptions, getKeys, Key } from '../api/client';

const { Title, Text } = Typography;

export type TabKey = 'overview' | 'trends' | 'explore';

// Chip: the small rounded badge (1mo, 4d, 16h) shown next to every preset,
// exactly like OpenRouter's date-range picker chips.
const Chip: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <span style={{
    display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
    minWidth: 34, padding: '0 6px', height: 18, borderRadius: 6,
    background: 'rgba(120,120,140,0.12)', color: 'rgba(120,120,140,0.9)',
    fontSize: 11, fontWeight: 500, fontVariantNumeric: 'tabular-nums',
  }}>
    {children}
  </span>
);

const rangeText = (r: DateRange): string =>
  `${r.since.format('MMM D, h:mm a')} – ${r.until.format('MMM D, h:mm a')}`;

// EntityIcon renders a model's vendor favicon (or a letter avatar fallback)
// in the filter panel's option list, matching the Explore/Trends rows.
const EntityIcon: React.FC<{ name: string }> = ({ name }) => {
  const fav = modelFavicon(name);
  if (fav.url) {
    return <img src={fav.url} alt="" width={14} height={14} style={{ borderRadius: 3, flexShrink: 0 }} />;
  }
  return (
    <span style={{ width: 14, height: 14, borderRadius: 3, background: fav.color, color: '#fff', fontSize: 9, display: 'inline-flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0, fontWeight: 600 }}>
      {fav.letter}
    </span>
  );
};

// The last-used date range and entity filter survive visits to other pages:
// the SPA never reloads, so these module-level values outlive the
// component's unmount/remount across route switches (same pattern as Help's
// scroll memory). A full page reload still starts from the defaults.
let lastRangeKey = '1d';
let lastCustom: { since: dayjs.Dayjs; until: dayjs.Dayjs } | null = null;
let lastFilter: ActivityFilter | null = null;

const Activity: React.FC = () => {
  const { token } = theme.useToken();
  const [tab, setTab] = useState<TabKey>('overview');
  // Default: Past 24 Hours — the window most users care about first.
  const [rangeKey, setRangeKey] = useState(lastRangeKey);
  const [custom, setCustom] = useState<{ since: dayjs.Dayjs; until: dayjs.Dayjs } | null>(lastCustom);
  const [loading, setLoading] = useState(false);
  // Trends "Explore" links hand the metric/grouping to the Explore tab.
  const [exploreInit, setExploreInit] = useState<ExploreOpts>({});
  // "now" is a state so Refresh recomputes the date window to the current
  // moment — otherwise the ranges freeze at page-load time and the data
  // never updates. Each change slides the range and re-fetches in place
  // (the pages below keep the previous data visible while refreshing).
  const [now, setNow] = useState(() => dayjs());
  const ranges = useMemo(() => makeRanges(now), [now]);

  // Global entity filter (Model / API Key / App), applied to every tab via
  // filter_type/filter_value on the activity + consumptions requests.
  const [filter, setFilter] = useState<ActivityFilter | null>(lastFilter);
  const [filterOpen, setFilterOpen] = useState(false);
  const [filterMode, setFilterMode] = useState<ActivityFilterType>('model');
  // Option lists: models/apps seen in the current window, plus all keys.
  // Loaded lazily when the panel opens so the 30s range slide doesn't
  // refetch them constantly.
  const [filterOpts, setFilterOpts] = useState<{ models: string[]; apps: string[]; keys: Key[] } | null>(null);
  const [filterOptsLoading, setFilterOptsLoading] = useState(false);
  const [rangeOpen, setRangeOpen] = useState(false);

  const range = useMemo<DateRange>(() => {
    if (rangeKey === CUSTOM_KEY && custom) return customRange(custom.since, custom.until);
    const r = ranges.find(r => r.key === rangeKey) ?? ranges.find(r => r.key === '1d') ?? ranges[0];
    // Preset windows snap BOTH bounds to the bucket grid: the live partial
    // bucket keeps accumulating usage, so showing it makes every 30s
    // auto-refresh read as "accumulating" instead of rolling. Snapped, the
    // window is exactly the preset length, perfectly stable between
    // refreshes, and slides by one bucket when the grid rolls over.
    // Custom ranges keep their exact picked bounds.
    return {
      ...r,
      since: floorWindowUntil(r.since, r.granularity),
      until: floorWindowUntil(r.until, r.granularity),
    };
  }, [rangeKey, custom, ranges]);

  const handleRefresh = useCallback(async () => {
    setLoading(true);
    setNow(dayjs()); // slide the windows to now
    await new Promise(r => setTimeout(r, 50));
    setLoading(false);
  }, []);

  // Keep the displayed range fresh: slide "now" every 30s (the window
  // presets and the queries are derived from it) and when switching tabs.
  useEffect(() => {
    const t = setInterval(() => setNow(dayjs()), 30000);
    return () => clearInterval(t);
  }, []);
  useEffect(() => { setNow(dayjs()); }, [tab]);

  // Save the current selections so the next mount (returning from another
  // page) restores them instead of resetting to the defaults.
  useEffect(() => {
    lastRangeKey = rangeKey;
    lastCustom = custom;
    lastFilter = filter;
  }, [rangeKey, custom, filter]);

  const onTabChange = (k: string) => {
    // A direct Explore-tab click (not a Trends "Explore" link) should start
    // with the defaults, not stale state from an earlier link click.
    if (k === 'explore') setExploreInit({});
    setTab(k as TabKey);
  };

  // Trends' "Explore" links (chart + trending card headers, trending rows)
  // switch to the Explore tab, seeding it with the section's metric/grouping.
  const handleNavigate = useCallback((tab: TabKey, opts?: ExploreOpts) => {
    if (opts) setExploreInit(opts);
    setTab(tab);
  }, []);

  // --- Filter panel --------------------------------------------------------

  // Fetch the entity options when the panel opens: models/apps are derived
  // from the consumption rows of the CURRENT window (only entities with
  // usage can be filtered), keys from the key table. The window is captured
  // at open time — `range` stays OUT of the deps so the 30s slide doesn't
  // refetch (and flicker the spinner) while the panel stays open.
  const rangeRef = useRef(range);
  rangeRef.current = range;
  useEffect(() => {
    if (!filterOpen) return;
    let cancelled = false;
    setFilterOptsLoading(true);
    Promise.all([
      getConsumptions({ since: rangeRef.current.since.toISOString(), until: rangeRef.current.until.toISOString() }),
      getKeys(),
    ])
      .then(([c, k]) => {
        if (cancelled) return;
        const cmp = (a: string, b: string) => a.localeCompare(b);
        const models = [...new Set(c.data.map(x => x.model_name || 'Unknown'))].sort(cmp);
        const apps = [...new Set(c.data.map(x => x.app_name || 'Unknown'))].sort(cmp);
        const keys = [...k.data].sort((a, b) =>
          (a.name || `Key #${a.id}`).localeCompare(b.name || `Key #${b.id}`));
        setFilterOpts({ models, apps, keys });
      })
      .catch(() => { if (!cancelled) message.error('Failed to load filter options'); })
      .finally(() => { if (!cancelled) setFilterOptsLoading(false); });
    return () => { cancelled = true; };
  }, [filterOpen]);

  const onFilterOpenChange = (open: boolean) => {
    setFilterOpen(open);
    // Reopening with an active filter shows the dimension it lives in.
    if (open && filter) setFilterMode(filter.type);
  };

  const applyFilter = (value: string, label: string) => {
    setFilter({ type: filterMode, value, label });
    setFilterOpen(false);
  };

  const clearFilter = () => setFilter(null);

  const keyNameById = useMemo(
    () => new Map((filterOpts?.keys ?? []).map(k => [String(k.id), k.name || `Key #${k.id}`])),
    [filterOpts],
  );

  // Select options for the current dimension. searchText backs the client
  // filter so ReactNode labels don't break optionFilterProp.
  const filterOptions = useMemo(() => {
    const opts = filterOpts ?? { models: [], apps: [], keys: [] };
    if (filterMode === 'model') {
      return opts.models.map(m => ({
        value: m,
        searchText: m,
        label: (
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
            <EntityIcon name={m} />
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{m}</span>
          </span>
        ),
      }));
    }
    if (filterMode === 'key') {
      return opts.keys.map(k => {
        const name = k.name || `Key #${k.id}`;
        return {
          value: String(k.id),
          searchText: `${name} ${maskKey(k.key_value)}`,
          label: (
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
              <span style={{ width: 8, height: 8, borderRadius: '50%', background: token.colorPrimary, flexShrink: 0 }} />
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{name}</span>
              <Text type="secondary" style={{ fontSize: 11, marginLeft: 'auto', flexShrink: 0 }}>{maskKey(k.key_value)}</Text>
            </span>
          ),
        };
      });
    }
    return opts.apps.map(a => ({
      value: a,
      searchText: a,
      label: (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
          <span style={{ width: 8, height: 8, borderRadius: 2, background: '#94a3b8', flexShrink: 0 }} />
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a}</span>
        </span>
      ),
    }));
  }, [filterMode, filterOpts, token.colorPrimary]);

  const filterPanel = (
    <div style={{ width: 300 }}>
      <div style={{ fontSize: 11, color: 'rgba(120,120,140,0.75)', textTransform: 'uppercase', letterSpacing: '0.04em', marginBottom: 8 }}>
        Filter by
      </div>
      <Segmented
        block
        size="small"
        value={filterMode}
        onChange={(v) => setFilterMode(v as ActivityFilterType)}
        options={FILTER_TYPES.map(t => ({ value: t.value, label: t.label }))}
      />
      <Select
        style={{ width: '100%', marginTop: 10 }}
        placeholder={filterMode === 'model' ? 'Select a model…' : filterMode === 'key' ? 'Select an API key…' : 'Select an app…'}
        showSearch
        loading={filterOptsLoading}
        value={filter && filter.type === filterMode ? filter.value : undefined}
        options={filterOptions}
        optionFilterProp="searchText"
        onChange={(v: string) => {
          const label = filterMode === 'key' ? keyNameById.get(v) ?? v : v;
          applyFilter(v, label);
        }}
      />
      {filter && (
        <div style={{ marginTop: 12, paddingTop: 10, borderTop: `1px solid rgba(120,120,140,0.14)`, display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={{ width: 8, height: 8, borderRadius: '50%', background: token.colorPrimary, flexShrink: 0 }} />
          <Text style={{ fontSize: 12, flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {FILTER_TYPES.find(t => t.value === filter.type)?.label}: {filter.label}
          </Text>
          <Button size="small" onClick={clearFilter}>Clear</Button>
        </div>
      )}
    </div>
  );

  // --- Date range panel ----------------------------------------------------

  // Collapsed to ONLY the custom entry while a custom range is active: the
  // trigger already names the selection, so showing the preset lists again
  // duplicates "Custom range" top (trigger) and bottom (panel button). The
  // "Show all ranges" link restores the preset lists.
  const [showAllRanges, setShowAllRanges] = useState(false);

  const onPresetClick = (key: string) => {
    if (key === CUSTOM_KEY) {
      // Entering custom mode immediately applies a default window
      // (last month) so the charts and the trigger stay consistent
      // until the user picks their own dates.
      setCustom(custom ?? { since: now.subtract(1, 'month'), until: now });
      setShowAllRanges(false);
    }
    setRangeKey(key);
    setRangeOpen(false);
  };

  const rangeCell = (r: DateRange, active: boolean) => (
    <button
      key={r.key}
      type="button"
      onClick={() => onPresetClick(r.key)}
      style={{
        display: 'flex', alignItems: 'center', gap: 6, padding: '5px 6px', borderRadius: 8, cursor: 'pointer',
        border: active ? `1px solid ${token.colorPrimary}` : '1px solid transparent',
        background: active ? token.colorPrimaryBg : 'transparent',
        color: 'inherit', fontFamily: 'inherit', fontSize: 12, textAlign: 'left', minWidth: 0,
      }}
    >
      <Chip>{r.badge}</Chip>
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{r.label}</span>
    </button>
  );

  const customButton = (active: boolean, onClick?: () => void) => (
    <button
      type="button"
      onClick={onClick ?? (() => onPresetClick(CUSTOM_KEY))}
      style={{
        width: '100%', display: 'flex', alignItems: 'center', gap: 8, padding: '7px 8px', marginTop: 10,
        borderRadius: 8, cursor: 'pointer', fontFamily: 'inherit', fontSize: 12, textAlign: 'left', color: 'inherit',
        border: active ? `1px solid ${token.colorPrimary}` : '1px solid transparent',
        background: active ? token.colorPrimaryBg : token.colorFillTertiary,
      }}
    >
      <CalendarOutlined style={{ fontSize: 13, opacity: 0.7 }} />
      {CUSTOM_LABEL}…
    </button>
  );

  const rangePanel = rangeKey === CUSTOM_KEY && !showAllRanges ? (
    <div style={{ width: 344 }}>
      {/* Clicking the already-active custom entry (or the link) restores the
          preset lists instead of dismissing the panel — a dead close would
          strand the user with no way back to the presets. */}
      {customButton(true, () => setShowAllRanges(true))}
      <button
        type="button"
        onClick={() => setShowAllRanges(true)}
        style={{
          width: '100%', marginTop: 2, padding: '4px 8px', border: 'none', background: 'none', cursor: 'pointer',
          fontFamily: 'inherit', fontSize: 12, textAlign: 'left', color: token.colorPrimary,
        }}
      >
        Show all ranges
      </button>
    </div>
  ) : (
    <div style={{ width: 344 }}>
      <div style={{ fontSize: 11, color: 'rgba(120,120,140,0.75)', textTransform: 'uppercase', letterSpacing: '0.04em', padding: '2px 2px 6px' }}>
        Rolling
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 4 }}>
        {ranges.slice(0, 9).map(r => rangeCell(r, rangeKey === r.key))}
      </div>
      <div style={{ fontSize: 11, color: 'rgba(120,120,140,0.75)', textTransform: 'uppercase', letterSpacing: '0.04em', padding: '10px 2px 6px' }}>
        Calendar
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 4 }}>
        {ranges.slice(9).map(r => rangeCell(r, rangeKey === r.key))}
      </div>
      {customButton(rangeKey === CUSTOM_KEY)}
    </div>
  );

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8, flexWrap: 'wrap', gap: 8 }}>
        <div>
          <Title level={3} style={{ margin: 0 }}>Activity</Title>
          <Text type="secondary" style={{ fontSize: 12 }}>
            Your usage across models on KeyRouter
          </Text>
        </div>
        <Space wrap>
          {/* Filter button: narrows every tab to one model / API key / app. */}
          <Popover trigger="click" open={filterOpen} onOpenChange={onFilterOpenChange} placement="bottomLeft" content={filterPanel}>
            <Button style={filter ? { borderColor: token.colorPrimary, color: token.colorPrimary } : undefined}>
              <FilterOutlined />
              {filter ? (
                <span style={{ maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'inline-block', verticalAlign: 'middle' }}>
                  {FILTER_TYPES.find(t => t.value === filter.type)?.label}: {filter.label}
                </span>
              ) : 'Filter'}
            </Button>
          </Popover>
          {/* Date range: a panel with the Rolling / Calendar presets. While a
              custom range is active the trigger shows just the label — the
              start/end pickers next to it display the actual dates, so the
              same range never renders twice. */}
          <Popover trigger="click" open={rangeOpen} onOpenChange={setRangeOpen} placement="bottomLeft" content={rangePanel}>
            <Button style={{ minWidth: 320, display: 'inline-flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, overflow: 'hidden', minWidth: 0 }}>
                {rangeKey === CUSTOM_KEY
                  ? <Chip><CalendarOutlined style={{ fontSize: 11 }} /></Chip>
                  : <Chip>{range.badge}</Chip>}
                <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {rangeKey === CUSTOM_KEY ? CUSTOM_LABEL : <span className="tabular-nums">{rangeText(range)}</span>}
                </span>
              </span>
              <DownOutlined style={{ fontSize: 10, opacity: 0.6, flexShrink: 0 }} />
            </Button>
          </Popover>
          {rangeKey === CUSTOM_KEY && custom && (
            // Two independent pickers (not a RangePicker): clicking the end
            // picker edits ONLY the end date — the RangePicker forced a
            // start-then-end order and ignored direct end-date edits. A pick
            // that would invert the range swaps the bounds (like the
            // RangePicker's order behavior) so the window never inverts.
            <Space size={4}>
              <DatePicker
                showTime
                format="MMM D, h:mm a"
                placeholder="Start"
                value={custom.since}
                onChange={(d) => {
                  if (!d || !custom) return;
                  setCustom(d.isAfter(custom.until)
                    ? { since: custom.until, until: d }
                    : { ...custom, since: d });
                }}
              />
              <Text type="secondary" style={{ fontSize: 12 }}>–</Text>
              <DatePicker
                showTime
                format="MMM D, h:mm a"
                placeholder="End"
                value={custom.until}
                onChange={(d) => {
                  if (!d || !custom) return;
                  setCustom(d.isBefore(custom.since)
                    ? { since: d, until: custom.since }
                    : { ...custom, until: d });
                }}
              />
            </Space>
          )}
          <Button type="text" icon={<ReloadOutlined />} loading={loading} onClick={handleRefresh} aria-label="Refresh" />
        </Space>
      </div>

      <Segmented
        block
        style={{ marginBottom: 16 }}
        value={tab}
        onChange={onTabChange}
        options={[
          { label: 'Overview', value: 'overview' },
          { label: 'Trends', value: 'trends' },
          { label: 'Explore', value: 'explore' },
        ]}
      />

      {loading && <Spin style={{ display: 'block', margin: '40px auto' }} />}

      {/* No key= remount: pages re-fetch on range change and keep the
          previous content visible while refreshing — a refresh must never
          blank the page. */}
      {tab === 'overview' && <ActivityOverview range={range} filter={filter} onNavigate={handleNavigate} />}
      {tab === 'trends' && <ActivityTrends range={range} filter={filter} onNavigate={handleNavigate} />}
      {tab === 'explore' && <ActivityExplore range={range} filter={filter} initialMetric={exploreInit.metric} initialGroupBy={exploreInit.groupBy} />}
    </div>
  );
};

export default Activity;
