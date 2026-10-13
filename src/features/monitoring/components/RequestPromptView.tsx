import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import type { RequestPromptResponse } from '@/services/api/usageService';
import styles from '@/pages/MonitoringCenterPage.module.scss';

export type RequestPromptViewState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; response: RequestPromptResponse };

type RequestPromptViewProps = {
  t: TFunction;
  state: RequestPromptViewState;
  meta: { time: string; apiKey: string; model: string; tokens: string };
  onRetry: () => void;
  onCopy: (text: string) => void;
};

const STATUS_MESSAGE_KEYS: Record<string, string> = {
  pending: 'monitoring.request_prompt_pending',
  unavailable: 'monitoring.request_prompt_unavailable',
  unparsable: 'monitoring.request_prompt_unparsable',
  not_found: 'monitoring.request_prompt_not_found',
};

export function RequestPromptView({ t, state, meta, onRetry, onCopy }: RequestPromptViewProps) {
  const question =
    state.kind === 'loaded' && state.response.status === 'ok' ? state.response.question || '' : '';

  let body;
  if (state.kind === 'loading') {
    body = <p>{t('monitoring.request_prompt_loading')}</p>;
  } else if (state.kind === 'error') {
    body = (
      <div>
        <p>{state.message}</p>
        <Button variant="secondary" size="sm" onClick={onRetry}>
          {t('monitoring.request_prompt_retry')}
        </Button>
      </div>
    );
  } else if (question) {
    body = (
      <div>
        <pre className={styles.requestPromptText}>{question}</pre>
        <Button variant="secondary" size="sm" onClick={() => onCopy(question)}>
          {t('monitoring.request_prompt_copy')}
        </Button>
      </div>
    );
  } else {
    const messageKey =
      STATUS_MESSAGE_KEYS[state.response.status] ?? 'monitoring.request_prompt_unparsable';
    body = (
      <div>
        <p>{t(messageKey)}</p>
        {state.response.status === 'pending' ? (
          <Button variant="secondary" size="sm" onClick={onRetry}>
            {t('monitoring.request_prompt_retry')}
          </Button>
        ) : null}
      </div>
    );
  }

  return (
    <div>
      <div className={styles.primaryCell}>
        <span>{`${meta.apiKey} · ${meta.model}`}</span>
        <small>{`${meta.time} · ${meta.tokens} tokens`}</small>
      </div>
      {body}
    </div>
  );
}
