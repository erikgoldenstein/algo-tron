// DOM updates triggered by incoming websocket messages. Reads from
// gameState and writes to specific DOM nodes — nothing here mutates game
// state.
//
// Depends on: helpers.js (esc, viewURL), schemes.js (playerColor), gameState.js,
// dom_follow.js (updateFollowPlayer).
// Provides: updateDom, showShutdownBanner.

// Last measured character capacity of the scoreboard name column. Used by
// the row renderer and by the scrolling tick in render.js. Updated after
// each render once the cell has a layout, and on window resize.
let scoreNameChars = 0;

// Width (in digits) of the largest sigma on the current scoreboard; set in
// updateDom before the rows render.
let tsSigmaChars = 0;

function updateDom() {
  const game = gameState.serverInfo[0];
  const view = gameState.viewInfo[0];

  // Tagline shows the *viewer* host so users land on the right web URL when
  // they share the line. The TCP game host is shown inside the help modal.
  const addr = document.getElementById('addr');
  if (addr && view) addr.textContent = viewURL(view);

  const modalGame = document.getElementById('modal-game');
  const modalView = document.getElementById('modal-view');
  if (modalGame && game) modalGame.textContent = game.host + ':' + game.port;
  if (modalView && view) modalView.textContent = viewURL(view);

  const players = gameState.game ? Object.values(gameState.game.players) : [];
  const alive = players.filter((p) => p.alive).length;
  const aliveEl = document.getElementById('alive-count');
  if (aliveEl) aliveEl.textContent = players.length ? `(${alive}/${players.length} alive)` : '';

  updateTabs();
  updateScoreboardTools();

  const scoreboardEl = document.getElementById('scoreboard');
  const scoreboard = currentScoreboard();
  // Pad every sigma to the widest one so the ± lines up down the ts column
  // (no-break spaces — plain ones would collapse in HTML).
  tsSigmaChars = Math.max(0, ...scoreboard.map((p) => String(Math.round(p.tsSigma)).length));
  scoreboardEl.innerHTML = scoreboard.length
    ? scoreboard.map(scoreRow).join('')
    : '<tr><td colspan="12" class="empty">nobody scored yet :(</td></tr>';

  // The name cell now exists in the DOM, so we can measure its actual width
  // and reflow the labels if the available space differs from what we used
  // when building the row above.
  const firstNameCell = scoreboardEl.querySelector('td.name');
  if (firstNameCell) {
    // Reserve 2 chars for the trailing " 🎉" winner marker so it doesn't
    // get visually clipped by the cell's overflow:hidden.
    const cap = Math.max(0, fitChars(firstNameCell) - 2);
    if (cap !== scoreNameChars) {
      scoreNameChars = cap;
      scoreboardEl.querySelectorAll('.namestr').forEach((el) => {
        el.textContent = displayName(el.dataset.name, scoreNameChars);
      });
    }
  }

  const chatPanel = visibleChats();
  const chat = document.getElementById('chat');
  chat.innerHTML = chatPanel.length
    ? [...chatPanel].reverse().map(chatRow).join('')
    : '<div class="chat-empty">no messages yet</div>';
  if (!document.getElementById('scoreboard-modal')?.hidden && typeof renderScoreboardModalRows === 'function') {
    renderScoreboardModalRows();
  }
}

function visibleChats() {
  if (gameState.scoreboardScope === 'board' && gameState.game?.id) {
    return gameState.chatLog.filter((m) => !m.gameId || m.gameId === gameState.game.id).slice(-30);
  }
  return gameState.chatLog.slice(-30);
}

function currentScoreboard() {
  if (gameState.boards.length > 1 && gameState.scoreboardScope === 'board') {
    return gameState.boardScoreboard;
  }
  return gameState.scoreboard;
}

function updateScoreboardTools() {
  const tools = document.getElementById('scoreboard-tools');
  if (!tools) return;
  tools.hidden = gameState.boards.length <= 1;
  if (tools.hidden) return;
  updateScoreboardScope();
  updateFollowPlayer();
}

function updateScoreboardScope() {
  const el = document.getElementById('scoreboard-scope');
  if (!el) return;
  el.querySelectorAll('.scope-option').forEach((btn) => {
    btn.classList.toggle('active', btn.dataset.scope === gameState.scoreboardScope);
    btn.onclick = () => {
      if (gameState.scoreboardScope === btn.dataset.scope) return;
      gameState.scoreboardScope = btn.dataset.scope;
      updateDom();
    };
  });
}

// One tmux-style tab per running board; the subscribed one carries the `*`.
// Click a tab (or use h / l / 1…9, wired in modal.js) to switch — switching
// just asks the server for that board's stream via watchBoard (ws.js).
function updateTabs() {
  const tabsEl = document.getElementById('tabs');
  if (!tabsEl) return;
  const current = gameState.game?.id;
  tabsEl.innerHTML = gameState.boards.length
    ? gameState.boards.map((b, i) => {
        const active = b.id === current;
        return `<span class="tab${active ? ' active' : ''}" data-id="${esc(b.id)}">${i + 1}:board-${i + 1}${active ? '*' : ''}</span>`;
      }).join('')
    : '<span class="tab">no games</span>';
  tabsEl.querySelectorAll('.tab[data-id]').forEach((el) => {
    el.addEventListener('click', () => watchBoard(el.dataset.id));
  });
}

function scoreRow(p, i) {
  const winner = gameState.lastWinners.includes(p.username) ? ' 🎉' : '';
  const old = p.oldOwner ? '<span class="old">(old owner' + p.oldOwner + ')</span>' : '';
  const wr = (p.winRatio * 100).toFixed(0) + '%';
  const c = playerColor(p.username);
  return '<tr>'
    + '<td class="num">' + (i + 1) + '</td>'
    + '<td class="name" style="color:' + c + '"><span class="namestr" data-name="' + esc(p.username) + '">' + esc(displayName(p.username, scoreNameChars)) + '</span>' + old + winner + '</td>'
    + '<td class="sep">|</td>'
    + '<td class="ts">' + Math.round(p.tsMu) + ' ± ' + String(Math.round(p.tsSigma)).padStart(tsSigmaChars, '\u00a0') + '</td>'
    + '<td class="sep">|</td>'
    + '<td class="wr">' + wr + '</td>'
    + '<td class="sep">|</td>'
    + '<td class="elo">' + p.elo.toFixed(0) + '</td>'
    + '<td class="sep">|</td>'
    + '<td class="wins">' + p.wins + '</td>'
    + '<td class="sep">|</td>'
    + '<td class="losses">' + p.losses + '</td>'
    + '</tr>';
}

function chatRow(m) {
  const d = new Date(m.time || Date.now());
  const time = d.toLocaleTimeString();
  const from = m.username || m.from || 'system';
  const c = m.system ? 'var(--text-muted)' : playerColor(from);
  return '<div class="msg">'
    + '<span class="from" style="color:' + c + '">' + esc(from) + '</span>'
    + ' <span class="time">(' + time + ')</span>'
    + '<span class="body">' + esc(m.message || '') + '</span>'
    + '</div>';
}

function showShutdownBanner(on) {
  const el = document.getElementById('shutdown-banner');
  if (el) el.hidden = !on;
}
