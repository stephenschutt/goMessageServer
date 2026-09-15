-- The authorization tables.
--
-- Each holds exactly one column, the base64 DER SPKI form of a client's RSA
-- public key. That is deliberate: the allow list should not be able to grow
-- into a user profile, so there is nowhere to put a name, an address or a
-- last-seen time.
--
-- The table names are double quoted so their camel case survives Postgres,
-- which folds unquoted identifiers to lower case and would otherwise create
-- "authorizedusers" instead.
--
-- The publicKey column is deliberately NOT quoted: Postgres folds it to
-- "publickey", and the queries in userdb.go are unquoted too, so both sides
-- fold identically. Quoting it here without quoting it there would break them.

-- Keys permitted to use the chat API.
CREATE TABLE IF NOT EXISTS "authorizedUsers" (
    publicKey TEXT PRIMARY KEY
);

-- Keys that proved they own their private half but are not on the allow list.
-- Identical in shape to "authorizedUsers"; it is the queue an administrator
-- promotes keys from.
CREATE TABLE IF NOT EXISTS "unauthorizedUsers" (
    publicKey TEXT PRIMARY KEY
);
