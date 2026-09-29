import { getObservedRouteOrderVersion, getRoutes, reorderRoutes } from './client';
import type { RouteOrderVersion } from './client';
import type { Route } from './client';

export interface RouteOrderUpdate {
  id: number;
  priority: number;
}

export const routeOrderPersistence = {
  pending: 0,
  generation: 0,
};

type PendingRouteOrder = { routes: RouteOrderUpdate[]; version: RouteOrderVersion };
const pendingRouteOrders: PendingRouteOrder[] = [];
const completionListeners = new Set<() => void>();

const clientID = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
  ? crypto.randomUUID()
  : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
let lastTimestamp = 0;
let sequence = 0;
let latestPendingRouteOrder: PendingRouteOrder | null = null;

export function subscribeRouteOrderCompletion(listener: () => void) {
  completionListeners.add(listener);
  return () => { completionListeners.delete(listener); };
}

function notifyRouteOrderCompletion() {
  for (const listener of completionListeners) listener();
}

function removePendingRouteOrder(pending: PendingRouteOrder) {
  const index = pendingRouteOrders.indexOf(pending);
  if (index >= 0) pendingRouteOrders.splice(index, 1);
  if (latestPendingRouteOrder === pending) {
    latestPendingRouteOrder = pendingRouteOrders[pendingRouteOrders.length - 1] ?? null;
  }
}

function nextRouteOrderVersion(): RouteOrderVersion {
  const now = typeof performance !== 'undefined' && Number.isFinite(performance.timeOrigin)
    ? performance.timeOrigin + performance.now()
    : Date.now();
  const serverFloor = getObservedRouteOrderVersion()?.timestamp ?? 0;
  lastTimestamp = Math.max(now, lastTimestamp + 0.001, serverFloor + 0.001);
  sequence++;
  return { timestamp: lastTimestamp, client_id: clientID, sequence };
}

function flushPendingRouteOrder() {
  const pending = latestPendingRouteOrder;
  if (!pending) return;

  const body = new Blob([JSON.stringify({ routes: pending.routes, order_version: pending.version })], {
    type: 'application/json',
  });
  if (typeof navigator !== 'undefined' && typeof navigator.sendBeacon === 'function' &&
    navigator.sendBeacon('/api/routes/reorder', body)) {
    return;
  }
  void fetch('/api/routes/reorder', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body,
    credentials: 'same-origin',
    keepalive: true,
  }).catch(() => {});
}

if (typeof window !== 'undefined') {
  window.addEventListener('pagehide', flushPendingRouteOrder);
}

function isVersionConflict(error: unknown) {
  return (error as { response?: { status?: number } })?.response?.status === 409;
}

export function persistRouteOrder(routes: RouteOrderUpdate[]): Promise<boolean> {
  routeOrderPersistence.pending++;
  const version = nextRouteOrderVersion();
  const pending = { routes: [...routes], version };
  pendingRouteOrders.push(pending);
  latestPendingRouteOrder = pending;
  return reorderRoutes(routes, version).then(() => {
    removePendingRouteOrder(pending);
    return true;
  }).catch(error => {
    removePendingRouteOrder(pending);
    if (!isVersionConflict(error)) throw error;
    return false;
  }).finally(() => {
    routeOrderPersistence.pending--;
    routeOrderPersistence.generation++;
    notifyRouteOrderCompletion();
  });
}

export function refreshRouteOrderIfIdle(
  setRoutes: (routes: Route[]) => void,
  canApply: () => boolean = () => true,
): Promise<boolean> {
  if (routeOrderPersistence.pending > 0) return Promise.resolve(false);
  const generation = routeOrderPersistence.generation;
  const isCurrent = () => routeOrderPersistence.pending === 0 && routeOrderPersistence.generation === generation && canApply();
  const refresh = async (retry: boolean): Promise<boolean> => {
    try {
      const { data } = await getRoutes();
      if (!isCurrent()) return false;
      setRoutes(data);
      return true;
    } catch (error) {
      if (!isCurrent() || !retry) throw error;
      await new Promise(resolve => setTimeout(resolve, 250));
      if (!isCurrent()) return false;
      return refresh(false);
    }
  };
  return refresh(true);
}
