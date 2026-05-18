import { useCallback, useEffect, useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  usageServiceApi,
  type ApiKeyLimit,
  type ApiKeyLimitCheckResponse,
  type ApiKeyLimitType,
  type ApiKeyLimitWindowDays,
} from '@/services/api/usageService';
import { useAuthStore, useUsageServiceStore } from '@/stores';
import { sha256Hex } from '@/utils/apiKeyHash';
import { maskApiKey } from '@/utils/format';
import styles from './ApiKeyLimitModal.module.scss';

interface ApiKeyLimitModalProps {
  open: boolean;
  apiKey: string;
  onClose: () => void;
}

const LIMIT_TYPE_OPTIONS = [
  { value: 'token', label: 'Token' },
  { value: 'cost', label: 'Cost (USD)' },
] as const;

const WINDOW_OPTIONS = [
  { value: '7', label: '7 days' },
  { value: '30', label: '30 days' },
] as const;

export function ApiKeyLimitModal({ open, apiKey, onClose }: ApiKeyLimitModalProps) {
  const { t } = useTranslation();
  const inputId = useId();

  const managementKey = useAuthStore((state) => state.managementKey);
  const usageServiceEnabled = useUsageServiceStore((state) => state.enabled);
  const usageServiceBase = useUsageServiceStore((state) => state.serviceBase);

  const [apiKeyHash, setApiKeyHash] = useState('');
  const [checkData, setCheckData] = useState<ApiKeyLimitCheckResponse | null>(null);
  const [loadingCheck, setLoadingCheck] = useState(false);

  const [limitType, setLimitType] = useState<ApiKeyLimitType>('token');
  const [limitValue, setLimitValue] = useState('');
  const [windowDays, setWindowDays] = useState<ApiKeyLimitWindowDays>(7);
  const [enabled, setEnabled] = useState(true);
  const [priority, setPriority] = useState(false);
  const [softLimit, setSoftLimit] = useState(false);

  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState('');
  const [success, setSuccess] = useState('');

  const resolveBase = useCallback((): string | null => {
    if (usageServiceEnabled && usageServiceBase) return usageServiceBase;
    return null;
  }, [usageServiceEnabled, usageServiceBase]);

  useEffect(() => {
    if (!open || !apiKey) return;
    let cancelled = false;

    const hash = sha256Hex(apiKey);
    setApiKeyHash(hash);

    void (async () => {
      const base = resolveBase();
      if (!base) return;

      setLoadingCheck(true);
      try {
        const data = await usageServiceApi.checkApiKeyLimit(base, hash, managementKey || undefined);
        if (cancelled) return;
        setCheckData(data);
        if (data.hasLimit) {
          setLimitType(data.limitType ?? 'token');
          setLimitValue(String(data.limitValue ?? ''));
          setWindowDays((data.windowDays ?? 7) as ApiKeyLimitWindowDays);
          setPriority(Boolean(data.priority));
          setSoftLimit(Boolean(data.softLimit));
        } else {
          setLimitType('token');
          setLimitValue('');
          setWindowDays(7);
          setEnabled(true);
          setPriority(false);
          setSoftLimit(false);
        }
      } catch {
        if (cancelled) return;
        setCheckData(null);
      } finally {
        if (!cancelled) setLoadingCheck(false);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [open, apiKey, managementKey, resolveBase]);

  const handleSave = async () => {
    const base = resolveBase();
    if (!base) {
      setError(t('ai_providers.limit_no_usage_service'));
      return;
    }
    const parsed = parseFloat(limitValue);
    if (!parsed || parsed <= 0) {
      setError(t('ai_providers.limit_value_required'));
      return;
    }
    setError('');
    setSuccess('');
    setSaving(true);
    try {
      const limit: ApiKeyLimit = {
        apiKeyHash,
        limitType,
        limitValue: parsed,
        windowDays,
        enabled,
        priority,
        softLimit,
      };
      await usageServiceApi.saveApiKeyLimit(base, limit, managementKey || undefined);
      const refreshed = await usageServiceApi.checkApiKeyLimit(base, apiKeyHash, managementKey || undefined);
      setCheckData(refreshed);
      setSuccess(t('ai_providers.limit_saved'));
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('ai_providers.limit_save_failed'));
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async () => {
    const base = resolveBase();
    if (!base) return;
    setError('');
    setSuccess('');
    setDeleting(true);
    try {
      await usageServiceApi.deleteApiKeyLimit(base, apiKeyHash, managementKey || undefined);
      setCheckData({ apiKeyHash, allowed: true, hasLimit: false });
      setLimitValue('');
      setSuccess(t('ai_providers.limit_deleted'));
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('ai_providers.limit_delete_failed'));
    } finally {
      setDeleting(false);
    }
  };

  const usedValue = checkData?.hasLimit
    ? limitType === 'cost'
      ? (checkData.usedCost ?? 0)
      : (checkData.usedTokens ?? 0)
    : 0;
  const limitVal = checkData?.hasLimit ? (checkData.limitValue ?? 0) : 0;
  const usedPercent = limitVal > 0 ? Math.min(100, Math.round((usedValue / limitVal) * 100)) : 0;
  const isOverLimit = checkData?.limitReached ?? false;

  const noUsageService = !usageServiceEnabled || !usageServiceBase;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('ai_providers.limit_modal_title')}
      width={480}
      footer={
        <div className={styles.footer}>
          {checkData?.hasLimit && (
            <Button variant="danger" size="sm" onClick={handleDelete} disabled={deleting || saving}>
              {deleting ? t('common.loading') : t('ai_providers.limit_remove')}
            </Button>
          )}
          <div className={styles.footerRight}>
            <Button variant="secondary" size="sm" onClick={onClose} disabled={saving || deleting}>
              {t('common.cancel')}
            </Button>
            <Button
              size="sm"
              onClick={handleSave}
              disabled={saving || deleting || noUsageService || loadingCheck}
            >
              {saving ? t('common.loading') : t('common.save')}
            </Button>
          </div>
        </div>
      }
    >
      <div className={styles.content}>
        <div className={styles.keyInfo}>
          <span className={styles.keyLabel}>{t('ai_providers.limit_key_label')}</span>
          <span className={styles.keyValue}>{maskApiKey(apiKey)}</span>
        </div>

        {noUsageService && (
          <div className={styles.warning}>{t('ai_providers.limit_no_usage_service')}</div>
        )}

        {checkData?.hasLimit && (
          <div className={styles.usageSection}>
            <div className={styles.usageRow}>
              <span className={styles.usageLabel}>{t('ai_providers.limit_usage_label', { window: windowDays })}</span>
              <span className={`${styles.usageValue} ${isOverLimit ? styles.overLimit : ''}`}>
                {limitType === 'cost'
                  ? `$${usedValue.toFixed(4)} / $${limitVal.toFixed(2)}`
                  : `${usedValue.toLocaleString()} / ${limitVal.toLocaleString()} tokens`}
              </span>
            </div>
            <div className={styles.progressBar}>
              <div
                className={`${styles.progressFill} ${isOverLimit ? styles.progressOverLimit : usedPercent > 80 ? styles.progressWarning : ''}`}
                style={{ width: `${usedPercent}%` }}
              />
            </div>
            {isOverLimit && (
              <div className={checkData?.softLimitOnly ? styles.softLimitBadge : styles.blockedBadge}>
                {checkData?.softLimitOnly
                  ? t('ai_providers.limit_soft_active')
                  : t('ai_providers.limit_blocked')}
              </div>
            )}
          </div>
        )}

        <div className={styles.formRow}>
          <label className={styles.formLabel}>{t('ai_providers.limit_type_label')}</label>
          <Select
            value={limitType}
            options={LIMIT_TYPE_OPTIONS as unknown as { value: string; label: string }[]}
            onChange={(v) => setLimitType(v as ApiKeyLimitType)}
            disabled={saving || deleting || noUsageService}
          />
        </div>

        <div className={styles.formRow}>
          <label className={styles.formLabel} htmlFor={`${inputId}-value`}>
            {limitType === 'cost'
              ? t('ai_providers.limit_value_cost_label')
              : t('ai_providers.limit_value_token_label')}
          </label>
          <input
            id={`${inputId}-value`}
            type="number"
            min="1"
            step={limitType === 'cost' ? '0.01' : '1000'}
            className={styles.input}
            value={limitValue}
            onChange={(e) => setLimitValue(e.target.value)}
            disabled={saving || deleting || noUsageService}
            placeholder={limitType === 'cost' ? '10.00' : '1000000'}
          />
        </div>

        <div className={styles.formRow}>
          <label className={styles.formLabel}>{t('ai_providers.limit_window_label')}</label>
          <Select
            value={String(windowDays)}
            options={WINDOW_OPTIONS as unknown as { value: string; label: string }[]}
            onChange={(v) => setWindowDays(Number(v) as ApiKeyLimitWindowDays)}
            disabled={saving || deleting || noUsageService}
          />
        </div>

        <div className={styles.formRow}>
          <label className={styles.formLabel}>{t('ai_providers.limit_enabled_label')}</label>
          <ToggleSwitch
            checked={enabled}
            onChange={setEnabled}
            disabled={saving || deleting || noUsageService}
          />
        </div>

        <div className={styles.formRow}>
          <label className={styles.formLabel}>
            {t('ai_providers.limit_priority_label')}
            <span className={styles.formLabelHint}>{t('ai_providers.limit_priority_hint')}</span>
          </label>
          <ToggleSwitch
            checked={priority}
            onChange={setPriority}
            disabled={saving || deleting || noUsageService}
          />
        </div>

        <div className={styles.formRow}>
          <label className={styles.formLabel}>
            {t('ai_providers.limit_soft_label')}
            <span className={styles.formLabelHint}>{t('ai_providers.limit_soft_hint')}</span>
          </label>
          <ToggleSwitch
            checked={softLimit}
            onChange={setSoftLimit}
            disabled={saving || deleting || noUsageService}
          />
        </div>

        {error && <div className={styles.error}>{error}</div>}
        {success && <div className={styles.successMsg}>{success}</div>}

        {loadingCheck && <div className={styles.hint}>{t('common.loading')}</div>}
      </div>
    </Modal>
  );
}
