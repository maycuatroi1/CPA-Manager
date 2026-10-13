import { describe, expect, it } from 'vitest';
import { collectUsageDetailsWithEndpoint } from './usage';

describe('collectUsageDetailsWithEndpoint request_id', () => {
  it('keeps the request id of each detail', () => {
    const details = collectUsageDetailsWithEndpoint({
      apis: {
        'POST /v1/messages': {
          models: {
            'claude-test': {
              details: [
                {
                  timestamp: '2026-10-14T01:00:00Z',
                  source: 's',
                  request_id: 'abc123',
                  tokens: {},
                  failed: false,
                },
                { timestamp: '2026-10-14T01:01:00Z', source: 's', tokens: {}, failed: false },
              ],
            },
          },
        },
      },
    });

    expect(details.map((detail) => detail.request_id)).toEqual(['abc123', undefined]);
  });
});
