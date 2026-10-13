import { renderToStaticMarkup } from 'react-dom/server';
import type { TFunction } from 'i18next';
import { describe, expect, it } from 'vitest';
import { RequestPromptView, type RequestPromptViewState } from './RequestPromptView';

const t = ((key: string) => key) as unknown as TFunction;
const meta = { time: '14/10/2026 01:00', apiKey: 'Cảnh', model: 'claude-opus', tokens: '1.2K' };

const render = (state: RequestPromptViewState) =>
  renderToStaticMarkup(
    <RequestPromptView t={t} state={state} meta={meta} onRetry={() => {}} onCopy={() => {}} />
  );

describe('RequestPromptView', () => {
  it('shows the question as plain text with meta', () => {
    const markup = render({
      kind: 'loaded',
      response: { request_id: 'abc', status: 'ok', question: '<b>ai hỏi</b>' },
    });
    expect(markup).toContain('&lt;b&gt;ai hỏi&lt;/b&gt;');
    expect(markup).toContain('Cảnh');
    expect(markup).toContain('claude-opus');
    expect(markup).toContain('monitoring.request_prompt_copy');
  });

  it.each([
    ['pending', 'monitoring.request_prompt_pending'],
    ['unavailable', 'monitoring.request_prompt_unavailable'],
    ['unparsable', 'monitoring.request_prompt_unparsable'],
    ['not_found', 'monitoring.request_prompt_not_found'],
  ] as const)('renders %s status message', (status, key) => {
    const markup = render({ kind: 'loaded', response: { request_id: 'abc', status } });
    expect(markup).toContain(key);
    expect(markup).not.toContain('monitoring.request_prompt_copy');
  });

  it('renders loading and error states', () => {
    expect(render({ kind: 'loading' })).toContain('monitoring.request_prompt_loading');
    const error = render({ kind: 'error', message: 'boom' });
    expect(error).toContain('boom');
    expect(error).toContain('monitoring.request_prompt_retry');
  });
});
