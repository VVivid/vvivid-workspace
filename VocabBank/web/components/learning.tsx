'use client';
import { useEffect, useRef, useState } from 'react';
import { Word } from '../lib/api';
import Dialog from './dialog';

function highlightTermInSentence(text: string, term: string) {
  const start = text.toLowerCase().indexOf(term.toLowerCase());
  if (start < 0) return text;
  return (
    <>
      {text.slice(0, start)}
      <strong>{text.slice(start, start + term.length)}</strong>
      {text.slice(start + term.length)}
    </>
  );
}
export default function Learning({
  word,
  error,
  loading,
  retry,
  close,
  edit,
  canWrite,
}: {
  word: Word;
  error: string;
  loading: boolean;
  retry: () => void;
  close: () => void;
  edit: () => void;
  canWrite: boolean;
}) {
  const [selected, setSelected] = useState(0);
  const tabs = useRef<(HTMLButtonElement | null)[]>([]);
  useEffect(() => {
    setSelected((old) => Math.min(old, Math.max(0, word.senses.length - 1)));
  }, [word.senses.length]);
  const index = Math.min(selected, Math.max(0, word.senses.length - 1));
  const sense = word.senses[index];
  const multiple = word.senses.length > 1;
  const senseTabLabel = (position: number) => {
    const partOfSpeech = word.senses[position].part_of_speech;
    const label = partOfSpeech[0].toUpperCase() + partOfSpeech.slice(1);
    const matchingSenses = word.senses.filter((item) => item.part_of_speech === partOfSpeech);
    if (matchingSenses.length <= 1) return label;

    const occurrence = word.senses
      .slice(0, position + 1)
      .filter((item) => item.part_of_speech === partOfSpeech).length;
    return `${label} ${occurrence}`;
  };
  return (
    <Dialog id="vg-definition" titleID="vg-word-title" close={close}>
      <div className="vg-modal-head">
        <div>
          <h2 id="vg-word-title">{word.term}</h2>
          <div className="vg-word-meta">
            <span>English</span>
            {word.senses.length === 1 && <span>{sense.part_of_speech}</span>}
          </div>
        </div>
        <button className="vg-close" type="button" onClick={close} aria-label="Close word details">
          ×
        </button>
      </div>
      {multiple && (
        <div
          className="vg-sense-tabs"
          role="tablist"
          aria-label="Word meanings"
          onKeyDown={(event) => {
            if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
            event.preventDefault();
            const next =
              event.key === 'Home'
                ? 0
                : event.key === 'End'
                  ? word.senses.length - 1
                  : (index + (event.key === 'ArrowRight' ? 1 : -1) + word.senses.length) %
                    word.senses.length;
            setSelected(next);
            tabs.current[next]?.focus();
          }}
        >
          {word.senses.map((_, position) => (
            <button
              key={position}
              ref={(node) => {
                tabs.current[position] = node;
              }}
              id={`sense-tab-${position}`}
              role="tab"
              type="button"
              aria-controls="vg-word-body"
              aria-selected={index === position}
              tabIndex={index === position ? 0 : -1}
              onClick={() => setSelected(position)}
            >
              {senseTabLabel(position)}
            </button>
          ))}
        </div>
      )}
      <div className="vg-modal-body">
        {loading && (
          <p role="status" className="vg-help">
            Refreshing word…
          </p>
        )}
        {error && (
          <div className="vg-error-box" role="alert">
            <p>{error}</p>
            <button className="vg-secondary" onClick={retry}>
              Retry word
            </button>
          </div>
        )}
        <div
          id="vg-word-body"
          role={multiple ? 'tabpanel' : undefined}
          aria-labelledby={multiple ? `sense-tab-${index}` : undefined}
          tabIndex={multiple ? 0 : undefined}
        >
          {!sense ? (
            <div className="vg-pending">
              <p>Saved and ready to learn</p>
              <p>Ask your agent to add a simple meaning, examples, and when to use this word.</p>
            </div>
          ) : (
            <>
              <section className="vg-learn-section">
                <h3>What it means</h3>
                <p className="vg-plain-meaning">{sense.definition}</p>
              </section>
              <section className="vg-learn-section">
                <h3>When to use it</h3>
                <p className="vg-use-case">{sense.when_to_use}</p>
              </section>
              <section className="vg-learn-section">
                <h3>Examples in context</h3>
                <div className="vg-context-examples">
                  {sense.examples.map((example, i) => (
                    <div className="vg-context-example" key={i}>
                      <span className="vg-example-context">{example.context}</span>
                      <p>{highlightTermInSentence(example.sentence, word.term)}</p>
                    </div>
                  ))}
                </div>
              </section>
              <section className="vg-learn-section">
                <h3>Common phrases</h3>
                <ul className="vg-phrase-list">
                  {sense.common_phrases.map((phrase, i) => (
                    <li key={i}>{phrase}</li>
                  ))}
                </ul>
              </section>
              {sense.usage_tip && (
                <details className="vg-usage-tip">
                  <summary>Usage tip</summary>
                  <p>{sense.usage_tip}</p>
                </details>
              )}
            </>
          )}
        </div>
        {(word.original_sentence || word.source_url || word.personal_note) && (
          <section className="vg-original">
            {word.original_sentence && (
              <>
                <div className="vg-label">Your saved sentence</div>
                <p className="vg-preserve">{word.original_sentence}</p>
              </>
            )}
            {word.source_url && (
              <>
                <div className="vg-label">Your source</div>
                <a href={word.source_url} target="_blank" rel="noopener noreferrer nofollow">
                  {word.source_url}
                </a>
              </>
            )}
            {word.personal_note && (
              <>
                <div className="vg-label">Your note</div>
                <p className="vg-preserve">{word.personal_note}</p>
              </>
            )}
          </section>
        )}
      </div>
      <div className="vg-modal-foot vg-learning-footer">
        <small>
          {sense ? 'Illustrative learning examples' : 'Details can be added through MCP'}
        </small>
        <div>
          {canWrite && (
            <button className="vg-secondary" type="button" onClick={edit}>
              Edit word
            </button>
          )}{' '}
          <button className="vg-secondary" type="button" onClick={close}>
            Close
          </button>
        </div>
      </div>
    </Dialog>
  );
}
