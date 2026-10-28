import { useCallback, useEffect, useMemo, useState } from 'react';
import type { ProviderKeyNameProvider } from '@/services/api/usageService';
import { useAuthStore } from '@/stores/useAuthStore';
import { useProviderKeyNamesStore } from '@/stores/useProviderKeyNamesStore';
import { useUsageServiceStore } from '@/stores/useUsageServiceStore';
import { providerKeyNameHash } from '@/utils/providerKeyName';

/** Loads provider key display names and exposes a lookup for list views. */
export function useProviderKeyNames() {
  const names = useProviderKeyNamesStore((state) => state.names);
  const status = useProviderKeyNamesStore((state) => state.status);
  const load = useProviderKeyNamesStore((state) => state.load);
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const usageRevision = useUsageServiceStore((state) => state.revision);

  useEffect(() => {
    void load();
  }, [apiBase, load, managementKey, usageRevision]);

  const getName = useCallback(
    (provider: ProviderKeyNameProvider, apiKey: string) => {
      const hash = providerKeyNameHash(provider, apiKey);
      return hash ? (names[hash] ?? '') : '';
    },
    [names]
  );

  return {
    getName,
    available: status === 'ready',
    loading: status === 'loading' || status === 'idle',
  };
}

export type ProviderKeyNameDraft = ReturnType<typeof useProviderKeyNameDraft>;

/**
 * Editable display name for one provider key in an add/edit form. The name is
 * stored outside CPA config, so it is committed separately after the config save.
 */
export function useProviderKeyNameDraft(provider: ProviderKeyNameProvider, originalApiKey: string) {
  const { getName, available, loading } = useProviderKeyNames();
  const saveName = useProviderKeyNamesStore((state) => state.saveName);
  const baseline = originalApiKey ? getName(provider, originalApiKey) : '';
  // null = untouched: names arrive asynchronously, so follow the stored value until the user types.
  const [edited, setEdited] = useState<string | null>(null);
  const value = edited ?? baseline;

  const setValue = useCallback((next: string) => setEdited(next), []);

  const isDirty = available && value.trim() !== baseline;

  const commit = useCallback(
    async (savedApiKey: string) => {
      if (!available) return;
      const keyChanged = Boolean(originalApiKey) && originalApiKey.trim() !== savedApiKey.trim();
      if (!keyChanged && value.trim() === baseline) return;
      await saveName(provider, savedApiKey, value, originalApiKey || undefined);
      setEdited(null);
    },
    [available, baseline, originalApiKey, provider, saveName, value]
  );

  return useMemo(
    () => ({ value, setValue, available, loading, isDirty, commit }),
    [available, commit, isDirty, loading, setValue, value]
  );
}
