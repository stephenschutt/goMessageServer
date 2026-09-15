import { useCallback, useEffect, useRef, useState } from 'react';

import { api } from '../api.js';

/// How long typing has to pause before the directory is searched. Every
/// keystroke would otherwise be a query on the server.
const SEARCH_DEBOUNCE = 250;

/// The people panel: who you are, who else there is, and who is in your chat —
/// the same three sections as the iOS app's People screen, in the same order,
/// because that is the order a new browser needs them in.
export default function People({
  chat, publicKey, onClose, onSetUsername, onAdd, onRemove, onNewKey,
}) {
  const [draftName, setDraftName] = useState(chat.username);
  const [nameNotice, setNameNotice] = useState(null);
  const [savingName, setSavingName] = useState(false);

  const [query, setQuery] = useState('');
  const [results, setResults] = useState([]);
  const [searchNotice, setSearchNotice] = useState(null);

  const [copied, setCopied] = useState(false);
  const searchSeq = useRef(0);

  const runSearch = useCallback(async (text) => {
    // Only the newest search may write the results: a slow earlier one must not
    // land on top of what the user is looking at now.
    const seq = searchSeq.current + 1;
    searchSeq.current = seq;
    try {
      const found = await api.search(text);
      if (searchSeq.current !== seq) return;
      setResults(found);
      setSearchNotice(found.length
        ? null
        : (text.trim()
          ? 'Nobody by that name.'
          : 'Nobody else has chosen a username yet.'));
    } catch (error) {
      if (searchSeq.current !== seq) return;
      setResults([]);
      setSearchNotice(error.message);
    }
  }, []);

  useEffect(() => {
    const timer = setTimeout(() => runSearch(query), SEARCH_DEBOUNCE);
    return () => clearTimeout(timer);
  }, [query, runSearch]);

  const saveName = async () => {
    const name = draftName.trim();
    if (!name || savingName) return;
    setSavingName(true);
    setNameNotice(null);
    try {
      await onSetUsername(name);
      setNameNotice({ text: `Saved. Other people can find you as ${name}.`, error: false });
      // A first name changes who can be added at all, and a rename changes what
      // the directory calls this browser.
      runSearch(query);
    } catch (error) {
      setNameNotice({ text: error.message, error: true });
    }
    setSavingName(false);
  };

  const act = async (run, username) => {
    try {
      await run(username);
      setSearchNotice(null);
      runSearch(query);
    } catch (error) {
      setSearchNotice(error.message);
    }
  };

  const copyKey = async () => {
    try {
      await navigator.clipboard.writeText(publicKey);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard access can be refused; the key is selectable either way.
      setCopied(false);
    }
  };

  return (
    <div className="panel">
      <div className="panel-head">
        <strong>Your chat</strong>
        <button type="button" className="panel-done" onClick={onClose}>Done</button>
      </div>

      <div className="panel-body">
        <span className="field-label">Your username</span>
        <div className="row-inline">
          <input
            type="text"
            maxLength={20}
            placeholder="pick a name"
            autoComplete="off"
            spellCheck={false}
            value={draftName}
            onChange={(event) => setDraftName(event.target.value)}
            onKeyDown={(event) => { if (event.key === 'Enter') saveName(); }}
          />
          <button
            type="button"
            className="primary"
            disabled={savingName || !draftName.trim() || draftName.trim() === chat.username}
            onClick={saveName}
          >
            {savingName ? 'Saving…' : 'Save'}
          </button>
        </div>
        <p className={`hint${nameNotice?.error ? ' error' : ''}`}>
          {nameNotice
            ? nameNotice.text
            : 'This is the name other people search for to add you. 3–20 characters: letters, digits, and . _ - between them.'}
        </p>

        <span className="field-label">Add people</span>
        <input
          type="search"
          placeholder="Search usernames"
          autoComplete="off"
          spellCheck={false}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <ul className="people-list">
          {results.map((entry) => (
            <li key={entry.username}>
              <span>{entry.username}</span>
              {entry.inChat
                ? <span className="quiet">In chat</span>
                : (
                  <button type="button" onClick={() => act(onAdd, entry.username)}>Add</button>
                )}
            </li>
          ))}
          {searchNotice && <li className="empty">{searchNotice}</li>}
        </ul>

        <span className="field-label">In your chat</span>
        <ul className="people-list">
          {chat.members.map((member) => (
            <li key={member}>
              <span>{member}</span>
              <button type="button" className="remove" onClick={() => act(onRemove, member)}>
                Remove
              </button>
            </li>
          ))}
          {chat.members.length === 0 && (
            <li className="empty">No one yet — search above to add someone.</li>
          )}
        </ul>
        <p className="hint">
          Adding someone puts you in each other&rsquo;s chat, so they can answer straight away.
          Everyone here sees the messages you send.
        </p>

        <span className="field-label">This browser</span>
        <textarea className="key" readOnly value={publicKey} />
        <div className="row-inline">
          <button type="button" className="primary" onClick={copyKey}>
            {copied ? 'Copied' : 'Copy public key'}
          </button>
          <button type="button" className="remove" onClick={onNewKey}>New key</button>
        </div>
        <p className="hint">
          Only the public key leaves this browser. The private key is kept in this
          browser&rsquo;s local storage. A new key has to be authorized again.
        </p>
      </div>
    </div>
  );
}
