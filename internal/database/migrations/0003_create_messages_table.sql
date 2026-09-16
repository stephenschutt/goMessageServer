-- The message log.
--
-- Until now messages lived in a slice inside the server process, which was
-- fine for one server and wrong for three: the EKS deployment in infra/terraform
-- runs several replicas, and a message posted to one of them was invisible to a
-- client polling either of the others. Moving the log here is what makes any
-- replica able to serve any client.
--
-- "Messages" holds the transcript. sender is the name its author had at the
-- moment they sent it, stored rather than joined, so that renaming yourself
-- changes what people call you from now on and not what you already said.
--
-- "messageRecipients" is the delivery list: the keys allowed to read a message,
-- fixed when it was sent. It is a second table rather than a column because
-- that is what lets a reader be found by index instead of by scanning. The
-- consequence is the one the in-memory version had too — joining a chat does
-- not hand you the history from before you were in it, and leaving one does not
-- erase what you were already shown.
--
-- As in 0001 and 0002, table names are double quoted so their camel case
-- survives Postgres and column names are left unquoted so they fold the same
-- way here and in the queries in messages.go.

CREATE TABLE IF NOT EXISTS "Messages" (
    id        BIGINT PRIMARY KEY,
    senderKey TEXT NOT NULL,
    sender    TEXT NOT NULL,
    body      TEXT NOT NULL,
    -- RFC3339 with nanoseconds, in UTC, as TEXT: the spelling sorts in the same
    -- order it happened, and it is the one timestamp type both engines read
    -- back identically. "schemaMigrations".appliedAt does the same.
    sentAt    TEXT NOT NULL
);

-- The sender's own copy of a message is found by this index; everybody else's
-- comes from "messageRecipients" below.
CREATE INDEX IF NOT EXISTS "messagesBySender" ON "Messages" (senderKey, id);

CREATE TABLE IF NOT EXISTS "messageRecipients" (
    messageId    BIGINT NOT NULL,
    recipientKey TEXT NOT NULL,
    PRIMARY KEY (messageId, recipientKey)
);

-- Polling asks "what is there for me after id N", so the reader comes first and
-- the message id second. The primary key above covers the other direction, which
-- is how a message's own delivery list is read back.
CREATE INDEX IF NOT EXISTS "messageRecipientsByKey" ON "messageRecipients" (recipientKey, messageId);

-- Message ids, handed out one at a time.
--
-- This exists because the two engines spell auto-increment incompatibly —
-- SQLite wants AUTOINCREMENT on an INTEGER PRIMARY KEY, Postgres wants
-- GENERATED AS IDENTITY or a sequence — and a migration file is applied to
-- whichever one DATABASE_URL points at, so it can only contain syntax both
-- accept. Incrementing a row and reading it back inside the sending
-- transaction is portable, and it keeps the property the clients rely on: ids
-- that only ever go up, so "?since=<id>" is a complete cursor.
--
-- The single row is id = 1. The table has a primary key only so that the
-- INSERT below can be repeated harmlessly.
CREATE TABLE IF NOT EXISTS "messageSequence" (
    id     INTEGER PRIMARY KEY,
    nextId BIGINT NOT NULL
);

INSERT INTO "messageSequence" (id, nextId) VALUES (1, 0)
    ON CONFLICT (id) DO NOTHING;
