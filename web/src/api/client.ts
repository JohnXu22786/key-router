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
  rpd_limit: number;
  rpw_limit: number;
  rpm_month_limit: number;
  counts?: Record<string, { count: number; token_count: number }>;
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

// Pricing
export const getPricings = () => api.get<Pricing[]>('/pricings');
export const createPricing = (data: Partial<Pricing>) => api.post<Pricing>('/pricings', data);
export const updatePricing = (id: number, data: Partial<Pricing>) => api.put<Pricing>(`/pricings/${id}`, data);
export const deletePricing = (id: number) => api.delete(`/pricings/${id}`);

// Settings
export const getSettings = () => api.get<Settings>('/settings');
export const updateSettings = (data: Settings) => api.put('/settings', data);

// Stats
export const getOverview = () => api.get<OverviewStats>('/stats/overview');
export const getConsumptions = (params?: { key_id?: number; since?: string; until?: string }) =>
  api.get<Consumption[]>('/stats/consumptions', { params });
export const getKeyDetail = (id: number) => api.get(`/stats/keys/${id}`);

// Actions
export const reloadConfig = () => api.post('/reload');

// Health
export const getHealth = () => api.get('/health');
export const getKeyStatuses = () => api.get('/status/keys');

export default api;
