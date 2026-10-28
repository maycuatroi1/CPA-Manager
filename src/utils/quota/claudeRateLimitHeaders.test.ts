import { describe, expect, it } from 'vitest';
import { isClaudeOAuthToken, parseClaudeRateLimitWindows } from './claudeRateLimitHeaders';

describe('parseClaudeRateLimitWindows', () => {
  it('reads 5h/7d utilization as percent, case-insensitively, 5h first', () => {
    const windows = parseClaudeRateLimitWindows({
      'Anthropic-Ratelimit-Unified-7d-Utilization': ['0.53'],
      'Anthropic-Ratelimit-Unified-7d-Reset': ['1787695200'],
      'anthropic-ratelimit-unified-5h-utilization': ['0.0'],
      'Anthropic-Ratelimit-Unified-5h-Reset': ['1787296800'],
      'Anthropic-Ratelimit-Unified-Status': ['allowed'],
      'Content-Type': ['application/json'],
    });

    expect(windows.map((w) => w.id)).toEqual(['unified-5h', 'unified-7d']);
    expect(windows[0]).toMatchObject({ labelKey: 'claude_quota.five_hour', usedPercent: 0 });
    expect(windows[1].usedPercent).toBeCloseTo(53);
    expect(windows[1].resetLabel).not.toBe('-');
  });

  it('keeps extra windows such as 7d_oi with their raw id as label', () => {
    const windows = parseClaudeRateLimitWindows({
      'Anthropic-Ratelimit-Unified-7d_oi-Utilization': ['1.02'],
    });
    expect(windows).toHaveLength(1);
    expect(windows[0]).toMatchObject({ label: '7d_oi', labelKey: undefined, resetLabel: '-' });
    expect(windows[0].usedPercent).toBeCloseTo(102);
  });

  it('returns nothing without unified headers', () => {
    expect(parseClaudeRateLimitWindows({ 'Retry-After': ['30'] })).toEqual([]);
    expect(parseClaudeRateLimitWindows(undefined)).toEqual([]);
  });
});

describe('isClaudeOAuthToken', () => {
  it('detects subscription OAuth tokens only', () => {
    expect(isClaudeOAuthToken('sk-ant-oat01-abc')).toBe(true);
    expect(isClaudeOAuthToken('sk-ant-api03-abc')).toBe(false);
    expect(isClaudeOAuthToken(undefined)).toBe(false);
  });
});
