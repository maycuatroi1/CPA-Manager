import { create } from 'zustand';
import {
  isUsageServiceId,
  normalizeUsageServiceBase,
  usageServiceApi,
  type ProviderKeyName,
  type ProviderKeyNameProvider,
} from '@/services/api/usageService';
import { detectApiBaseFromLocation } from '@/utils/connection';
import { providerKeyNameHash } from '@/utils/providerKeyName';
import { useAuthStore } from './useAuthStore';
import { useUsageServiceStore } from './useUsageServiceStore';

type Status = 'idle' | 'loading' | 'ready' | 'unavailable';

interface ProviderKeyNamesState {
  names: Record<string, string>;
  status: Status;
  serviceBase: string;
  loadedFor: string;
  load: () => Promise<void>;
  saveName: (
    provider: ProviderKeyNameProvider,
    apiKey: string,
    name: string,
    previousApiKey?: string
  ) => Promise<void>;
}

const connectionFingerprint = () => {
  const { apiBase, managementKey } = useAuthStore.getState();
  const { enabled, serviceBase, revision } = useUsageServiceStore.getState();
  return [apiBase, managementKey, enabled, serviceBase, revision].join('|');
};

// Same discovery order as the client API key aliases: an explicitly configured
// Usage Service first, then the panel origin when Usage Service hosts the panel.
const resolveServiceBase = async (): Promise<string> => {
  const { enabled, serviceBase } = useUsageServiceStore.getState();
  if (enabled && serviceBase) return serviceBase;

  const { apiBase } = useAuthStore.getState();
  const candidates = Array.from(
    new Set(
      [apiBase, detectApiBaseFromLocation()]
        .map((candidate) => normalizeUsageServiceBase(candidate || ''))
        .filter(Boolean)
    )
  );
  for (const candidate of candidates) {
    try {
      const info = await usageServiceApi.getInfo(candidate);
      if (isUsageServiceId(info.service)) return candidate;
    } catch {
      // Plain CPA does not expose Usage Service metadata.
    }
  }
  return '';
};

const toNameMap = (items: ProviderKeyName[] | undefined) => {
  const map: Record<string, string> = {};
  (Array.isArray(items) ? items : []).forEach((item) => {
    const hash = String(item.keyHash || '')
      .trim()
      .toLowerCase();
    const name = String(item.name || '').trim();
    if (hash && name) map[hash] = name;
  });
  return map;
};

let inflight: Promise<void> | null = null;

export const useProviderKeyNamesStore = create<ProviderKeyNamesState>()((set, get) => ({
  names: {},
  status: 'idle',
  serviceBase: '',
  loadedFor: '',

  load: async () => {
    const fingerprint = connectionFingerprint();
    const state = get();
    if (state.loadedFor === fingerprint && state.status !== 'idle') return;
    if (inflight) return inflight;

    set({ status: 'loading', loadedFor: fingerprint });
    inflight = (async () => {
      try {
        const serviceBase = await resolveServiceBase();
        if (!serviceBase) {
          set({ status: 'unavailable', serviceBase: '', names: {} });
          return;
        }
        const { managementKey } = useAuthStore.getState();
        const response = await usageServiceApi.getProviderKeyNames(serviceBase, managementKey);
        set({ status: 'ready', serviceBase, names: toNameMap(response.items) });
      } catch {
        // Older Usage Service builds return 404 for this endpoint.
        set({ status: 'unavailable', serviceBase: '', names: {} });
      } finally {
        inflight = null;
      }
    })();
    return inflight;
  },

  saveName: async (provider, apiKey, name, previousApiKey) => {
    const { status, serviceBase, names } = get();
    if (status !== 'ready' || !serviceBase) {
      throw new Error('Usage Service is not available');
    }
    const { managementKey } = useAuthStore.getState();
    const nextHash = providerKeyNameHash(provider, apiKey);
    const previousHash = previousApiKey ? providerKeyNameHash(provider, previousApiKey) : nextHash;
    const trimmed = name.trim();
    const nextNames = { ...names };

    if (previousHash && previousHash !== nextHash && nextNames[previousHash]) {
      await usageServiceApi.deleteProviderKeyName(serviceBase, previousHash, managementKey);
      delete nextNames[previousHash];
    }
    if (nextHash && trimmed) {
      await usageServiceApi.saveProviderKeyNames(
        serviceBase,
        [{ keyHash: nextHash, provider, name: trimmed }],
        managementKey
      );
      nextNames[nextHash] = trimmed;
    } else if (nextHash && nextNames[nextHash]) {
      await usageServiceApi.deleteProviderKeyName(serviceBase, nextHash, managementKey);
      delete nextNames[nextHash];
    }
    set({ names: nextNames });
  },
}));
