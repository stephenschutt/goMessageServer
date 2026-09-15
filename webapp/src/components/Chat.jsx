import { useEffect, useLayoutEffect, useRef, useState } from 'react';

/// A gap this long between messages earns a time separator, the way Messages
/// breaks a conversation into sessions.
const SEPARATOR_GAP = 15 * 60 * 1000;

/// The conversation: who you are talking to, the transcript, and the compose
/// bar. Everything that sets the chat up — a username, the people in it — is on
/// the People panel, so the dead ends here lead there.
export default function Chat({ chat, messages, fault, onSend, onOpenPeople }) {
  const [draft, setDraft] = useState('');
  const [sending, setSending] = useState(false);
  const transcript = useRef(null);
  const atBottom = useRef(true);

  const named = Boolean(chat.username);
  const canSend = named && !sending && draft.trim().length > 0;

  // Stay pinned to the newest message, but only for a reader who was already
  // there — scrolling back to re-read something should not be undone by the
  // next poll.
  useLayoutEffect(() => {
    const element = transcript.current;
    if (element && atBottom.current) element.scrollTop = element.scrollHeight;
  }, [messages]);

  useEffect(() => {
    const element = transcript.current;
    if (!element) return undefined;
    const onScroll = () => {
      atBottom.current = element.scrollTop + element.clientHeight >= element.scrollHeight - 20;
    };
    element.addEventListener('scroll', onScroll);
    return () => element.removeEventListener('scroll', onScroll);
  }, []);

  const submit = async () => {
    const text = draft.trim();
    if (!text) return;
    if (!named) {
      onOpenPeople();
      return;
    }
    setDraft('');
    setSending(true);
    atBottom.current = true;
    await onSend(text);
    setSending(false);
  };

  const title = chat.members.length ? chat.members.join(', ') : 'No one in this chat';

  return (
    <div className="phone">
      <div className="notch" />
      <StatusBar />

      <div className="nav-bar">
        <button type="button" className="nav-action" onClick={onOpenPeople}>People</button>
        <div className="nav-identity">
          <div className="avatar">{avatarLabel(chat.members)}</div>
          <div className="contact-name">{title}</div>
        </div>
        <span className="nav-action nav-action-quiet">
          {chat.username ? `you: ${chat.username}` : ''}
        </span>
      </div>

      {!named && (
        <Banner
          text="Choose a username to start"
          action="People"
          onAction={onOpenPeople}
        />
      )}
      {named && chat.members.length === 0 && (
        <Banner
          text="No one in this chat yet"
          action="Add people"
          onAction={onOpenPeople}
        />
      )}
      {fault && <Banner text={fault} />}

      <div className="messages" ref={transcript}>
        {messages.map((message, index) => (
          <Row
            key={message.id}
            message={message}
            mine={message.sender === chat.username}
            showSender={chat.members.length > 1}
            separator={needsSeparator(messages, index)}
          />
        ))}
      </div>

      <div className="input-bar">
        <textarea
          rows={1}
          className="text-input"
          placeholder="iMessage"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter' && !event.shiftKey) {
              event.preventDefault();
              submit();
            }
          }}
        />
        <button type="button" className="send" disabled={!canSend} onClick={submit}>&uarr;</button>
      </div>
    </div>
  );
}

function Row({ message, mine, showSender, separator }) {
  return (
    <>
      {separator && <div className="timestamp">{timeLabel(message.timestamp)}</div>}
      <div className={`row ${mine ? 'me' : 'them'}`}>
        <div className="bubble">
          {showSender && !mine && <div className="sender">{message.sender}</div>}
          {message.text}
        </div>
      </div>
    </>
  );
}

function Banner({ text, action, onAction }) {
  return (
    <div className="banner">
      <span>{text}</span>
      {action && (
        <button type="button" className="banner-action" onClick={onAction}>{action}</button>
      )}
    </div>
  );
}

/// The fake status bar that makes the frame read as a phone. The clock is real
/// so it does not look frozen next to the messages' own timestamps.
function StatusBar() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), 30000);
    return () => clearInterval(timer);
  }, []);

  const hours = now.getHours() % 12 || 12;
  const minutes = String(now.getMinutes()).padStart(2, '0');
  return (
    <div className="status-bar">
      <span>{`${hours}:${minutes}`}</span>
      <span>&#9679;&#9679;&#9679; &#128246; &#128267;</span>
    </div>
  );
}

function avatarLabel(members) {
  if (!members.length) return '?';
  if (members.length === 1) return members[0].slice(0, 1).toUpperCase();
  return String(members.length);
}

function needsSeparator(messages, index) {
  if (index === 0) return true;
  const gap = new Date(messages[index].timestamp) - new Date(messages[index - 1].timestamp);
  return gap > SEPARATOR_GAP;
}

function timeLabel(timestamp) {
  const when = new Date(timestamp);
  const time = when.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
  const today = new Date();
  if (when.toDateString() === today.toDateString()) return time;
  return `${when.toLocaleDateString([], { month: 'short', day: 'numeric' })}  ${time}`;
}
