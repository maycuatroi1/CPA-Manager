import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { CLAUDE_CONFIG } from '@/components/quota/quotaConfigs';
import { QuotaProgressBar } from '@/components/quota/QuotaCard';
import { apiCallApi, getApiCallErrorMessage, providersApi } from '@/services/api';
import type { ClaudeQuotaState, ProviderKeyConfig } from '@/types';
import { parseClaudeRateLimitWindows } from '@/utils/quota/claudeRateLimitHeaders';
import quotaStyles from '@/pages/QuotaPage.module.scss';

const DEFAULT_BASE_URL = 'https://api.anthropic.com';
// Cheapest model; the probe asks for a single output token.
const PROBE_MODEL = 'claude-haiku-5-5';

/**
 * Remaining subscription quota for a claude-api-key entry holding a setup-token.
 * /api/oauth/usage rejects these tokens (inference-only scope), so this sends a
 * one-token /v1/messages request through CPA's api-call and reads the
 * anthropic-ratelimit-unified-* response headers instead.
 */
export function ClaudeKeyQuota({ config }: { config: ProviderKeyConfig }) {
  const { t } = useTranslation();
  const [quota, setQuota] = useState<ClaudeQuotaState>({ status: 'idle', windows: [] });

  // The AI Providers list is built from /config, which carries no auth-index;
  // only /claude-api-key does, so look the entry up there when it is missing.
  const resolveAuthIndex = async (): Promise<string> => {
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

  const refresh = async () => {
    setQuota({ status: 'loading', windows: [] });
    try {
      const authIndex = await resolveAuthIndex();
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
      setQuota({ status: 'success', windows });
    } catch (err: unknown) {
      setQuota({
        status: 'error',
        windows: [],
        error: err instanceof Error ? err.message : String(err),
      });
    }
  };

  return (
    <div className={quotaStyles.quotaSection}>
      <Button
        size="sm"
        variant="secondary"
        onClick={() => void refresh()}
        loading={quota.status === 'loading'}
      >
        {t('claude_quota.refresh_button')}
      </Button>
      {quota.status === 'idle' && (
        <div className={quotaStyles.quotaMessage}>{t('claude_quota.setup_token_probe_hint')}</div>
      )}
      {quota.status === 'error' && (
        <div className={quotaStyles.quotaError}>
          {t('claude_quota.load_failed', { message: quota.error })}
        </div>
      )}
      {quota.status === 'success' &&
        CLAUDE_CONFIG.renderQuotaItems(quota, t, {
          styles: quotaStyles,
          QuotaProgressBar,
        })}
    </div>
  );
}
