// This browser's identity: one RSA key pair, generated on the first visit and
// kept in localStorage from then on — the same arrangement as the iOS app,
// which keeps its pair in a local SQLite table.
//
// Only the public half is ever sent. The private half signs a summary of each
// request (see api.js) so that possession of it, rather than the public key
// alone, is what the server checks.
//
// Keeping a private key in localStorage means any script that runs on this
// origin can read it. That is the trade this client makes for being able to
// show and re-import its own key the way the iOS app can; the page is served
// from the same origin as the API and loads no third-party script.

const ALGORITHM = {
  name: 'RSASSA-PKCS1-v1_5',
  modulusLength: 2048,
  publicExponent: new Uint8Array([1, 0, 1]),
  hash: 'SHA-256',
};

const STORED_PRIVATE = 'messages.privateKey';
const STORED_PUBLIC = 'messages.publicKey';

/// The pair in the form the rest of the app needs it: a CryptoKey to sign with
/// and the base64 SPKI string the server stores in authorizedUsers.
let pair = null;

export function base64(buffer) {
  let binary = '';
  for (const byte of new Uint8Array(buffer)) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function bytes(encoded) {
  const binary = atob(encoded);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) out[i] = binary.charCodeAt(i);
  return out;
}

/// Loads the stored pair, or creates and stores one on the first visit. Safe to
/// call repeatedly; the work happens once.
export async function loadKeys() {
  if (pair) return pair;
  if (!window.isSecureContext || !window.crypto?.subtle) {
    throw new Error('Signing needs a secure context — open this page over localhost or https.');
  }

  const storedPrivate = localStorage.getItem(STORED_PRIVATE);
  const storedPublic = localStorage.getItem(STORED_PUBLIC);
  if (storedPrivate && storedPublic) {
    try {
      const privateKey = await crypto.subtle.importKey(
        'pkcs8', bytes(storedPrivate), ALGORITHM, true, ['sign']);
      pair = { privateKey, publicKey: storedPublic };
      return pair;
    } catch (e) {
      // What is stored no longer imports — the only way forward is a new pair,
      // which simply has to be authorized again.
      console.warn('stored key could not be imported; making a new one', e);
    }
  }
  return createKeys();
}

/// Throws away the stored pair and makes a new one. The new key is unknown to
/// the server, so it has to be authorized before the app works again.
export async function createKeys() {
  const generated = await crypto.subtle.generateKey(ALGORITHM, true, ['sign', 'verify']);
  const pkcs8 = await crypto.subtle.exportKey('pkcs8', generated.privateKey);
  const spki = await crypto.subtle.exportKey('spki', generated.publicKey);

  localStorage.setItem(STORED_PRIVATE, base64(pkcs8));
  localStorage.setItem(STORED_PUBLIC, base64(spki));
  pair = { privateKey: generated.privateKey, publicKey: base64(spki) };
  return pair;
}

/// Signs `message` the way the Go server verifies it: RSA PKCS#1 v1.5 over
/// SHA-256.
export async function sign(message) {
  const { privateKey } = await loadKeys();
  const signature = await crypto.subtle.sign(
    { name: 'RSASSA-PKCS1-v1_5' }, privateKey, message);
  return base64(signature);
}

export async function publicKey() {
  return (await loadKeys()).publicKey;
}
