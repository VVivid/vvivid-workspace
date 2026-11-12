'use client';
import { FormEvent, useRef, useState } from 'react';
import { APIError, Capture, Session, Word, api, message, write } from '../lib/api';
import Dialog from './dialog';

const emptyCapture: Capture = {
  term: '',
  original_sentence: '',
  source_url: '',
  personal_note: '',
};
function captureFields(word: Word): Capture {
  return {
    term: word.term,
    original_sentence: word.original_sentence,
    source_url: word.source_url,
    personal_note: word.personal_note,
  };
}
export default function Editor({
  word,
  ownerID,
  session,
  checkingSession,
  sessionError,
  checkSession,
  handleUnauthorized,
  close,
  saved,
}: {
  word?: Word;
  ownerID: string;
  session: Session | null;
  checkingSession: boolean;
  sessionError: string;
  checkSession: () => void;
  handleUnauthorized: (failure: unknown) => void;
  close: () => void;
  saved: (word: Word) => void;
}) {
  const [fields, setFields] = useState<Capture>(word ? captureFields(word) : emptyCapture);
  const [expectedRevision, setExpectedRevision] = useState(word?.revision);
  const [error, setError] = useState('');
  const [hasRevisionConflict, setHasRevisionConflict] = useState(false);
  const [latestSavedWord, setLatestSavedWord] = useState<Word | null>(null);
  const [saving, setSaving] = useState(false);
  const [reviewing, setReviewing] = useState(false);
  const saveIntent = useRef<{ fingerprint: string; key: string } | null>(null);
  const sameOwner = session?.owner_id === ownerID;
  const canSave = sameOwner && session.scopes.includes('vocabbank:write') && !checkingSession;
  const change = (key: keyof Capture, value: string) =>
    setFields((previous) => ({ ...previous, [key]: value }));
  async function save(event: FormEvent) {
    event.preventDefault();
    if (saving || hasRevisionConflict || !canSave) return;
    setSaving(true);
    setError('');
    const intent = word ? { ...fields, id: word.id, expected_revision: expectedRevision } : fields;
    const fingerprint = JSON.stringify(intent);
    // A failed response may follow a successful commit. Keep its retry key
    // until the input changes, including the expected revision.
    if (saveIntent.current?.fingerprint !== fingerprint)
      saveIntent.current = { fingerprint, key: crypto.randomUUID() };
    try {
      const result = await write<Word>(
        word ? `/api/v1/words/${word.id}` : '/api/v1/words',
        word ? 'PUT' : 'POST',
        { ...intent, idempotency_key: saveIntent.current!.key },
        session.csrf_token,
      );
      saved(result);
    } catch (failure) {
      setError(message(failure));
      setHasRevisionConflict(failure instanceof APIError && failure.code === 'stale_revision');
      handleUnauthorized(failure);
    } finally {
      setSaving(false);
    }
  }
  async function loadLatestForReview() {
    if (!word || !sameOwner || checkingSession) return;
    setReviewing(true);
    try {
      setLatestSavedWord(await api<Word>(`/api/v1/words/${word.id}`));
    } catch (failure) {
      setError(message(failure));
      handleUnauthorized(failure);
    } finally {
      setReviewing(false);
    }
  }
  return (
    <Dialog
      id="vg-add-dialog"
      titleID="vg-add-title"
      close={() => {
        if (!saving) close();
      }}
    >
      <form onSubmit={save}>
        <div className="vg-modal-head">
          <h2 id="vg-add-title">{word ? 'Edit word' : 'Add a word'}</h2>
          <button
            className="vg-close"
            type="button"
            onClick={close}
            disabled={saving}
            aria-label={word ? 'Close edit word' : 'Close add word'}
          >
            ×
          </button>
        </div>
        <div className="vg-modal-body vg-form">
          <p className="vg-language">
            {word
              ? 'Edit your saved word and capture. Changing the term clears its learning details.'
              : 'Save the word now. Your agent can add its details later.'}
          </p>
          {(!sameOwner || checkingSession) && (
            <section className="vg-error-box" aria-label="Sign-in recovery" role="alert">
              <h3>
                {checkingSession
                  ? 'Checking sign-in…'
                  : session
                    ? 'Different account signed in'
                    : 'Sign in again to save'}
              </h3>
              <p>
                {session && !sameOwner
                  ? 'This draft belongs to the account that opened it. Sign in with that account to save it.'
                  : 'Your unsaved text is still here. Sign in in a new tab, then return here to continue.'}
              </p>
              <p>Keep this tab open. You can copy your text or cancel to discard it.</p>
              {sessionError && <p>{sessionError}</p>}
              <div className="vg-recovery-actions">
                <a
                  className="vg-secondary"
                  href="/auth/login?reauthenticate=1"
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  Sign in in a new tab
                </a>
                <button
                  className="vg-secondary"
                  type="button"
                  onClick={checkSession}
                  disabled={checkingSession}
                >
                  Check sign-in
                </button>
              </div>
            </section>
          )}
          {sameOwner && !session.scopes.includes('vocabbank:write') && (
            <p className="vg-error-box" role="alert">
              This account does not currently have permission to save. Your text is still here.
            </p>
          )}
          <label htmlFor="vg-new-word">
            Word or expression
            <input
              id="vg-new-word"
              required
              maxLength={120}
              autoComplete="off"
              autoFocus
              value={fields.term}
              onChange={(event) => change('term', event.target.value)}
              placeholder="e.g. in hindsight"
              disabled={saving}
            />
          </label>
          <label htmlFor="vg-new-sentence">
            Sentence <span>(optional)</span>
            <textarea
              id="vg-new-sentence"
              maxLength={4000}
              rows={2}
              value={fields.original_sentence}
              onChange={(event) => change('original_sentence', event.target.value)}
              placeholder="Where you saw or heard it…"
              disabled={saving}
            />
          </label>
          <label htmlFor="vg-new-source">
            Source URL <span>(optional)</span>
            <input
              id="vg-new-source"
              type="url"
              maxLength={2048}
              value={fields.source_url}
              onChange={(event) => change('source_url', event.target.value)}
              placeholder="https://example.com/article"
              disabled={saving}
            />
          </label>
          <label htmlFor="vg-new-note">
            Personal note <span>(optional)</span>
            <textarea
              id="vg-new-note"
              maxLength={4000}
              rows={2}
              value={fields.personal_note}
              onChange={(event) => change('personal_note', event.target.value)}
              disabled={saving}
            />
          </label>
          {error && (
            <div className="vg-error-box" role="alert">
              <p className="vg-error">{error}</p>
              {hasRevisionConflict && (
                <button
                  type="button"
                  className="vg-secondary"
                  onClick={loadLatestForReview}
                  disabled={reviewing || !sameOwner || checkingSession}
                >
                  {reviewing ? 'Loading…' : 'Review latest version'}
                </button>
              )}
            </div>
          )}
          {latestSavedWord && (
            <section className="vg-error-box" aria-label="Latest saved fields">
              <h3>Latest saved version</h3>
              <p className="vg-preserve">{latestSavedWord.term}</p>
              <p className="vg-preserve">
                Sentence: {latestSavedWord.original_sentence || '(empty)'}
              </p>
              <p className="vg-preserve">Source: {latestSavedWord.source_url || '(empty)'}</p>
              <p className="vg-preserve">Note: {latestSavedWord.personal_note || '(empty)'}</p>
              <p className="vg-help">
                Your input is still in the form above. Copy anything you want to keep before loading
                these fields.
              </p>
              <button
                type="button"
                className="vg-secondary"
                onClick={() => {
                  setFields(captureFields(latestSavedWord));
                  setExpectedRevision(latestSavedWord.revision);
                  setLatestSavedWord(null);
                  setHasRevisionConflict(false);
                  setError('');
                  saveIntent.current = null;
                }}
              >
                Use latest fields
              </button>
            </section>
          )}
        </div>
        <div className="vg-modal-foot">
          <button type="button" className="vg-secondary" onClick={close} disabled={saving}>
            Cancel
          </button>
          <button
            className="vg-action"
            type="submit"
            disabled={saving || hasRevisionConflict || !canSave}
          >
            {saving ? 'Saving…' : error ? 'Retry save' : word ? 'Save changes' : 'Save word'}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
