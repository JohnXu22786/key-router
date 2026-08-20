import { describe, it, expect } from 'vitest';
import { buildKeyPayload, windowTypes } from './keyPayload';

// Helpers: mirror how the real form behaves — `values` is the full form store
// (validateFields returns untouched fields too) and `isTouched` reports only
// the fields the user actually edited.
const untouched = () => false;
const edited = (field: string) => (name: string) => name === field;
// Models a form whose fields were ALL marked touched — how antd v6 behaves on
// edits after the first modal open (setFieldsValue marks prefilled fields
// touched), which makes the untouched-deletion filter inert.
const allTouched = () => true;

describe('buildKeyPayload — create path (no touched filtering)', () => {
  it('converts cost-metric window limits and the lifetime budget from USD to micro-USD', () => {
    const payload = buildKeyPayload(
      { provider_id: 1, name: 'k', rpd_metric: 'cost', rpd_limit: 3, total_spend_limit: 30, tpm_limit: 200000 },
      { editing: false, isTouched: untouched, statusTouched: false },
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
      { editing: false, isTouched: untouched, statusTouched: false },
    );
    expect(payload.rpd_limit).toBe(0);
    expect(payload.total_spend_limit).toBe(0);
  });
});

describe('buildKeyPayload — edit path force-sends a window limit with its metric', () => {
  it('metric-only edit: switching a window from requests to cost re-sends the converted limit', () => {
    // Regression: the daily window was requests-rpd_limit 5000. The user
    // switched ONLY the Daily metric Requests -> Cost (drop-down) and saved
    // without retyping the limit. The old code deleted rpd_limit (untouched)
    // while sending rpd_metric='cost', so the server kept the raw 5000 and
    // reinterpreted it as 5000 micro-USD ($0.005), silently capping the key.
    const payload = buildKeyPayload(
      { name: 'k', rpd_metric: 'cost', rpd_limit: 5000 },
      { editing: true, isTouched: edited('rpd_metric'), statusTouched: false },
    );
    expect(payload.rpd_metric).toBe('cost');
    // 5000 USD -> micro-USD, and it must NOT be dropped from the payload.
    expect(payload.rpd_limit).toBe(5_000_000_000);
  });

  it('metric-only edit: switching a window from cost to requests re-sends the raw limit', () => {
    // Inverse direction: a cost window stored as 5e9 micro-USD ($5000) opens
    // showing 5000. Switching Daily Metric Cost -> Requests must send 5000 as
    // a raw request count, not leave the server's 5e9 meaning "5e9 requests".
    const payload = buildKeyPayload(
      { rpd_metric: 'requests', rpd_limit: 5000 },
      { editing: true, isTouched: edited('rpd_metric'), statusTouched: false },
    );
    expect(payload.rpd_metric).toBe('requests');
    expect(payload.rpd_limit).toBe(5000);
  });

  it('editing only the limit of a cost window still converts that limit', () => {
    const payload = buildKeyPayload(
      { rpd_metric: 'cost', rpd_limit: 50 },
      { editing: true, isTouched: edited('rpd_limit'), statusTouched: false },
    );
    expect(payload.rpd_limit).toBe(50_000_000);
  });

  it('name-only edit still sends nothing but the touched fields (limits untouched by the fix)', () => {
    // The fix must not widen payloads for edits outside its target: when no
    // metric is being sent, no limit is force-sent either.
    const payload = buildKeyPayload(
      { name: 'renamed', rpd_metric: 'cost', rpd_limit: 5000, provider_id: 7 },
      { editing: true, isTouched: edited('name'), statusTouched: false },
    );
    expect(payload).toEqual({ name: 'renamed' });
  });

  it('does not ship a fractional limit into a non-cost metric (would 400 the int64 bind)', () => {
    // A cost window stores micro-USD; openEditKey pre-fills its limit as
    // stored/1e6, so a sub-dollar cost window ("$0.005" — exactly what the
    // bug's mangled "5000" keys look like, or a legit "$12.50" budget) opens
    // showing a fractional number. Flipping ONLY its metric to requests leaves
    // the limit out of the payload (untouched, non-integer): the backend int64
    // bind rejects non-integers, so it must not be shipped.
    const payload = buildKeyPayload(
      { rpd_metric: 'requests', rpd_limit: 0.005 },
      { editing: true, isTouched: edited('rpd_metric'), statusTouched: false },
    );
    expect(payload.rpd_metric).toBe('requests');
    expect(payload.rpd_limit).toBeUndefined();
  });

  it('deletes a fractional limit from a full-resend payload when its metric is sent', () => {
    // After the first modal open, antd v6 setFieldsValue marks every prefilled
    // field touched, so on later edits the untouched-filter is inert and the
    // WHOLE config is re-sent — including a cost window's /1e6 USD prefill
    // (12.5). Flipping the metric to requests must DELETE 12.5 from the
    // payload, not ship it and 400 the entire save.
    const payload = buildKeyPayload(
      { name: 'k', rpd_metric: 'requests', rpd_limit: 12.5 },
      { editing: true, isTouched: allTouched, statusTouched: true },
    );
    expect(payload.rpd_metric).toBe('requests');
    expect(payload.rpd_limit).toBeUndefined();
    expect(payload.name).toBe('k');
  });

  it('keeps a whole-number limit in a full-resend metric flip (correct unit, no drop)', () => {
    // Same inert-filter path, but an INTEGER raw value is a valid count in the
    // new unit — it must be re-sent, not dropped.
    const payload = buildKeyPayload(
      { rpd_metric: 'requests', rpd_limit: 5000 },
      { editing: true, isTouched: allTouched, statusTouched: true },
    );
    expect(payload.rpd_metric).toBe('requests');
    expect(payload.rpd_limit).toBe(5000);
  });

  it('edit of only the lifetime budget converts and sends it', () => {
    const payload = buildKeyPayload(
      { name: 'k', total_spend_limit: 30 },
      { editing: true, isTouched: edited('total_spend_limit'), statusTouched: false },
    );
    expect(payload.total_spend_limit).toBe(30_000_000);
    expect(payload.name).toBeUndefined();
  });

  it('metric-only edit force-sends only the touched window\u2019s limit, not untouched windows\u2019', () => {
    // Fiddling rpd_metric must not drag rp5h/rpw limits (their metrics are
    // not in the payload) into the update.
    const payload = buildKeyPayload(
      { rpd_metric: 'cost', rpd_limit: 5000, rp5h_limit: 999999, rp5h_metric: 'requests', rpw_limit: 888, rpw_metric: 'tokens' },
      { editing: true, isTouched: edited('rpd_metric'), statusTouched: false },
    );
    expect(payload.rpd_metric).toBe('cost');
    expect(payload.rpd_limit).toBe(5_000_000_000);
    expect(payload.rp5h_limit).toBeUndefined();
    expect(payload.rpw_limit).toBeUndefined();
  });

  it('sends status only when the user actually touched it (or the ref says so)', () => {
    const base = { name: 'k', status: 'disabled', provider_id: 7 };
    const untouchedStatus = buildKeyPayload(
      base, { editing: true, isTouched: untouched, statusTouched: false });
    expect(untouchedStatus.status).toBeUndefined();
    const touchedStatus = buildKeyPayload(
      base, { editing: true, isTouched: edited('status'), statusTouched: true });
    expect(touchedStatus.status).toBe('disabled');
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
