/// What this browser sees until its key is in authorizedUsers.
///
/// The requirement is a friendly 404 rather than a "forbidden": to a browser
/// the server has not been told to trust, the app is simply not a page that
/// exists. The key is offered underneath anyway — without it there would be no
/// way for anyone to ever be let in, and it is not a secret.
export default function NotFound({ publicKey, onRetry, onNewKey }) {
  return (
    <div className="not-found">
      <h1>404</h1>
      <p className="lead">This page isn&rsquo;t available.</p>
      <p className="quiet">
        If you were expecting Messages, this browser hasn&rsquo;t been let in yet.
        Give this key to whoever runs the server:
      </p>
      <textarea className="key" readOnly value={publicKey} />
      <code>go run . -authorize &lsquo;&hellip;&rsquo;</code>
      <div className="row-inline center">
        <button type="button" className="primary" onClick={onRetry}>Check again</button>
        <button type="button" className="remove" onClick={onNewKey}>New key</button>
      </div>
    </div>
  );
}
