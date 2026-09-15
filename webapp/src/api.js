// Every call the app makes to the Go server, signed with this browser's key.
//
// The credential headers and the string they sign are the same ones auth.go
// verifies and the iOS client produces: method, path, query, timestamp, nonce
// and a hash of the body, each on its own line. Signing the request rather than
// merely presenting the public key is what stops a key copied off the wire from
// being usable.

import { base64, publicKey, sign } from './keys.js';

/// A refusal from the server. `status` 401 or 403 means this browser is not
/// (yet) allowed in, which is what the app turns into its 404 page.
export class ApiError extends Error {
  constructor(status, detail) {
    super(detail || `Server returned HTTP ${status}`);
    this.name = 'ApiError';
    this.status = status;
    this.detail = detail;
  }

  get isUnauthorized() {
    return this.status === 401 || this.status === 403;
  }
}

function nonce() {
  return base64(crypto.getRandomValues(new Uint8Array(16)));
}

async function sha256Hex(body) {
  const digest = await crypto.subtle.digest('SHA-256', body);
  return [...new Uint8Array(digest)]
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('');
}

/// One signed request. Everything the server answers with is JSON, so there is
/// one shape of call here and no special cases.
async function request(method, path, { query, body } = {}) {
  const url = new URL(path, window.location.origin);
  if (query) {
    for (const [name, value] of Object.entries(query)) {
      url.searchParams.set(name, value);
    }
  }

  const payload = body === undefined ? new Uint8Array() : new TextEncoder().encode(JSON.stringify(body));
  const timestamp = String(Math.floor(Date.now() / 1000));
  const value = nonce();
  const canonical = [
    method,
    url.pathname,
    url.search.replace(/^\?/, ''),
    timestamp,
    value,
    await sha256Hex(payload),
  ].map((part) => `${part}\n`).join('');

  const headers = {
    'X-Public-Key': await publicKey(),
    'X-Timestamp': timestamp,
    'X-Nonce': value,
    'X-Signature': await sign(new TextEncoder().encode(canonical)),
  };
  if (body !== undefined) headers['Content-Type'] = 'application/json';

  const response = await fetch(url, {
    method,
    headers,
    body: body === undefined ? undefined : payload,
  });

  if (!response.ok) {
    // The server answers a refusal with a sentence saying what was wrong, which
    // is a better error than the status code on its own.
    const detail = (await response.text()).trim();
    throw new ApiError(response.status, detail.length <= 160 ? detail : '');
  }
  return response.json();
}

export const api = {
  /// Who this browser is and who is in its chat.
  chat: () => request('GET', '/api/chat'),
  setUsername: (username) => request('PUT', '/api/username', { body: { username } }),
  search: (q) => request('GET', '/api/users', { query: { q } }),
  addToChat: (username) => request('POST', '/api/chat', { body: { username } }),
  removeFromChat: (username) => request('DELETE', '/api/chat', { query: { username } }),
  messagesSince: (since) => request('GET', '/api/messages', { query: { since } }),
  send: (text) => request('POST', '/api/messages', { body: { text } }),
};
