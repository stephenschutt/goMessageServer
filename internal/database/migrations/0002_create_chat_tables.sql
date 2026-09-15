-- Usernames and chat membership.
--
-- The allow list stays exactly what it was — a bare column of public keys — so
-- the profile a user chooses for themselves lives here instead. A row in
-- "userNames" is what turns an authorized key into someone other people can
-- find by name; a key with no row is authorized but anonymous, which is the
-- state every device starts in.
--
-- "chatMembers" is the chat each user has built for themselves. Membership is
-- symmetric: adding someone writes both directions, so the other side can
-- answer without having to add you back. Both columns hold public keys rather
-- than names, so a user renaming themselves does not empty anyone's chat.
--
-- As in 0001, table names are double quoted so their camel case survives
-- Postgres, and column names are left unquoted so they fold the same way here
-- and in the queries in userdb.go.

CREATE TABLE IF NOT EXISTS "userNames" (
    publicKey TEXT PRIMARY KEY,
    username  TEXT NOT NULL
);

-- One name to one person, whatever case it was typed in: the search in
-- userdb.go matches case-insensitively, so two users differing only in case
-- would be indistinguishable to anyone trying to add them.
CREATE UNIQUE INDEX IF NOT EXISTS "userNamesByName" ON "userNames" (lower(username));

CREATE TABLE IF NOT EXISTS "chatMembers" (
    ownerKey  TEXT NOT NULL,
    memberKey TEXT NOT NULL,
    PRIMARY KEY (ownerKey, memberKey)
);

-- Membership is read in both directions: "who is in my chat" walks ownerKey,
-- which the primary key already covers, and removing an account walks memberKey.
CREATE INDEX IF NOT EXISTS "chatMembersByMember" ON "chatMembers" (memberKey);
