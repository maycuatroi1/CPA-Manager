import { describe, expect, it } from 'vitest';
import { buildUsageFetchRange } from './useMonitoringData';

const NOW = new Date(2026, 9, 13, 15, 30, 0).getTime();
const TODAY_START = new Date(2026, 9, 13, 0, 0, 0).getTime();
const DAY_MS = 24 * 60 * 60 * 1000;

describe('buildUsageFetchRange', () => {
  it('only bounds the start for preset ranges so refreshes keep picking up new events', () => {
    expect(buildUsageFetchRange('today', NOW)).toEqual({ startMs: TODAY_START });
    expect(buildUsageFetchRange('7d', NOW)).toEqual({ startMs: TODAY_START - 6 * DAY_MS });
    expect(buildUsageFetchRange('30d', NOW)).toEqual({ startMs: TODAY_START - 29 * DAY_MS });
  });

  it('requests everything for the all range', () => {
    expect(buildUsageFetchRange('all', NOW)).toEqual({});
  });

  it('bounds both sides for a valid custom range', () => {
    expect(
      buildUsageFetchRange('custom', NOW, { startMs: TODAY_START - DAY_MS, endMs: TODAY_START })
    ).toEqual({ startMs: TODAY_START - DAY_MS, endMs: TODAY_START });
  });

  it('returns null for an incomplete custom range so no request is sent', () => {
    expect(buildUsageFetchRange('custom', NOW, null)).toBeNull();
  });
});
