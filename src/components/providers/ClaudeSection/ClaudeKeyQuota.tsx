import { useTranslation } from 'react-i18next';
import { CLAUDE_CONFIG } from '@/components/quota/quotaConfigs';
import { QuotaProgressBar } from '@/components/quota/QuotaCard';
import type { ClaudeQuotaState } from '@/types';
import quotaStyles from '@/pages/QuotaPage.module.scss';
import styles from '@/pages/AiProvidersPage.module.scss';

/** Compact quota block shown inside a Claude key card once it has been checked. */
export function ClaudeKeyQuotaPanel({ quota }: { quota: ClaudeQuotaState | undefined }) {
  const { t } = useTranslation();
  if (!quota || quota.status === 'idle') return null;

  return (
    <div className={`${quotaStyles.quotaSection} ${styles.keyQuotaPanel}`}>
      {quota.status === 'loading' && (
        <div className={quotaStyles.quotaMessage}>{t('claude_quota.loading')}</div>
      )}
      {quota.status === 'error' && (
        <div className={quotaStyles.quotaError}>
          {t('claude_quota.load_failed', { message: quota.error })}
        </div>
      )}
      {quota.status === 'success' &&
        CLAUDE_CONFIG.renderQuotaItems(quota, t, { styles: quotaStyles, QuotaProgressBar })}
    </div>
  );
}
