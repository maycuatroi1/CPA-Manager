import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/Input';
import type { ProviderKeyNameDraft } from '@/hooks/useProviderKeyNames';

interface ProviderKeyNameInputProps {
  draft: ProviderKeyNameDraft;
  disabled?: boolean;
}

export function ProviderKeyNameInput({ draft, disabled }: ProviderKeyNameInputProps) {
  const { t } = useTranslation();
  const unavailable = !draft.loading && !draft.available;

  return (
    <Input
      label={t('ai_providers.key_name_label')}
      placeholder={t('ai_providers.key_name_placeholder')}
      hint={unavailable ? t('ai_providers.key_name_unavailable') : t('ai_providers.key_name_hint')}
      value={draft.value}
      maxLength={120}
      onChange={(e) => draft.setValue(e.target.value)}
      disabled={disabled || !draft.available}
    />
  );
}
