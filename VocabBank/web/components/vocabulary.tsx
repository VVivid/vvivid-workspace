'use client';
import { CSSProperties, useCallback, useEffect, useRef, useState } from 'react';
import { APIError, Session, Word, WordPage, api, message, write } from '../lib/api';
import Editor from './editor';
import Learning from './learning';

type Settings = { theme: 'light' | 'dark'; columns: 3 | 4; pageSize: 30 | 50 | 100 };
const defaults: Settings = { theme: 'light', columns: 3, pageSize: 30 };
const settingsKey = 'vocabbank-display';
export default function Vocabulary() {
  const [settings, setSettings] = useState(defaults);
  const [settingsReady, setSettingsReady] = useState(false);
  const [session, setSession] = useState<Session | null>(null);
  const sessionRef = useRef<Session | null>(null);
  const sessionRequestVersion = useRef(0);
  const [checking, setChecking] = useState(true);
  const [sessionError, setSessionError] = useState('');
  const [signingOut, setSigningOut] = useState(false);
  const [query, setQuery] = useState('');
  const [page, setPage] = useState(1);
  const [data, setData] = useState<WordPage | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [selected, setSelected] = useState<Word | null>(null);
  const selectedRef = useRef<Word | null>(null);
  const [detailError, setDetailError] = useState('');
  const [detailLoading, setDetailLoading] = useState(false);
  // Incrementing a version also invalidates responses still in flight after
  // a filter change, dialog close or loss of authentication.
  const detailRequestVersion = useRef(0);
  const [editor, setEditor] = useState<{ word?: Word; ownerID: string } | null>(null);
  const listRequestVersion = useRef(0);
  const canWrite = session?.scopes.includes('vocabbank:write') || false;
  useEffect(() => {
    try {
      const stored = JSON.parse(localStorage.getItem(settingsKey) || '{}');
      setSettings({
        theme: stored.theme === 'dark' ? 'dark' : 'light',
        columns: stored.columns === 4 ? 4 : 3,
        pageSize: [30, 50, 100].includes(stored.pageSize) ? stored.pageSize : 30,
      });
    } catch {
      /* Display preferences are optional. */
    }
    setSettingsReady(true);
  }, []);
  useEffect(() => {
    document.documentElement.dataset.theme = settings.theme;
    if (settingsReady) {
      try {
        localStorage.setItem(settingsKey, JSON.stringify(settings));
      } catch {
        /* Private browsing may deny storage. */
      }
    }
  }, [settings, settingsReady]);
  const updateSession = useCallback((next: Session | null) => {
    if (sessionRef.current?.owner_id !== next?.owner_id) {
      listRequestVersion.current++;
      detailRequestVersion.current++;
      setData(null);
      setSelected(null);
      selectedRef.current = null;
      setLoading(false);
      setDetailLoading(false);
    }
    sessionRef.current = next;
    setSession(next);
  }, []);
  const checkSession = useCallback(async () => {
    const requestVersion = ++sessionRequestVersion.current;
    listRequestVersion.current++;
    detailRequestVersion.current++;
    setChecking(true);
    setSessionError('');
    try {
      const next = await api<Session>('/api/v1/session');
      if (requestVersion === sessionRequestVersion.current) updateSession(next);
    } catch (failure) {
      if (requestVersion !== sessionRequestVersion.current) return;
      updateSession(null);
      if (!(failure instanceof APIError && failure.status === 401))
        setSessionError(message(failure));
    } finally {
      if (requestVersion === sessionRequestVersion.current) setChecking(false);
    }
  }, [updateSession]);
  useEffect(() => {
    void checkSession();
  }, [checkSession]);
  const handleUnauthorized = useCallback(
    (failure: unknown) => {
      if (failure instanceof APIError && failure.status === 401) {
        sessionRequestVersion.current++;
        updateSession(null);
        setChecking(false);
        setSessionError('');
      }
    },
    [updateSession],
  );
  const loadWords = useCallback(async () => {
    if (!session || checking) return;
    const requestVersion = ++listRequestVersion.current;
    setLoading(true);
    try {
      const next = await api<WordPage>(
        `/api/v1/words?${new URLSearchParams({ query, page: String(page), page_size: String(settings.pageSize) })}`,
      );
      if (requestVersion !== listRequestVersion.current) return;
      if (next.total > 0 && page > Math.ceil(next.total / settings.pageSize)) {
        setPage(Math.ceil(next.total / settings.pageSize));
        return;
      }
      setData(next);
      setError('');
    } catch (failure) {
      if (requestVersion === listRequestVersion.current) {
        setError(message(failure));
        handleUnauthorized(failure);
      }
    } finally {
      if (requestVersion === listRequestVersion.current) setLoading(false);
    }
  }, [session, checking, query, page, settings.pageSize, handleUnauthorized]);
  useEffect(() => {
    if (!session || checking) return;
    const timer = setTimeout(() => {
      void loadWords();
    }, 180);
    return () => {
      clearTimeout(timer);
      listRequestVersion.current++;
    };
  }, [loadWords, session, checking]);
  const refreshDetail = useCallback(
    async (id: string, quiet = false) => {
      const requestVersion = ++detailRequestVersion.current;
      if (!quiet) setDetailLoading(true);
      try {
        const latest = await api<Word>(`/api/v1/words/${id}`);
        if (requestVersion !== detailRequestVersion.current || selectedRef.current?.id !== id)
          return;
        setSelected(latest);
        selectedRef.current = latest;
        setDetailError('');
      } catch (failure) {
        if (requestVersion === detailRequestVersion.current && selectedRef.current?.id === id) {
          setDetailError(message(failure));
          handleUnauthorized(failure);
        }
      } finally {
        if (requestVersion === detailRequestVersion.current) setDetailLoading(false);
      }
    },
    [handleUnauthorized],
  );
  useEffect(() => {
    if (!session || checking) return;
    const refresh = () => {
      if (document.visibilityState !== 'visible') return;
      void loadWords();
      if (selectedRef.current) void refreshDetail(selectedRef.current.id, true);
    };
    const interval = setInterval(refresh, 12000);
    window.addEventListener('focus', refresh);
    document.addEventListener('visibilitychange', refresh);
    return () => {
      clearInterval(interval);
      window.removeEventListener('focus', refresh);
      document.removeEventListener('visibilitychange', refresh);
    };
  }, [session, checking, loadWords, refreshDetail]);
  const editorNeedsSignIn = !!editor && session?.owner_id !== editor.ownerID;
  useEffect(() => {
    if (!editorNeedsSignIn) return;
    const resume = () => {
      if (document.visibilityState === 'visible') void checkSession();
    };
    window.addEventListener('focus', resume);
    document.addEventListener('visibilitychange', resume);
    return () => {
      window.removeEventListener('focus', resume);
      document.removeEventListener('visibilitychange', resume);
    };
  }, [editorNeedsSignIn, checkSession]);
  function open(word: Word) {
    setDetailError('');
    setSelected(word);
    selectedRef.current = word;
    void refreshDetail(word.id);
  }
  function closeDetail() {
    setSelected(null);
    selectedRef.current = null;
    detailRequestVersion.current++;
  }
  function discardSessionAndDraft() {
    sessionRequestVersion.current++;
    updateSession(null);
    setChecking(false);
    setEditor(null);
    closeDetail();
  }
  async function signOut() {
    if (!session || signingOut) return;
    setSigningOut(true);
    setSessionError('');
    try {
      await write('/auth/logout', 'POST', {}, session.csrf_token);
      discardSessionAndDraft();
    } catch (failure) {
      if (failure instanceof APIError && [401, 502].includes(failure.status))
        discardSessionAndDraft();
      setSessionError(message(failure));
    } finally {
      setSigningOut(false);
    }
  }

  return (
    <main
      id="vocab-word-grid"
      data-theme={settings.theme}
      style={{ '--vg-columns': settings.columns } as CSSProperties}
    >
      <header className="vg-top">
        <span className="vg-brand">VocabBank</span>
        <div className="vg-account">
          <div className="vg-theme" role="group" aria-label="Color theme">
            {(['light', 'dark'] as const).map((theme) => (
              <button
                key={theme}
                type="button"
                aria-pressed={settings.theme === theme}
                onClick={() => setSettings((old) => ({ ...old, theme }))}
              >
                {theme === 'light' ? 'Light' : 'Dark'}
              </button>
            ))}
          </div>
          {session && (
            <button className="vg-secondary" type="button" onClick={signOut} disabled={signingOut}>
              Sign out
            </button>
          )}
        </div>
      </header>
      <div className="vg-title">
        <h1>Your vocabulary</h1>
        {session && data && (
          <span className="vg-total">
            {data.total} {data.total === 1 ? 'word' : 'words'}
            {query && ' matching'}
          </span>
        )}
      </div>
      {checking ? (
        <p role="status" className="vg-empty">
          Checking sign-in…
        </p>
      ) : !session ? (
        <section className="vg-session">
          <h2>Keep the words you want to learn.</h2>
          <p>
            Save a word or expression now. Ask your connected agent to add English meanings and
            examples later.
          </p>
          {sessionError && (
            <div className="vg-error-box" role="alert">
              <p>{sessionError}</p>
              <button className="vg-secondary" type="button" onClick={checkSession}>
                Retry sign-in check
              </button>
            </div>
          )}
          <a className="vg-action" href="/auth/login">
            Sign in
          </a>
        </section>
      ) : (
        <>
          {sessionError && (
            <div className="vg-error-box" role="alert">
              <p>{sessionError}</p>
              <button
                className="vg-secondary"
                type="button"
                onClick={signOut}
                disabled={signingOut}
              >
                Retry sign out
              </button>
            </div>
          )}
          <div className="vg-tools">
            <input
              className="vg-search"
              type="search"
              aria-label="Search words"
              placeholder="Search words…"
              maxLength={120}
              value={query}
              onChange={(event) => {
                setQuery(event.target.value);
                setPage(1);
              }}
            />
            <div className="vg-view">
              <label htmlFor="vg-columns">
                Columns
                <select
                  id="vg-columns"
                  value={settings.columns}
                  onChange={(event) =>
                    setSettings((old) => ({ ...old, columns: Number(event.target.value) as 3 | 4 }))
                  }
                >
                  <option value="3">3</option>
                  <option value="4">4</option>
                </select>
              </label>
              <label htmlFor="vg-page-size">
                Words per page
                <select
                  id="vg-page-size"
                  value={settings.pageSize}
                  onChange={(event) => {
                    setSettings((old) => ({
                      ...old,
                      pageSize: Number(event.target.value) as 30 | 50 | 100,
                    }));
                    setPage(1);
                  }}
                >
                  <option value="30">30</option>
                  <option value="50">50</option>
                  <option value="100">100</option>
                </select>
              </label>
            </div>
            {canWrite && (
              <button
                className="vg-action"
                type="button"
                onClick={() => {
                  setNotice('');
                  setEditor({ ownerID: session.owner_id });
                }}
              >
                + Add word
              </button>
            )}
          </div>
          {error && (
            <div className="vg-error-box" role="alert">
              <p>{error}</p>
              <button className="vg-secondary" type="button" onClick={loadWords}>
                Retry words
              </button>
            </div>
          )}
          <div className="vg-status" role="status">
            {loading && !data ? 'Loading words…' : ''}
          </div>
          {data && (
            <>
              <ul className="vg-grid" aria-label="Vocabulary words">
                {data.items.map((word) => (
                  <li key={word.id}>
                    <button
                      className="vg-word"
                      type="button"
                      aria-haspopup="dialog"
                      onClick={() => open(word)}
                    >
                      {word.term}
                    </button>
                  </li>
                ))}
              </ul>
              {!loading && data.items.length === 0 && (
                <p className="vg-empty">
                  {query
                    ? 'No matching words.'
                    : 'No words yet. Add your first word or expression.'}
                </p>
              )}
              <div className="vg-bottom">
                <span>Click a word to see its meaning.</span>
                {data.total > 0 && (
                  <div className="vg-pagination">
                    <button
                      className="vg-secondary"
                      type="button"
                      aria-label="Previous words"
                      disabled={page === 1 || loading}
                      onClick={() => setPage((old) => old - 1)}
                    >
                      Previous
                    </button>
                    <span>
                      {(data.page - 1) * data.page_size + 1}–
                      {Math.min(data.page * data.page_size, data.total)} of {data.total}
                    </span>
                    <button
                      className="vg-secondary"
                      type="button"
                      aria-label="Next words"
                      disabled={page * settings.pageSize >= data.total || loading}
                      onClick={() => setPage((old) => old + 1)}
                    >
                      Next
                    </button>
                  </div>
                )}
              </div>
            </>
          )}
          <p className="vg-live" aria-live="polite">
            {notice}
          </p>
        </>
      )}
      {session && selected && (
        <Learning
          key={selected.id}
          word={selected}
          error={detailError}
          loading={detailLoading}
          retry={() => refreshDetail(selected.id)}
          close={closeDetail}
          canWrite={canWrite}
          edit={() => {
            setEditor({ word: selected, ownerID: session.owner_id });
            closeDetail();
          }}
        />
      )}
      {/* Keep the editor mounted independently of sign-in so its fields,
          expected revision and exact retry intent survive authentication loss. */}
      {editor && (
        <Editor
          word={editor.word}
          ownerID={editor.ownerID}
          session={session}
          checkingSession={checking}
          sessionError={sessionError}
          checkSession={checkSession}
          handleUnauthorized={handleUnauthorized}
          close={() => {
            if (editor.word && session?.owner_id === editor.ownerID) open(editor.word);
            setEditor(null);
          }}
          saved={(word) => {
            setEditor(null);
            if (sessionRef.current?.owner_id !== editor.ownerID) return;
            setQuery('');
            setPage(1);
            setNotice(`“${word.term}” saved.`);
            void loadWords();
            open(word);
          }}
        />
      )}
    </main>
  );
}
