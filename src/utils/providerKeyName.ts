import type { ProviderKeyNameProvider } from '@/services/api/usageService';
import { sha256Hex } from './apiKeyHash';

/**
 * Hash used to key a provider credential's display name in Usage Service.
 * The provider is part of the input so the same secret reused across two
 * providers can carry two different names; the raw key never leaves the panel.
 */
export const providerKeyNameHash = (provider: ProviderKeyNameProvider, apiKey: string): string => {
  const trimmed = String(apiKey || '').trim();
  if (!trimmed) return '';
  return sha256Hex(`${provider}:${trimmed}`).toLowerCase();
};
