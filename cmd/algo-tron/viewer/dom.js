// DOM updates triggered by incoming websocket messages. Reads from
// gameState and writes to specific DOM nodes — nothing here mutates game
// state.
//
// Depends on: helpers.js (esc), schemes.js (playerColor), gameState.js.
// Provides: updateDom, showShutdownBanner.

const chatPanel = [];
const chatLast = {};

function updateDom() {
  const game = gameState.serverInfo[0];
  const view = gameState.viewInfo[0];

  // Tagline shows the *viewer* host so users land on the right web URL when
  // they share the line. The TCP game host is shown inside the help modal.
  const addr = document.getElementById('addr');
  if (addr && view) addr.textContent = view.host + ':' + view.port;
  const playAddr = document.getElementById('play-addr');
  if (playAddr && view) playAddr.textContent = view.host + ':' + view.port;

  const modalGame = document.getElementById('modal-game');
  const modalView = document.getElementById('modal-view');
  if (modalGame && game) modalGame.textContent = game.host + ':' + game.port;
  if (modalView && view) modalView.textContent = view.host + ':' + view.port;

  const players = gameState.game ? Object.values(gameState.game.players) : [];
  const alive = players.filter((p) => p.alive).length;
  const aliveEl = document.getElementById('alive-count');
  if (aliveEl) aliveEl.textContent = players.length ? `(${alive}/${players.length} alive)` : '';

  const scoreboardEl = document.getElementById('scoreboard');
  scoreboardEl.innerHTML = gameState.scoreboard.length
    ? gameState.scoreboard.map(scoreRow).join('')
    : '<tr><td colspan="12" class="empty">nobody scored yet :(</td></tr>';

  // Append any new chat lines to the rolling panel. We only render the
  // server's currently-active chats; anything not echoed back has expired.
  for (const p of Object.values(gameState.game?.players || {})) {
    if (!p.chat) {
      chatLast[p.name] = undefined;
      continue;
    }
    if (chatLast[p.name] !== p.chat) {
      chatLast[p.name] = p.chat;
      chatPanel.push({ date: Date.now(), from: p.name, message: p.chat });
      if (chatPanel.length > 30) chatPanel.shift();
    }
  }
  const chat = document.getElementById('chat');
  chat.innerHTML = chatPanel.length
    ? [...chatPanel].reverse().map(chatRow).join('')
    : '<div class="chat-empty">no messages yet</div>';
}

function scoreRow(p, i) {
  const winner = gameState.lastWinners.includes(p.username) ? ' 🎉' : '';
  const wr = (p.winRatio * 100).toFixed(0) + '%';
  const c = playerColor(p.username);
  return '<tr>'
    + '<td class="num">' + (i + 1) + '</td>'
    + '<td class="name" style="color:' + c + '">' + esc(p.username) + winner + '</td>'
    + '<td class="sep">|</td>'
    + '<td class="wr">' + wr + '</td>'
    + '<td class="sep">|</td>'
    + '<td class="elo">' + p.elo.toFixed(0) + '</td>'
    + '<td class="sep">|</td>'
    + '<td class="ts">' + Math.round(p.tsMu) + ' ± ' + Math.round(p.tsSigma) + '</td>'
    + '<td class="sep">|</td>'
    + '<td class="wins">' + p.wins + '</td>'
    + '<td class="sep">|</td>'
    + '<td class="losses">' + p.losses + '</td>'
    + '</tr>';
}

function chatRow(m) {
  const d = new Date(m.date);
  const time = d.toLocaleTimeString();
  const c = playerColor(m.from);
  return '<div class="msg">'
    + '<span class="from" style="color:' + c + '">' + esc(m.from) + '</span>'
    + ' <span class="time">(' + time + ')</span>'
    + '<span class="body">' + esc(m.message) + '</span>'
    + '</div>';
}

function showShutdownBanner(on) {
  const el = document.getElementById('shutdown-banner');
  if (el) el.hidden = !on;
}
