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
  // The prefilled values the form was opened with (after USD display
  // conversion), used to diff against the values at save time. Only fields
  // whose value actually changed are sent — see the edit rule below. Ignored
  // on create (sends everything); required on edit. Defensive only: if a
  // caller edits without a baseline, changedFields falls back to treating
  // every field as changed (a full resend) so no user edit is lost.
  baseline: Record<string, any> | null;
}

export class KeyPayloadValidationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'KeyPayloadValidationError';
  }
}

function validateCountLimits(values: Record<string, any>, ctx: KeyBuildContext) {
  for (const wt of windowTypes) {
    const limit = values[wt.limitField];
    if (limit == null || Number.isInteger(limit)) continue;

    const metric = wt.metricField ? values[wt.metricField] : undefined;
    if (metric === 'cost') continue;

    // Keep B41's focused validation and explicit-clear handling for cost
    // windows switched to a count metric.
    const wasCost = wt.metricField && ctx.baseline?.[wt.metricField] === 'cost';
    if (ctx.editing && wasCost && (metric === 'requests' || metric === 'tokens')) continue;

    const unit = metric === 'tokens' || wt.key === 'tpm' ? 'tokens' : 'requests';
    throw new KeyPayloadValidationError(`${wt.label} limit must be a whole number of ${unit}.`);
  }
}

// The fields whose value differs between `current` (the form store at save
// time) and `baseline` (the form store when the edit dialog opened). This — a
// real value comparison — decides what an edit sends. antd's isFieldTouched
// cannot be used: its setFieldsValue marks every prefilled field touched
// (ant-design/ant-design#53981), so "touched" is true for the whole form the
// moment an edit opens and would filter nothing, re-sending the entire key.
export function changedFields(
  current: Record<string, any>,
  baseline: Record<string, any>,
): Set<string> {
  const names = new Set<string>([...Object.keys(current), ...Object.keys(baseline)]);
  const changed = new Set<string>();
  for (const name of names) {
    if (current[name] !== baseline[name]) changed.add(name);
  }
  return changed;
}

// Build the create/update payload for a key from the full form store `values`.
//
// Unit conversion: cost-metric windows and the lifetime budget are entered in
// USD but stored in micro-USD (1e6 per $1). Convert on `values` so BOTH the
// create and edit paths send stored units — the create path sends everything.
// (A missing conversion here once stored a "$30" budget as 30 micro-USD,
// disabling the key the moment its next request pushed total_spent past it.)
//
// Edit rule: editing must NOT reset fields the user did not change — diff the
// save-time values against the baseline snapshot taken when the dialog opened
// and send only the fields whose value differs, so a name-only edit leaves the
// limits exactly as they are. The exception: a window's metric and limit are
// reconciled together (see the loop below), because the limit's unit depends
// on the metric.
export function buildKeyPayload(values: Record<string, any>, ctx: KeyBuildContext): Record<string, any> {
  validateCountLimits(values, ctx);
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
  const changed = changedFields(values, ctx.baseline ?? {});
  for (const key of Object.keys(out)) {
    if (!changed.has(key)) delete payload[key];
  }
  // A fractional USD display value is not a valid request/token count. If an
  // existing cost window is switched to a count metric, require an integer
  // replacement or an explicit clear rather than leaving the stored
  // micro-USD integer behind under the new unit.
  for (const wt of windowTypes) {
    if (!wt.metricField || !changed.has(wt.metricField)) continue;
    const baseline = ctx.baseline;
    if (!baseline || baseline[wt.metricField] !== 'cost') continue;
    const metric = values[wt.metricField];
    if (metric !== 'requests' && metric !== 'tokens') continue;
    const limit = values[wt.limitField];
    if (limit == null) {
      // The API intentionally ignores JSON null for editable fields; zero is
      // its persisted representation for an unlimited window.
      if (baseline[wt.limitField] != null) payload[wt.limitField] = 0;
      continue;
    }
    if (!Number.isInteger(limit)) {
      throw new KeyPayloadValidationError(
        `${wt.label} limit must be a whole-number count for ${metric}. Enter an integer count or clear the limit before saving.`,
      );
    }
  }
  // Metric-only edits: a window's metric and limit must travel together.
  // The change-filter above drops a limit the user did not retype, but
  // if the metric column IS being sent its unit drives how the stored limit
  // is interpreted — dropping the limit leaves the server's raw integer with
  // the NEW unit (e.g. a daily "5000" flipped to cost becomes 5000 micro-USD
  // = $0.005, silently capping the key). Re-send the (already converted)
  // limit whenever the metric row is sent, so the stored value always matches
  // the submitted metric's unit. Name-only edits are unaffected: no metric is
  // in the payload, so no limit is force-sent.
  //
  // A fractional cost display switched to a count metric is rejected above;
  // once the user enters an integer or clears the field, the paired payload
  // below keeps the stored unit consistent with the selected metric.
  for (const wt of windowTypes) {
    if (!wt.metricField || payload[wt.metricField] == null) continue;
    const limit = out[wt.limitField];
    if (limit == null) continue; // empty limit: leave stored value as-is
    if (!Number.isInteger(limit)) { delete payload[wt.limitField]; continue; }
    payload[wt.limitField] = limit;
  }
  if (payload.provider_id == null) delete payload.provider_id;
  return payload;
}
