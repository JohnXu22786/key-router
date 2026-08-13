// USD <-> micro-USD conversions for key limit fields.
//
// The UI enters cost-metric window limits and the lifetime budget in USD;
// the backend stores them in micro-USD (1e6 per $1) so cost math stays in
// integers. These round-trips must stay in lockstep: a units mismatch on the
// budget once stored "$30" as 30 micro-USD ($0.00003) and the key was
// disabled (spend_limit_exhausted) the moment it served its next request.
export const usdToMicroUsd = (v: number): number => Math.round(v * 1e6);
export const microUsdToUsd = (v: number): number => v / 1e6;
