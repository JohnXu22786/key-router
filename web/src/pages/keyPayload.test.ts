import { describe, it, expect } from 'vitest';
import { buildKeyPayload, changedFields, windowTypes } from './keyPayload';

// Helpers mirror the form contract: `values` is the full form store at save
// time (validateFields returns untouched fields too) and `baseline` is the
// snapshot taken right when the edit dialog opened. Only fields whose value
// differs between the two are sent — NOT antd's isFieldTouched, which
// setFieldsValue marks true for every prefilled field (ant-design/ant-design
// #53981) and would therefore re-send the whole key on every edit.
const edit = (values: Record<string, any>, baseline: Record<string, any>) =>
  ({ editing: true, baseline });

describe('buildKeyPayload — create path (sends everything)', () => {
  it('converts cost-metric window limits and the lifetime budget from USD to micro-USD', () => {
    const payload = buildKeyPayload(
      { provider_id: 1, name: 'k', rpd_metric: 'cost', rpd_limit: 3, total_spend_limit: 30, tpm_limit: 200000 },
      { editing: false, baseline: null },
    );
    expect(payload.rpd_limit).toBe(3_000_000);
    expect(payload.total_spend_limit).toBe(30_000_000);
    // Non-cost windows are stored as raw counts — converted/non-converted
    // both untouched.
    expect(payload.tpm_limit).toBe(200000);
  });

  it('keeps 0 and empty limits as unlimited (no conversion, no capping)', () => {
    const payload = buildKeyPayload(
      { rpd_metric: 'cost', rpd_limit: 0, total_spend_limit: 0 },
      { editing: false, baseline: null },
    );
    expect(payload.rpd_limit).toBe(0);
    expect(payload.total_spend_limit).toBe(0);
  });
});

describe('buildKeyPayload — edit path sends only the fields that actually changed', () => {
  it('name-only edit sends only the name: limits and key_value are not re-sent', () => {
    // Regression: an edit must diff against the baseline taken when the modal
    // opened, so a rename save sends nothing but the name — it must not
    // re-transmit the full secret nor overwrite a window's limit with the
    // stale loaded copy (the app may have live-updated the key meanwhile).
    const values = { name: 'renamed', key_value: 'sk-secret', rpd_metric: 'cost', rpd_limit: 5000, provider_id: 7, status: 'active' };
    const baseline = { ...values, name: 'old name' };
    expect(buildKeyPayload(values, edit(values, baseline))).toEqual({ name: 'renamed' });
  });

  it('an edit that changes nothing sends an empty payload (no stale overwrite, no secret re-send)', () => {
    const values = { name: 'k', key_value: 'sk-secret', rpd_metric: 'cost', rpd_limit: 5000, provider_id: 7, total_spend_limit: 30 };
    expect(buildKeyPayload(values, edit(values, { ...values }))).toEqual({});
  });

  it('a real change somewhere keeps the rest out (status flip sends only status)', () => {
    const values = { name: 'k', status: 'disabled', provider_id: 7, rpd_metric: 'requests', rpd_limit: 5000 };
    const baseline = { name: 'k', status: 'active', provider_id: 7, rpd_metric: 'requests', rpd_limit: 5000 };
    expect(buildKeyPayload(values, edit(values, baseline))).toEqual({ status: 'disabled' });
  });

  it('editing only the limit of a cost window still converts that limit', () => {
    const values = { rpd_metric: 'cost', rpd_limit: 50 };
    const baseline = { rpd_metric: 'cost', rpd_limit: 100 };
    const payload = buildKeyPayload(values, edit(values, baseline));
    expect(payload).toEqual({ rpd_limit: 50_000_000 });
  });

  it('edit of only the lifetime budget converts and sends it', () => {
    const values = { name: 'k', total_spend_limit: 30 };
    const baseline = { name: 'k', total_spend_limit: 10 };
    expect(buildKeyPayload(values, edit(values, baseline))).toEqual({ total_spend_limit: 30_000_000 });
  });

  it('clearing a limit (5000 -> empty/null) is detected as a change and sent', () => {
    const values = { name: 'k', rpd_limit: null };
    const baseline = { name: 'k', rpd_limit: 5000 };
    expect(buildKeyPayload(values, edit(values, baseline))).toEqual({ rpd_limit: null });
  });

  it('baseline-only fields (id, total_spent, ...) never leak into the payload', () => {
    // The baseline is built from the whole loaded key ({ ...k }), which carries
    // fields the form has no Field for (id, total_spent, disabled_reason, ...).
    // Even though they "differ" from the absent save-time values, they must
    // never be sent — only the form fields change what an edit writes.
    const values = { name: 'renamed' };
    const baseline = { id: 5, total_spent: 12345, disabled_reason: 'x', name: 'old' };
    expect(buildKeyPayload(values, edit(values, baseline))).toEqual({ name: 'renamed' });
  });
});

describe('buildKeyPayload — edit path force-sends a window limit with its metric', () => {
  it('metric-only edit: switching a window from requests to cost re-sends the converted limit', () => {
    // Regression: switching only the Daily metric Requests -> Cost without
    // retyping the limit must re-send the limit under the new metric's unit —
    // dropping it would leave the server's raw 5000 as 5000 micro-USD
    // ($0.005), silently capping the key.
    const values = { name: 'k', rpd_metric: 'cost', rpd_limit: 5000 };
    const baseline = { name: 'k', rpd_metric: 'requests', rpd_limit: 5000 };
    expect(buildKeyPayload(values, edit(values, baseline)))
      .toEqual({ rpd_metric: 'cost', rpd_limit: 5_000_000_000 });
  });

  it('metric-only edit: switching a window from cost to requests re-sends the raw limit', () => {
    // Inverse direction: a cost window stored as 5e9 micro-USD ($5000) opens
    // showing 5000. Switching Daily Metric Cost -> Requests must send 5000 as
    // a raw request count, not leave the server's 5e9 meaning "5e9 requests".
    const values = { rpd_metric: 'requests', rpd_limit: 5000 };
    const baseline = { rpd_metric: 'cost', rpd_limit: 5000 };
    expect(buildKeyPayload(values, edit(values, baseline)))
      .toEqual({ rpd_metric: 'requests', rpd_limit: 5000 });
  });

  it('does not ship a fractional limit into a non-cost metric (would 400 the int64 bind)', () => {
    // A stored cost limit opens as stored/1e6, so a sub-dollar cost window
    // ("$0.005" — exact shape of the mangled keys the old bug produced, or a
    // legit "$12.50") shows a fraction. Flipping only its metric to requests
    // must leave the fractional limit un-sent: the backend int64 bind rejects
    // non-integers, and the stored integer stays valid in the new unit.
    const values = { rpd_metric: 'requests', rpd_limit: 0.005 };
    const baseline = { rpd_metric: 'cost', rpd_limit: 0.005 };
    const payload = buildKeyPayload(values, edit(values, baseline));
    expect(payload).toEqual({ rpd_metric: 'requests' });
    expect(payload.rpd_limit).toBeUndefined();
  });

  it('metric-only edit force-sends only the changing window\u2019s limit, not untouched windows\u2019', () => {
    const values = { rpd_metric: 'cost', rpd_limit: 5000, rp5h_limit: 999999, rp5h_metric: 'requests', rpw_limit: 888, rpw_metric: 'tokens' };
    const baseline = { rpd_metric: 'requests', rpd_limit: 5000, rp5h_limit: 999999, rp5h_metric: 'requests', rpw_limit: 888, rpw_metric: 'tokens' };
    const payload = buildKeyPayload(values, edit(values, baseline));
    expect(payload).toEqual({ rpd_metric: 'cost', rpd_limit: 5_000_000_000 });
  });

  it('a whole-number metric flip in an otherwise-full edit keeps the limit (no drop)', () => {
    // The change-diff can legitimately cover every field (user edited
    // everything). Under that full-resend the reconcile must still pair an
    // integer limit with its metric instead of dropping it.
    const values = { name: 'n2', rpd_metric: 'requests', rpd_limit: 5000 };
    const baseline = { name: 'n1', rpd_metric: 'cost', rpd_limit: 4000 };
    const payload = buildKeyPayload(values, edit(values, baseline));
    expect(payload).toEqual({ name: 'n2', rpd_metric: 'requests', rpd_limit: 5000 });
  });
});

describe('changedFields — the value diff that backs an edit save', () => {
  it('reports exactly the fields whose value differs between current and baseline', () => {
    expect(Array.from(changedFields({ a: 1, b: 2, c: 3 }, { a: 1, b: 3, c: 4 })).sort())
      .toEqual(['b', 'c']);
  });

  it('treats undefined, null, and 0 as distinct values (a cleared limit is a real edit)', () => {
    expect(changedFields({ a: null }, { a: undefined }).has('a')).toBe(true);
    expect(changedFields({ a: 0 }, { a: undefined }).has('a')).toBe(true);
    expect(changedFields({ a: undefined }, { a: undefined }).has('a')).toBe(false);
  });

  it('is symmetric about which side declares a key', () => {
    expect(changedFields({ a: 1 }, {}).has('a')).toBe(true);
    expect(changedFields({}, { b: 2 }).has('b')).toBe(true);
  });
});

describe('windowTypes table sanity', () => {
  it('windows with a metric dropdown all declare a metricField', () => {
    for (const wt of windowTypes) {
      if (wt.key === 'rpm' || wt.key === 'tpm') continue;
      expect(wt.metricField, wt.key).toBeDefined();
    }
  });
});
