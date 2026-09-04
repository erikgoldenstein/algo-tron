// WebSocket entry point + board subscription.
//
// On every incoming frame we forward to gameState.applyMessage (which
// mutates state) and then update the parts of the page affected by that
// message. Scoreboard rows are deliberately not rebuilt for ticks: the board
// table changes at round/lifecycle events, while the canvas and chat change
// every tick.
// The canvas redraws on its own 30fps loop in render.js — it reads gameState
// directly, so we don't need to nudge it here.
//
// The server streams only the board we subscribe to. watchBoard(id) asks for
// another one (the server answers with a fresh "game" snapshot); whenever
// the board list changes we make sure we're still watching a live board. If
// the watched board ended, the list's tick snapshot lets us pick the live
// board with the least progress, avoiding unnecessary board changes. A board
// ending elsewhere only updates the tabs and leaves the current subscription
// alone.
//
// On disconnect we reconnect with a 1s backoff. If a session was previously
// established (we saw at least one init frame) and the socket later opens
// again, we hard-reload the page so any new static assets shipped by a
// redeployed server come into effect.
//
// Depends on: dom.js (updateDom, showShutdownBanner), gameState.js
// (applyMessage, gameState). `updateDom({scoreboard:false})` keeps the
// scoreboard rows and their hover targets intact during tick frames.
// Provides: watchBoard, stepBoard, ensureWatched.

let hadActiveSession = false;
let ws = null;
// Board id we've asked the server for but whose "game" snapshot hasn't
// arrived yet. ensureWatched leaves an in-flight switch alone so a boards
// update can't bounce us back to the first board.
let pendingWatchID = '';

function watchBoard(id, { preserveFollow = false, automatic = false } = {}) {
  if (id && ws && ws.readyState === WebSocket.OPEN) {
    if (!automatic && id !== gameState.game?.id) {
      gameState.lobbyPreference = '';
      gameState.autoLobby = '';
      if (gameState.followName) {
        clearFollow();
        updateDom({ scoreboard: false });
      }
    }
    pendingWatchID = id;
    ws.send(JSON.stringify({ watch: id }));
  }
}

function requestScoreboard(q) {
  if (ws && ws.readyState === WebSocket.OPEN) {
    ws.send(JSON.stringify({ scoreboard: q }));
  }
}

// stepBoard switches to the previous (-1) / next (+1) board, wrapping.
function stepBoard(delta) {
  const ids = gameState.boards.map((b) => b.id);
  if (!ids.length) return;
  const i = ids.indexOf(gameState.game?.id);
  watchBoard(i < 0 ? ids[0] : ids[(i + delta + ids.length) % ids.length]);
}

function lowestTickBoard(preferredLobby = '', excludedLobby = '') {
  let candidates = gameState.boards;
  if (preferredLobby) {
    const sameLobby = gameState.boards.filter((board) => board.lobby === preferredLobby);
    if (!sameLobby.length) return null;
    candidates = sameLobby;
  }
  if (excludedLobby) candidates = candidates.filter((board) => board.lobby !== excludedLobby);
  let best = null;
  let bestTick = Number.MAX_SAFE_INTEGER;
  for (const board of candidates) {
    const tick = Number(board.tick);
    const comparableTick = Number.isFinite(tick) ? tick : Number.MAX_SAFE_INTEGER;
    if (!best || comparableTick < bestTick) {
      best = board;
      bestTick = comparableTick;
    }
  }
  return best;
}

// If the board we're watching is gone (or we never had one), subscribe to
// the followed player's board, otherwise the preferred/sticky lobby's board,
// otherwise the live board with the lowest progress. Called after board-list
// changes.
function lobbyKnown(name) {
  return !name || !gameState.lobbies || gameState.lobbies.includes(name);
}

function ensureWatched({ preserveFollow = false, preferredLobby = '', force = false, excludeLobby = '' } = {}) {
  const ids = gameState.boards.map((b) => b.id);
  const followed = followedBoardID();
  const keepFollow = preserveFollow || !!gameState.followName;
  const requestedLobby = preferredLobby || gameState.autoLobby || gameState.lobbyPreference;

  if (force) {
    const next = lowestTickBoard('', excludeLobby);
    if (next) {
      gameState.autoLobby = '';
      watchBoard(next.id, { preserveFollow: keepFollow, automatic: true });
      return;
    }
  }

  if (followed && pendingWatchID === followed) return;
  if (followed && gameState.game?.id !== followed) {
    watchBoard(followed, { preserveFollow: keepFollow, automatic: true });
    return;
  }
  if (requestedLobby && lobbyKnown(requestedLobby)) {
    const next = lowestTickBoard(requestedLobby);
    if (next && next.id !== gameState.game?.id) {
      watchBoard(next.id, { preserveFollow: keepFollow, automatic: true });
    }
    return;
  }
  if (requestedLobby && !lobbyKnown(requestedLobby)) {
    if (gameState.autoLobby === requestedLobby) gameState.autoLobby = '';
    if (gameState.lobbyPreference === requestedLobby) gameState.lobbyPreference = '';
  }
  if (pendingWatchID && ids.includes(pendingWatchID)) return;
  if (gameState.game && ids.includes(gameState.game.id)) return;
  const next = lowestTickBoard();
  if (next) watchBoard(next.id, { preserveFollow: keepFollow, automatic: true });
}

function followedBoardID() {
  const name = gameState.followName;
  if (!name) return '';
  return gameState.boards.find((b) => (b.names || []).some((candidate) => sameName(candidate, name)))?.id || '';
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
    if (msg.type === 'game' && msg.id === pendingWatchID) pendingWatchID = '';
    const watchedID = gameState.game?.id || '';
    const watchedLobby = msg.type === 'boards'
      ? gameState.boards.find((board) => board.id === watchedID)?.lobby || ''
      : '';
    applyMessage(msg);
    const followedID = followedBoardID();
    const watchedBoardEnded = msg.type === 'boards'
      && watchedID
      && !gameState.boards.some((board) => board.id === watchedID);
    const currentLobbyClosed = msg.type === 'boards'
      && watchedLobby
      && gameState.lobbies
      && !gameState.lobbies.includes(watchedLobby);
    if (watchedBoardEnded) gameState.autoLobby = watchedLobby;
    if (currentLobbyClosed) gameState.lobbyPreference = '';
    // A followed player can appear or move to another board in a later board
    // snapshot. Track that case without making unrelated board updates move
    // the viewer away from its current board.
    const followedBoardChanged = msg.type === 'boards'
      && followedID
      && followedID !== watchedID;
    // A boards update can arrive after init while the first game snapshot is
    // still pending. Keep retrying selection until either a game snapshot or
    // a watch request exists; the viewer should never sit on an empty board.
    const noBoardSelected = !gameState.game?.id && !pendingWatchID;
    const preferredLobbyUpdate = msg.type === 'boards' && !!gameState.lobbyPreference;
    if (msg.type === 'init' || watchedBoardEnded || followedBoardChanged || noBoardSelected || currentLobbyClosed || preferredLobbyUpdate) {
      ensureWatched({
        preserveFollow: followedBoardChanged,
        force: currentLobbyClosed,
        excludeLobby: currentLobbyClosed ? watchedLobby : '',
        preferredLobby: watchedBoardEnded ? watchedLobby : '',
      });
    }
    const scoreboardResponse = msg.type === 'scoreboard';
    updateDom({
      scoreboard: !['tick', 'chat', 'misc'].includes(msg.type),
      renderModal: !scoreboardResponse,
    });
    // A period scoreboard may be the first uncached response and can arrive
    // after a slow cache fill. Render it directly from the state just applied
    // so the open modal never waits for another open or unrelated frame.
    if (scoreboardResponse && !document.getElementById('scoreboard-modal')?.hidden
        && typeof renderScoreboardModalRows === 'function') {
      renderScoreboardModalRows();
    }
  };
  ws.onclose = () => setTimeout(connect, 1000);
  ws.onerror = () => ws.close();
}
connect();
