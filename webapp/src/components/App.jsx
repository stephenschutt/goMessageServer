import { useCallback, useEffect, useRef, useState } from 'react';

import { api, ApiError } from '../api.js';
import { createKeys, loadKeys } from '../keys.js';
import Chat from './Chat.jsx';
import NotFound from './NotFound.jsx';
import People from './People.jsx';

/// Poll cadences, matched to the other two clients so all three feel equally
/// live: messages every 1.5s, the roster every 5s because it only changes when
/// somebody adds or removes you.
const MESSAGE_POLL = 1500;
const CHAT_POLL = 5000;

/// App owns everything the two screens share: this browser's key, who it is,
/// who is in its chat, and the transcript.
///
/// There are only four things it can be doing — starting up, running, refused,
/// or broken before it ever reached the server — and `phase` is which.
export default function App() {
  const [phase, setPhase] = useState('starting');
  const [publicKey, setPublicKey] = useState('');
  const [fault, setFault] = useState('');
  const [chat, setChat] = useState({ username: '', members: [] });
  const [messages, setMessages] = useState([]);
  const [showPeople, setShowPeople] = useState(false);

  const lastID = useRef(0);
  const polling = useRef(false);

  /// Every failed call lands here. A key the server will not talk to is not an
  /// error to show in a banner — it is the whole answer, so it becomes the 404.
  const handle = useCallback((error) => {
    if (error instanceof ApiError && error.isUnauthorized) {
      setPhase('denied');
      return;
    }
    setFault(error.message);
  }, []);

  // Generating a 2048-bit key takes a moment on the very first visit, so the
  // app waits for it here rather than letting the first request do it.
  useEffect(() => {
    let live = true;
    loadKeys()
      .then(({ publicKey: key }) => {
        if (!live) return;
        setPublicKey(key);
        setPhase((current) => (current === 'starting' ? 'ready' : current));
      })
      .catch((error) => {
        if (!live) return;
        setFault(error.message);
        setPhase('broken');
      });
    return () => { live = false; };
  }, []);

  const refreshChat = useCallback(async () => {
    try {
      setChat(await api.chat());
      // Reaching this means the key is in authorizedUsers after all, which is
      // how the 404 page recovers once a device has been approved.
      setPhase((current) => (current === 'denied' ? 'ready' : current));
      setFault('');
    } catch (error) {
      handle(error);
    }
  }, [handle]);

  const pollMessages = useCallback(async () => {
    // Two polls in flight at once would both ask from the same id and append
    // the same messages twice.
    if (polling.current) return;
    polling.current = true;
    try {
      const fresh = await api.messagesSince(lastID.current);
      if (fresh.length) {
        lastID.current = Math.max(lastID.current, ...fresh.map((m) => m.id));
        setMessages((previous) => [...previous, ...fresh]);
      }
      setFault('');
    } catch (error) {
      handle(error);
    } finally {
      polling.current = false;
    }
  }, [handle]);

  useEffect(() => {
    if (phase !== 'ready') return undefined;
    pollMessages();
    const timer = setInterval(pollMessages, MESSAGE_POLL);
    return () => clearInterval(timer);
  }, [phase, pollMessages]);

  useEffect(() => {
    if (phase !== 'ready') return undefined;
    refreshChat();
    const timer = setInterval(refreshChat, CHAT_POLL);
    return () => clearInterval(timer);
  }, [phase, refreshChat]);

  // MARK: things the user does

  const send = useCallback(async (text) => {
    try {
      await api.send(text);
      await pollMessages();
    } catch (error) {
      handle(error);
    }
  }, [handle, pollMessages]);

  /// The people actions report their own failures to the panel, which shows
  /// them next to the field that caused them, so they rethrow rather than
  /// routing through `handle`.
  const setUsername = useCallback(async (name) => {
    await api.setUsername(name);
    await refreshChat();
  }, [refreshChat]);

  const addToChat = useCallback(async (username) => {
    setChat(await api.addToChat(username));
  }, []);

  const removeFromChat = useCallback(async (username) => {
    setChat(await api.removeFromChat(username));
  }, []);

  /// Discards this browser's identity for a new one. The new key is unknown to
  /// the server, so the app drops straight back to the 404 until it is approved.
  const newKey = useCallback(async () => {
    const { publicKey: key } = await createKeys();
    setPublicKey(key);
    setMessages([]);
    setChat({ username: '', members: [] });
    lastID.current = 0;
    setShowPeople(false);
    setPhase('denied');
  }, []);

  if (phase === 'starting') {
    return <div className="splash">Setting up this browser&rsquo;s key&hellip;</div>;
  }
  if (phase === 'broken') {
    return <div className="splash splash-error">{fault}</div>;
  }
  if (phase === 'denied') {
    return <NotFound publicKey={publicKey} onRetry={refreshChat} onNewKey={newKey} />;
  }

  return (
    <>
      <Chat
        chat={chat}
        messages={messages}
        fault={fault}
        onSend={send}
        onOpenPeople={() => setShowPeople(true)}
      />
      {showPeople && (
        <People
          chat={chat}
          publicKey={publicKey}
          onClose={() => setShowPeople(false)}
          onSetUsername={setUsername}
          onAdd={addToChat}
          onRemove={removeFromChat}
          onNewKey={newKey}
        />
      )}
    </>
  );
}
