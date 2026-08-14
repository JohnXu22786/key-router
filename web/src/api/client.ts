import axios from 'axios';

const api = axios.create({
  baseURL: '/api',
  timeout: 10000,
});

export interface Provider {
  id: number;
  name: string;
  type: 'openai' | 'anthropic';
  base_url: string;
  extra_headers: string;
  created_at: string;
  updated_at: string;
}

export interface Key {
  id: number;
  provider_id: number;
  name: string;
  key_value: string;
  status: 'active' | 'rate_limited' | 'disabled' | 'testing';
  recovery_strategy: 'immediate' | 'lazy';
  rate_limited_until: string | null;
  disabled_reason: string;
  rpm_limit: number;
  tpm_limit: number;
  rp5h_limit: number;
  rp5h_metric: string;
  rpd_limit: number;
  rpd_metric: string;
  rpw_limit: number;
  rpw_metric: string;
  rpm_month_limit: number;
  rpm_metric: string;
  total_spend_limit: number;
  total_spent: number;
  sort_order: number;
  counts?: Record<string, { count: number; token_count: number }>;
  // Window types whose limits are currently exhausted (e.g. "rpm", "rpd") —
  // why the selector skips this key even though its status is "active".
  limited_windows?: string[];
  provider?: Provider;
  created_at: string;
  updated_at: string;
}

export interface ModelGroup {
  id: number;
  group_id: string;
  name: string;
  enabled: boolean;
  retry_times: number;
  context_length: number;
  max_output_tokens: number;
  created_at: string;
  updated_at: string;
}

export interface Route {
  id: number;
  model_group_id: number;
  provider_id: number;
  target_model: string;
  priority: number;
  weight: number;
  enabled: boolean;
  prompt_per_1m: number;
  completion_per_1m: number;
  cache_read_per_1m: number;
  cache_write_per_1m: number;
  extra_params: string;
  model_group?: ModelGroup;
  provider?: Provider;
  created_at: string;
  updated_at: string;
}

export interface Pricing {
  id: number;
  model_name: string;
  prompt_per_1k: number;
  completion_per_1k: number;
  cache_read_per_1k: number;
  cache_write_per_1k: number;
  created_at: string;
  updated_at: string;
}

export interface Settings {
  [key: string]: string;
}

export interface OverviewStats {
  total_requests: number;
  total_cost: number;
  total_input_tokens: number;
  total_output_tokens: number;
  active_keys: number;
  disabled_keys: number;
  total_keys: number;
  total_providers: number;
}

export interface Consumption {
  id: number;
  key_id: number;
  hour_bucket: string;
  model_name: string;
  app_name: string;
  request_count: number;
  input_tokens: number;
  output_tokens: number;
  cache_hit_tokens: number;
  cache_write_tokens: number;
  cost_usd: number;
  key?: Key;
}

// Providers
export const getProviders = () => api.get<Provider[]>('/providers');
export const createProvider = (data: Partial<Provider>) => api.post<Provider>('/providers', data);
export const updateProvider = (id: number, data: Partial<Provider>) => api.put<Provider>(`/providers/${id}`, data);
export const deleteProvider = (id: number) => api.delete(`/providers/${id}`);

// Keys
export const getKeys = () => api.get<Key[]>('/keys');
export const createKey = (data: Partial<Key>) => api.post<Key>('/keys', data);
export const updateKey = (id: number, data: Partial<Key>) => api.put<Key>(`/keys/${id}`, data);
export const deleteKey = (id: number) => api.delete(`/keys/${id}`);
export const reorderKeys = (keys: { id: number; sort_order: number }[]) => api.post('/keys/reorder', { keys });
export const resetKeySpend = (id: number) => api.post(`/keys/${id}/reset-spend`);

// Model Groups
export const getModelGroups = () => api.get<ModelGroup[]>('/model-groups');
export const createModelGroup = (data: Partial<ModelGroup>) => api.post<ModelGroup>('/model-groups', data);
export const updateModelGroup = (id: number, data: Partial<ModelGroup>) => api.put<ModelGroup>(`/model-groups/${id}`, data);
export const deleteModelGroup = (id: number) => api.delete(`/model-groups/${id}`);

// Routes
export const getRoutes = () => api.get<Route[]>('/routes');
export const createRoute = (data: Partial<Route>) => api.post<Route>('/routes', data);
export const updateRoute = (id: number, data: Partial<Route>) => api.put<Route>(`/routes/${id}`, data);
export const deleteRoute = (id: number) => api.delete(`/routes/${id}`);
export const reorderRoutes = (routes: { id: number; priority: number }[]) => api.post('/routes/reorder', { routes });

// Pricing
export const getPricings = () => api.get<Pricing[]>('/pricings');
export const createPricing = (data: Partial<Pricing>) => api.post<Pricing>('/pricings', data);
export const updatePricing = (id: number, data: Partial<Pricing>) => api.put<Pricing>(`/pricings/${id}`, data);
export const deletePricing = (id: number) => api.delete(`/pricings/${id}`);

// Settings
export const getSettings = () => api.get<Settings>('/settings');
export const updateSettings = (data: Settings) => api.put('/settings', data);
export const getAutostart = () => api.get<{ enabled: boolean; supported: boolean }>('/autostart');
export const setAutostart = (enabled: boolean) => api.put('/autostart', { enabled });

// Stats
export const getOverview = () => api.get<OverviewStats>('/stats/overview');
export const getConsumptions = (params?: { key_id?: number; since?: string; until?: string; filter_type?: string; filter_value?: string }) =>
  api.get<Consumption[]>('/stats/consumptions', { params });
export const getKeyDetail = (id: number) => api.get(`/stats/keys/${id}`);

// Activity (OpenRouter-style: overview / trends / explore)
export interface ActivitySeriesPoint { bucket: string; group: string; subgroup?: string; value: number; is_zero: boolean; }
export interface ActivityGroupSummary { group: string; min: number; max: number; avg: number; sum: number; value: number; percent: number; }
export interface ActivityResponse {
  metric: string;
  group_by: string;
  rollup: string;
  series: ActivitySeriesPoint[];
  summary: ActivityGroupSummary[];
  buckets: string[];
  totals: { spend: number; tokens: number; requests: number; cache: number };
}
export const getActivity = (params: { metric?: string; group_by?: string; subgroup?: string; rollup?: string; top?: number; since?: string; until?: string; filter_type?: string; filter_value?: string }) =>
  api.get<ActivityResponse>('/stats/activity', { params });

// Actions
export const reloadConfig = () => api.post('/reload');

// Updates
export interface UpdateInfo {
  current_version: string;
  latest_version: string;
  update_available: boolean;
  install_mode: 'portable' | 'installed';
  asset_name?: string;
  asset_url?: string;
  asset_size?: number;
  checked_at?: string;
  error?: string;
}
export const checkUpdate = () => api.post<UpdateInfo>('/updates/check');
// The apply request blocks while the installer downloads (tens of MB) and
// the UAC prompt is answered — the global 10s timeout would abort it. The
// timeout matches the backend's 30-minute download deadline.
export const applyUpdate = () => api.post('/updates/apply', null, { timeout: 30 * 60 * 1000 });
export const getAutoCheckState = () => api.get<UpdateInfo & { checked: boolean }>('/updates/state');

// Health
export const getHealth = () => api.get('/health');
export const getKeyStatuses = () => api.get('/status/keys');

export default api;
