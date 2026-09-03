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

function updateDom({ scoreboard = true } = {}) {
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

  if (scoreboard) renderScoreboardDom();

  const chatPanel = visibleChats();
  const chat = document.getElementById('chat');
  chat.innerHTML = chatPanel.length
    ? [...chatPanel].reverse().map(chatRow).join('')
    : '<div class="chat-empty">no messages yet</div>';
}

function renderScoreboardDom() {
  hideScoreHover();
  const scoreboardEl = document.getElementById('scoreboard');
  const scores = currentScoreboard();
  // Pad every sigma to the widest one so the ± lines up down the ts column
  // (no-break spaces — plain ones would collapse in HTML).
  tsSigmaChars = Math.max(0, ...scores.map((p) => String(Math.round(p.tsSigma)).length));
  scoreboardEl.innerHTML = scores.length
    ? scores.map(scoreRow).join('')
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
        renderScoreName(el);
      });
    }
  }
  if (!document.getElementById('scoreboard-modal')?.hidden && typeof renderScoreboardModalRows === 'function') {
    renderScoreboardModalRows();
  }
}

// Chat is purely client-side state: it stays put across round and board
// changes, capped only by message count (see applyChat's 100-cap).
function visibleChats() {
  return gameState.chatLog.slice(-30);
}

function currentScoreboard() {
  if (gameState.boards.length > 1 && gameState.scoreboardScope === 'board') {
    return gameState.boardScoreboard;
  }
  return gameState.scoreboard;
}

function scoreNameLabel(p) {
  return p.showVersion && p.version ? p.username + '-' + p.version : p.username;
}

function scoreNameMarkup(username, version, showVersion, maxChars) {
  const label = showVersion && version ? username + '-' + version : username;
  const shown = displayName(label, maxChars);
  // If the name is being truncated/scrolled, keep the existing plain-text
  // behavior. The suffix gets its lighter weight whenever the full label fits.
  if (!showVersion || !version || shown !== label) return esc(shown);
  return esc(username) + '<span class="version-tag">-' + esc(version) + '</span>';
}

function renderScoreName(el) {
  const showVersion = el.dataset.showVersion === 'true';
  el.innerHTML = scoreNameMarkup(
    el.dataset.username || el.dataset.name || '',
    el.dataset.version || '',
    showVersion,
    scoreNameChars,
  );
}

let scoreHoverCard = null;
let scoreHoverTarget = null;
let scoreHoverHideTimer = 0;

function formatFirstSeen(value) {
  const millis = Number(value);
  if (!Number.isFinite(millis) || millis <= 0) return '—';
  return new Date(millis).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

function scoreHoverMarkup(target) {
  const username = target.dataset.username || '';
  const version = target.dataset.version || '';
  const contact = target.dataset.contact || '';
  const src = target.dataset.src || '';
  const versionTag = version ? '<span class="score-hover-version">-' + esc(version) + '</span>' : '';
  const srcRow = src
    ? '<div class="score-hover-row"><span class="score-hover-label">src</span><a href="' + esc(src) + '" target="_blank" rel="noopener noreferrer">repository ↗</a></div>'
    : '';
  const contactRow = contact
    ? '<div class="score-hover-row"><span class="score-hover-label">contact</span><span>' + esc(contact) + '</span></div>'
    : '';
  return '<div class="score-hover-title">' + esc(username) + versionTag + '</div>'
    + '<div class="score-hover-row"><span class="score-hover-label">version</span><span>' + esc(version || '—') + '</span></div>'
    + '<div class="score-hover-row"><span class="score-hover-label">first seen</span><span>' + formatFirstSeen(target.dataset.firstSeen) + '</span></div>'
    + contactRow + srcRow;
}

function ensureScoreHoverCard() {
  if (scoreHoverCard) return scoreHoverCard;
  scoreHoverCard = document.createElement('div');
  scoreHoverCard.className = 'score-hover-card';
  scoreHoverCard.hidden = true;
  scoreHoverCard.addEventListener('pointerenter', () => clearTimeout(scoreHoverHideTimer));
  scoreHoverCard.addEventListener('pointerleave', scheduleScoreHoverHide);
  document.body.appendChild(scoreHoverCard);
  return scoreHoverCard;
}

function hideScoreHover() {
  clearTimeout(scoreHoverHideTimer);
  scoreHoverTarget = null;
  if (scoreHoverCard) scoreHoverCard.hidden = true;
}

function scheduleScoreHoverHide() {
  clearTimeout(scoreHoverHideTimer);
  scoreHoverHideTimer = setTimeout(hideScoreHover, 100);
}

function showScoreHover(target) {
  clearTimeout(scoreHoverHideTimer);
  const card = ensureScoreHoverCard();
  scoreHoverTarget = target;
  card.innerHTML = scoreHoverMarkup(target);
  card.hidden = false;
  const rect = target.getBoundingClientRect();
  const gap = 6;
  let left = rect.left;
  let top = rect.bottom + gap;
  if (left + card.offsetWidth > window.innerWidth - 8) left = window.innerWidth - card.offsetWidth - 8;
  if (left < 8) left = 8;
  if (top + card.offsetHeight > window.innerHeight - 8) top = rect.top - card.offsetHeight - gap;
  if (top < 8) top = 8;
  card.style.left = Math.round(left) + 'px';
  card.style.top = Math.round(top) + 'px';
}

function initScoreHover() {
  document.addEventListener('pointerover', (event) => {
    const target = event.target.closest?.('.score-hover-target');
    if (!target || target === scoreHoverTarget) return;
    showScoreHover(target);
  });
  document.addEventListener('pointerout', (event) => {
    const target = event.target.closest?.('.score-hover-target');
    if (!target || target !== scoreHoverTarget) return;
    const related = event.relatedTarget;
    if (related && (target.contains(related) || scoreHoverCard?.contains(related))) return;
    scheduleScoreHoverHide();
  });
}

document.addEventListener('DOMContentLoaded', initScoreHover);

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
  const label = scoreNameLabel(p);
  const contact = p.bio?.contact || '';
  const src = p.bio?.src || '';
  return '<tr>'
    + '<td class="num">' + (i + 1) + '</td>'
    + '<td class="name score-hover-target" style="color:' + c + '" data-username="' + esc(p.username) + '" data-version="' + esc(p.version || '') + '" data-first-seen="' + (p.firstSeen || 0) + '" data-contact="' + esc(contact) + '" data-src="' + esc(src) + '"><span class="namestr" data-name="' + esc(label) + '" data-username="' + esc(p.username) + '" data-version="' + esc(p.version || '') + '" data-show-version="' + (p.showVersion && p.version ? 'true' : 'false') + '">' + scoreNameMarkup(p.username, p.version || '', !!p.showVersion, scoreNameChars) + '</span>' + old + winner + '</td>'
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
  // System notices (e.g. who-won) read as terse info, not chat: one small
  // muted line with no coloured author.
  if (m.system) {
    return '<div class="msg system">'
      + '<span class="body">' + esc(m.message || '') + '</span>'
      + ' <span class="time">(' + time + ')</span>'
      + '</div>';
  }
  const from = m.username || m.from || 'system';
  const c = playerColor(from);
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
