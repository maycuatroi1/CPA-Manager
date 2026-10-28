import type { ClaudeQuotaWindow } from '@/types';
import { formatQuotaResetTime } from './formatters';

const PREFIX = 'anthropic-ratelimit-unified-';

const WINDOW_LABEL_KEYS: Record<string, string> = {
  '5h': 'claude_quota.five_hour',
  '7d': 'claude_quota.seven_day',
};

/** True for Claude subscription OAuth tokens (`claude setup-token` / OAuth access tokens). */
export const isClaudeOAuthToken = (apiKey: string | undefined): boolean =>
  String(apiKey ?? '').includes('sk-ant-oat');

const lowerCaseHeaders = (headers: Record<string, string[] | string> | null | undefined) => {
  const result: Record<string, string> = {};
  Object.entries(headers ?? {}).forEach(([key, value]) => {
    const last = Array.isArray(value) ? value[value.length - 1] : value;
    if (typeof last === 'string' && last.trim()) result[key.toLowerCase()] = last.trim();
  });
  return result;
};

/**
 * Builds quota windows from Anthropic unified rate-limit headers
 * (`anthropic-ratelimit-unified-<window>-utilization|reset`). Setup tokens only carry
 * `user:inference`, so /api/oauth/usage answers 403 for them; these headers on a
 * /v1/messages response are the only quota signal they can read.
 *
 * Utilization is a fraction (0.53 = 53%), reset is unix seconds.
 */
export const parseClaudeRateLimitWindows = (
  headers: Record<string, string[] | string> | null | undefined
): ClaudeQuotaWindow[] => {
  const map = lowerCaseHeaders(headers);
  const windowIds = new Set<string>();
  Object.keys(map).forEach((key) => {
    const match = key.startsWith(PREFIX) && key.slice(PREFIX.length).match(/^(.+)-utilization$/);
    if (match) windowIds.add(match[1]);
  });

  const order = (id: string) => (id === '5h' ? 0 : id === '7d' ? 1 : 2);
  return Array.from(windowIds)
    .sort((a, b) => order(a) - order(b) || a.localeCompare(b))
    .map((id) => {
      const utilization = Number.parseFloat(map[`${PREFIX}${id}-utilization`]);
      const resetSeconds = Number.parseInt(map[`${PREFIX}${id}-reset`] ?? '', 10);
      const resetLabel = Number.isFinite(resetSeconds)
        ? formatQuotaResetTime(new Date(resetSeconds * 1000).toISOString())
        : '-';
      return {
        id: `unified-${id}`,
        label: id,
        labelKey: WINDOW_LABEL_KEYS[id],
        usedPercent: Number.isFinite(utilization) ? utilization * 100 : null,
        resetLabel,
      };
    });
};
