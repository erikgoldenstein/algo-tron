// WebSocket entry point.
//
// On every incoming frame we forward to gameState.applyMessage (which
// mutates state) and then call updateDom (which writes to the page).
// The canvas redraws on its own 30fps loop in render.js — it reads gameState
// directly, so we don't need to nudge it here.
//
// On disconnect we reconnect with a 1s backoff. If a session was previously
// established (we saw at least one init frame) and the socket later opens
// again, we hard-reload the page so any new static assets shipped by a
// redeployed server come into effect.
//
// Depends on: dom.js (updateDom, showShutdownBanner), gameState.js
// (applyMessage).

let hadActiveSession = false;

function connect() {
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(scheme + '://' + location.host + '/ws');
  ws.onopen = () => {
    if (hadActiveSession) location.reload();
  };
  ws.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    if (msg.type === 'misc' && msg.content === 'shutdown') { showShutdownBanner(true); return; }
    if (msg.type === 'init') { showShutdownBanner(false); hadActiveSession = true; }
    applyMessage(msg);
    updateDom();
  };
  ws.onclose = () => setTimeout(connect, 1000);
  ws.onerror = () => ws.close();
}
connect();
