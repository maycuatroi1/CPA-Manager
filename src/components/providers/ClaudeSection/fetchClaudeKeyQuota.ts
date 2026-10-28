import type { TFunction } from 'i18next';
import { apiCallApi, getApiCallErrorMessage, providersApi } from '@/services/api';
import type { ClaudeQuotaWindow, ProviderKeyConfig } from '@/types';
import { parseClaudeRateLimitWindows } from '@/utils/quota/claudeRateLimitHeaders';

const DEFAULT_BASE_URL = 'https://api.anthropic.com';
// Cheapest model; the probe asks for a single output token.
const PROBE_MODEL = 'claude-haiku-5-5';

// The AI Providers list is built from /config, which carries no auth-index;
// only /claude-api-key does, so look the entry up there when it is missing.
const resolveAuthIndex = async (config: ProviderKeyConfig, t: TFunction): Promise<string> => {
  if (config.authIndex) return config.authIndex;
  const apiKey = config.apiKey.trim();
  const baseUrl = (config.baseUrl ?? '').trim();
  const entries = await providersApi.getClaudeConfigs();
  const match = entries.find(
    (entry) => entry.apiKey.trim() === apiKey && (entry.baseUrl ?? '').trim() === baseUrl
  );
  if (!match?.authIndex) throw new Error(t('claude_quota.missing_auth_index'));
  return match.authIndex;
};

/**
 * Remaining subscription quota for a claude-api-key entry holding a setup-token.
 * /api/oauth/usage rejects these tokens (inference-only scope), so this sends a
 * one-token /v1/messages request through CPA's api-call and reads the
 * anthropic-ratelimit-unified-* response headers instead.
 */
export const fetchClaudeKeyQuota = async (
  config: ProviderKeyConfig,
  t: TFunction
): Promise<ClaudeQuotaWindow[]> => {
  const authIndex = await resolveAuthIndex(config, t);
  const baseUrl = (config.baseUrl || DEFAULT_BASE_URL).replace(/\/+$/, '');
  const result = await apiCallApi.request({
    authIndex,
    method: 'POST',
    url: `${baseUrl}/v1/messages`,
    header: {
      Authorization: 'Bearer $TOKEN$',
      'Content-Type': 'application/json',
      'anthropic-version': '2023-06-01',
      'anthropic-beta': 'oauth-2025-04-20',
    },
    data: JSON.stringify({
      model: PROBE_MODEL,
      max_tokens: 1,
      system: "You are Claude Code, Anthropic's official CLI for Claude.",
      messages: [{ role: 'user', content: 'quota' }],
    }),
  });
  // Rate-limit headers are present on 429 too, so read them before judging the status.
  const windows = parseClaudeRateLimitWindows(result.header);
  if (windows.length === 0) {
    throw new Error(
      result.statusCode >= 200 && result.statusCode < 300
        ? t('claude_quota.empty_windows')
        : getApiCallErrorMessage(result)
    );
  }
  return windows;
};
