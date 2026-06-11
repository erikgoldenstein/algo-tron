// WebSocket entry point + board subscription.
//
// On every incoming frame we forward to gameState.applyMessage (which
// mutates state) and then call updateDom (which writes to the page).
// The canvas redraws on its own 30fps loop in render.js — it reads gameState
// directly, so we don't need to nudge it here.
//
// The server streams only the board we subscribe to. watchBoard(id) asks for
// another one (the server answers with a fresh "game" snapshot); whenever
// the board list changes we make sure we're still watching a live board.
//
// On disconnect we reconnect with a 1s backoff. If a session was previously
// established (we saw at least one init frame) and the socket later opens
// again, we hard-reload the page so any new static assets shipped by a
// redeployed server come into effect.
//
// Depends on: dom.js (updateDom, showShutdownBanner), gameState.js
// (applyMessage, gameState).
// Provides: watchBoard, stepBoard, ensureWatched.

let hadActiveSession = false;
let ws = null;

function watchBoard(id) {
  if (id && ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ watch: id }));
  }
}

// stepBoard switches to the previous (-1) / next (+1) board, wrapping.
function stepBoard(delta) {
  const ids = gameState.boards.map((b) => b.id);
  if (!ids.length) return;
  const i = ids.indexOf(gameState.game?.id);
  watchBoard(i < 0 ? ids[0] : ids[(i + delta + ids.length) % ids.length]);
}

// If the board we're watching is gone (or we never had one), subscribe to
// the followed player's board, otherwise the first live board. Called after
// board-list changes.
function ensureWatched() {
  const ids = gameState.boards.map((b) => b.id);
  const followed = followedBoardID();
  if (followed && gameState.game?.id !== followed) {
    watchBoard(followed);
    return;
  }
  if (gameState.game && ids.includes(gameState.game.id)) return;
  if (ids.length) watchBoard(ids[0]);
}

function followedBoardID() {
  const name = gameState.followName;
  if (!name) return '';
  return gameState.boards.find((b) => (b.names || []).includes(name))?.id || '';
}

function connect() {
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  ws = new WebSocket(scheme + '://' + location.host + '/ws');
  ws.onopen = () => {
    if (hadActiveSession) location.reload();
  };
  ws.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    if (msg.type === 'misc' && msg.content === 'shutdown') { showShutdownBanner(true); return; }
    if (msg.type === 'init') { showShutdownBanner(false); hadActiveSession = true; }
    applyMessage(msg);
    if (msg.type === 'init' || msg.type === 'boards') ensureWatched();
    updateDom();
  };
  ws.onclose = () => setTimeout(connect, 1000);
  ws.onerror = () => ws.close();
}
connect();
