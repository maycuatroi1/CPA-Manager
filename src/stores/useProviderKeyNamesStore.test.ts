import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  saveProviderKeyNames: vi.fn(async () => ({ items: [] })),
  deleteProviderKeyName: vi.fn(async () => undefined),
}));

vi.mock('@/services/api/usageService', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/services/api/usageService')>();
  return { ...actual, usageServiceApi: { ...actual.usageServiceApi, ...api } };
});

import { providerKeyNameHash } from '@/utils/providerKeyName';
import { useProviderKeyNamesStore } from './useProviderKeyNamesStore';

const BASE = 'http://usage.test';

describe('providerKeyNameHash', () => {
  it('scopes the hash by provider and ignores surrounding whitespace', () => {
    expect(providerKeyNameHash('claude', ' sk-1 ')).toBe(providerKeyNameHash('claude', 'sk-1'));
    expect(providerKeyNameHash('claude', 'sk-1')).not.toBe(providerKeyNameHash('codex', 'sk-1'));
    expect(providerKeyNameHash('claude', '   ')).toBe('');
  });
});

describe('useProviderKeyNamesStore.saveName', () => {
  beforeEach(() => {
    api.saveProviderKeyNames.mockClear();
    api.deleteProviderKeyName.mockClear();
    useProviderKeyNamesStore.setState({ names: {}, status: 'ready', serviceBase: BASE });
  });

  it('moves the name to the new hash when the key changes', async () => {
    const oldHash = providerKeyNameHash('claude', 'sk-old');
    const newHash = providerKeyNameHash('claude', 'sk-new');
    useProviderKeyNamesStore.setState({ names: { [oldHash]: 'Team' } });

    await useProviderKeyNamesStore.getState().saveName('claude', 'sk-new', 'Team', 'sk-old');

    expect(api.deleteProviderKeyName).toHaveBeenCalledWith(BASE, oldHash, expect.anything());
    expect(api.saveProviderKeyNames).toHaveBeenCalledWith(
      BASE,
      [{ keyHash: newHash, provider: 'claude', name: 'Team' }],
      expect.anything()
    );
    expect(useProviderKeyNamesStore.getState().names).toEqual({ [newHash]: 'Team' });
  });

  it('deletes the stored name when cleared', async () => {
    const hash = providerKeyNameHash('gemini', 'k');
    useProviderKeyNamesStore.setState({ names: { [hash]: 'Old' } });

    await useProviderKeyNamesStore.getState().saveName('gemini', 'k', '  ');

    expect(api.saveProviderKeyNames).not.toHaveBeenCalled();
    expect(api.deleteProviderKeyName).toHaveBeenCalledWith(BASE, hash, expect.anything());
    expect(useProviderKeyNamesStore.getState().names).toEqual({});
  });

  it('refuses to save when Usage Service is unavailable', async () => {
    useProviderKeyNamesStore.setState({ status: 'unavailable', serviceBase: '' });
    await expect(useProviderKeyNamesStore.getState().saveName('codex', 'k', 'x')).rejects.toThrow();
    expect(api.saveProviderKeyNames).not.toHaveBeenCalled();
  });
});
