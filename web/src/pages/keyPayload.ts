import { usdToMicroUsd } from './keyLimits';

// Window definitions used by both the key form and the payload builder.
// rpm/tpm are fixed request/token windows (no metric choice); the rest have
// a metric dropdown (requests | tokens | cost) and their limit's stored unit
// depends on it: cost windows store micro-USD, the others store the raw count.
export const windowTypes = [
  { key: 'rpm', label: 'RPM', limitField: 'rpm_limit' },
  { key: 'tpm', label: 'TPM', limitField: 'tpm_limit' },
  { key: 'rp5h', label: '5 Hour', limitField: 'rp5h_limit', metricField: 'rp5h_metric' },
  { key: 'rpd', label: 'Daily', limitField: 'rpd_limit', metricField: 'rpd_metric' },
  { key: 'rpw', label: 'Weekly', limitField: 'rpw_limit', metricField: 'rpw_metric' },
  { key: 'rpmo', label: 'Monthly', limitField: 'rpm_month_limit', metricField: 'rpm_metric' },
];

export interface KeyBuildContext {
  editing: boolean;
  isTouched: (name: string) => boolean;
  statusTouched: boolean;
}

// Build the create/update payload for a key from the full form store `values`.
//
// Unit conversion: cost-metric windows and the lifetime budget are entered in
// USD but stored in micro-USD (1e6 per $1). Convert on `values` so BOTH the
// create and edit paths send stored units — the create path sends everything.
// (A missing conversion here once stored a "$30" budget as 30 micro-USD,
// disabling the key the moment its next request pushed total_spent past it.)
//
// Edit rule: editing must NOT reset fields the user did not touch — only send
// the fields actually modified, so a name-only edit leaves the limits exactly
// as they were. The exception: a window's metric and limit are reconciled
// together (see the loop below), because the limit's unit depends on the metric.
export function buildKeyPayload(values: Record<string, any>, ctx: KeyBuildContext): Record<string, any> {
  const out: any = { ...values };
  for (const wt of windowTypes) {
    if (out[wt.limitField] == null || out[wt.limitField] === 0) continue;
    if (wt.metricField && out[wt.metricField] === 'cost') {
      out[wt.limitField] = usdToMicroUsd(out[wt.limitField]);
    }
  }
  if (out.total_spend_limit != null && out.total_spend_limit !== 0) {
    out.total_spend_limit = usdToMicroUsd(out.total_spend_limit);
  }
  if (!ctx.editing) return out;
  const payload: any = { ...out };
  for (const key of Object.keys(out)) {
    if (!ctx.isTouched(key)) delete payload[key];
  }
  // Metric-only edits: a window's metric and limit must travel together.
  // The untouched-deletion above drops a limit the user did not retype, but
  // if the metric column IS being sent its unit drives how the stored limit
  // is interpreted — dropping the limit leaves the server's raw integer with
  // the NEW unit (e.g. a daily "5000" flipped to cost becomes 5000 micro-USD
  // = $0.005, silently capping the key). Re-send the (already converted)
  // limit whenever the metric row is sent, so the stored value always matches
  // the submitted metric's unit. Name-only edits are unaffected: no metric is
  // in the payload, so no limit is force-sent.
  //
  // Cost limits are always whole micro-USD (usdToMicroUsd rounds), and any
  // stored limit is a whole integer, so the only fractional raw value here is
  // a cost window's /1e6 USD display flipped to a non-cost metric without a
  // retype (e.g. "$12.50" -> requests). A non-integer has no lossless form in
  // the new unit and the backend int64 bind rejects it (400ing the whole
  // save), so rather than guess or fail we DELETE it and let the server keep
  // the stored integer, which is now a valid count in the new unit.
  for (const wt of windowTypes) {
    if (!wt.metricField || payload[wt.metricField] == null) continue;
    const limit = out[wt.limitField];
    if (limit == null) continue; // empty limit: leave stored value as-is
    if (!Number.isInteger(limit)) { delete payload[wt.limitField]; continue; }
    payload[wt.limitField] = limit;
  }
  if (!ctx.statusTouched) delete payload.status;
  if (payload.provider_id == null) delete payload.provider_id;
  return payload;
}
