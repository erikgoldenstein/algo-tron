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

// Websocket updates can arrive between pointerdown and click. Keep dynamic
// interactive DOM in place for that short interval so a render cannot detach
// the element the user is currently clicking.
const activePointerIDs = new Set();
const pointerReleaseTimers = new Map();
let pendingDomRender = null;
let pointerSafetyTimer = 0;

function normalizedDomRenderOptions(options = {}) {
  return {
    scoreboard: options.scoreboard !== false,
    renderModal: options.renderModal !== false,
    shell: options.shell !== false,
    stats: options.stats === true,
    chat: options.chat !== false,
  };
}

function deferInteractiveRender(options = {}) {
  if (!activePointerIDs.size) return false;
  const next = normalizedDomRenderOptions(options);
  if (!pendingDomRender) {
    pendingDomRender = next;
  } else {
    // A full render supersedes a partial one while the pointer is held.
    pendingDomRender.scoreboard ||= next.scoreboard;
    pendingDomRender.renderModal ||= next.renderModal;
    pendingDomRender.shell ||= next.shell;
    pendingDomRender.stats ||= next.stats;
    pendingDomRender.chat ||= next.chat;
  }
  return true;
}

function flushDeferredDomRender() {
  if (activePointerIDs.size || !pendingDomRender) return;
  const options = pendingDomRender;
  pendingDomRender = null;
  updateDom(options);
}

function initPointerRenderGuard() {
  document.addEventListener('pointerdown', (event) => {
    const pointerID = event.pointerId ?? 0;
    const previousRelease = pointerReleaseTimers.get(pointerID);
    if (previousRelease) clearTimeout(previousRelease);
    pointerReleaseTimers.delete(pointerID);
    activePointerIDs.add(pointerID);
    clearTimeout(pointerSafetyTimer);
    pointerSafetyTimer = setTimeout(() => {
      activePointerIDs.clear();
      pointerReleaseTimers.clear();
      flushDeferredDomRender();
    }, 5000);
  }, true);

  const releasePointer = (event) => {
    const pointerID = event.pointerId ?? 0;
    // Keep the guard active until the current browser event sequence reaches
    // click. A websocket render must not run in the pointerup→click gap.
    const previousRelease = pointerReleaseTimers.get(pointerID);
    if (previousRelease) clearTimeout(previousRelease);
    const releaseTimer = setTimeout(() => {
      pointerReleaseTimers.delete(pointerID);
      activePointerIDs.delete(pointerID);
      if (!activePointerIDs.size) {
        clearTimeout(pointerSafetyTimer);
        flushDeferredDomRender();
      }
    }, 0);
    pointerReleaseTimers.set(pointerID, releaseTimer);
  };
  document.addEventListener('pointerup', releasePointer, true);
  document.addEventListener('pointercancel', releasePointer, true);
  window.addEventListener('blur', () => {
    activePointerIDs.clear();
    for (const timer of pointerReleaseTimers.values()) clearTimeout(timer);
    pointerReleaseTimers.clear();
    clearTimeout(pointerSafetyTimer);
    flushDeferredDomRender();
  });
}

document.addEventListener('DOMContentLoaded', initPointerRenderGuard);

function updateDom({ scoreboard = true, renderModal = true, shell = true, stats = false, chat = true } = {}) {
  if (deferInteractiveRender({ scoreboard, renderModal, shell, stats, chat })) return;

  if (shell) {
    const game = gameState.serverInfo[0];
    const view = gameState.viewInfo[0];

    // Tagline shows the *viewer* host so users land on the right web URL when
    // they share the line. The TCP game host is shown inside the help modal.
    const addr = document.getElementById('addr');
    if (addr && view) addr.textContent = viewURL(view);

    const modalGame = document.getElementById('modal-game');
    const modalView = document.getElementById('modal-view');
    const commitEl = document.getElementById('deployed-commit');
    if (commitEl) {
      const commit = gameState.buildCommit || 'unknown';
      commitEl.textContent = commit.slice(0, 12);
      commitEl.title = 'build commit ' + commit;
    }
    if (modalGame && game) modalGame.textContent = game.host + ':' + game.port;
    if (modalView && view) modalView.textContent = viewURL(view);
  }

  if (shell || stats) {
    const players = gameState.game ? Object.values(gameState.game.players) : [];
    let playerCount = players.length;
    let alive = players.filter((p) => p.alive).length;
    if (gameState.scoreboardScope === 'global') {
      playerCount = gameState.globalPlayers ?? gameState.boards.reduce((total, board) => total + (Number(board.players) || 0), 0);
      alive = gameState.globalAlive ?? gameState.boards.reduce((total, board) => total + (Number(board.alive) || 0), 0);
    } else if (gameState.scoreboardScope === 'lobby') {
      const stats = gameState.lobbyStats[gameState.scoreboardLobby];
      playerCount = stats?.players || 0;
      alive = stats?.alive || 0;
    }
    const aliveEl = document.getElementById('alive-count');
    if (aliveEl) aliveEl.textContent = playerCount ? `(${alive}/${playerCount} alive)` : '';
  }

  if (shell) {
    updateTabs();
    updateScoreboardTools();
    updateChatTools();
  }

  if (scoreboard) {
    renderScoreboardDom({ renderModal });
    if (typeof updateScorePlotUsers === 'function') updateScorePlotUsers();
  }

  if (chat) {
    const chatPanel = visibleChats();
    const chatEl = document.getElementById('chat');
    chatEl.innerHTML = chatPanel.length
      ? [...chatPanel].reverse().map(chatRow).join('')
      : '<div class="chat-empty">no messages yet</div>';
  }
}

function renderScoreboardDom({ renderModal = true } = {}) {
  const scoreboardEl = document.getElementById('scoreboard');
  const scores = currentScoreboard();
  // Pad every sigma to the widest one so the ± lines up down the ts column
  // (no-break spaces — plain ones would collapse in HTML).
  tsSigmaChars = Math.max(0, ...scores.map((p) => String(Math.round(p.tsSigma)).length));
  if (scores.length) {
    // Keep row nodes alive across rank/data changes. Replacing innerHTML here
    // made the board disappear during the end→next-game handoff and also
    // reset the browser's layout for every scoreboard refresh.
    scoreboardEl.querySelector('tr.empty')?.remove();
    const rowsByKey = new Map(
      [...scoreboardEl.querySelectorAll('tr[data-score-key]')]
        .map((row) => [row.dataset.scoreKey, row]),
    );
    const firstTops = new Map(
      [...rowsByKey.values()].map((row) => [row.dataset.scoreKey, row.getBoundingClientRect().top]),
    );
    const usedKeys = new Set();

    for (let i = 0; i < scores.length; i++) {
      const score = scores[i];
      const key = scoreRowKey(score);
      let row = rowsByKey.get(key);
      if (!row) {
        row = createScoreRow(score, i);
      } else {
        updateScoreRow(row, score, i);
      }
      usedKeys.add(key);
      // appendChild moves an existing row without destroying it, so rank
      // changes only adjust its position in the table.
      scoreboardEl.appendChild(row);
    }
    for (const [key, row] of rowsByKey) {
      if (!usedKeys.has(key)) row.remove();
    }

    // FLIP the rows that changed rank. The transform is cleared on the next
    // frame, allowing the existing rows to glide to their new positions.
    for (const row of scoreboardEl.querySelectorAll('tr[data-score-key]')) {
      const first = firstTops.get(row.dataset.scoreKey);
      if (first === undefined) continue;
      const delta = first - row.getBoundingClientRect().top;
      if (!delta) continue;
      row.style.transform = 'translateY(' + delta + 'px)';
      requestAnimationFrame(() => {
        row.style.transform = '';
      });
    }
  } else if (!scoreboardEl.querySelector('tr.empty')) {
    scoreboardEl.innerHTML = '<tr class="empty"><td colspan="13" class="empty">nobody scored yet :(</td></tr>';
  }
  if (typeof bindScoreFollowTargets === 'function') bindScoreFollowTargets(scoreboardEl);

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
  if (renderModal && !document.getElementById('scoreboard-modal')?.hidden && typeof renderScoreboardModalRows === 'function') {
    renderScoreboardModalRows();
  }
  restoreScoreHover();
}

// Chat is purely client-side state: it stays put across round and board
// changes, capped only by message count (see applyChat's 100-cap).
function visibleChats() {
  let chats = gameState.chatLog;
  if (gameState.chatScope === 'board') {
    chats = chats.filter((m) => (m.system && !m.gameId) || (m.gameId && m.gameId === gameState.game?.id));
  } else if (gameState.chatScope === 'lobby') {
    chats = chats.filter((m) => m.lobby === gameState.chatLobby);
  }
  return chats.slice(-30);
}

function currentScoreboard() {
  if (gameState.scoreboardScope === 'board') {
    return gameState.boardScoreboard.slice(0, gameState.boardScoreboardVisible || 10);
  }
  if (gameState.scoreboardScope === 'lobby') {
    return gameState.lobbyScoreboards[gameState.scoreboardLobby] || [];
  }
  return gameState.scoreboard;
}

function scoreNameLabel(p) {
  return p.showVersion && p.version ? p.username + '-' + p.version : p.username;
}

function scoreRowKey(p) {
  // UUID is deliberately not sent over the wire. JSON keeps the fallback
  // key unambiguous and safe for a data-* attribute, including unusual names.
  return JSON.stringify([p.username || '', p.version || '']);
}

function createScoreRow(p, i) {
  const holder = document.createElement('tbody');
  holder.innerHTML = scoreRow(p, i);
  return holder.firstElementChild;
}

function updateScoreRow(row, p, i) {
  const holder = createScoreRow(p, i);
  row.className = holder.className;
  row.replaceChildren(...holder.children);
}

function scoreNameMarkup(username, version, showVersion, maxChars) {
  const label = showVersion && version ? username + '-' + version : username;
  const shown = displayName(label, maxChars);
  // If the name is being truncated/scrolled, keep the existing plain-text
  // behavior. The suffix gets its lighter weight whenever the full label fits.
  if (!showVersion || !version || shown !== label) return esc(shown);
  return esc(username) + '<span class="version-tag">-' + esc(version) + '</span>';
}

function scoreInfoTrigger(target) {
  return target?.closest('tr')?.querySelector('.score-info-button') || null;
}

function renderScoreName(el) {
  const showVersion = el.dataset.showVersion === 'true';
  const nameChars = Number(el.dataset.nameChars) || scoreNameChars;
  el.innerHTML = scoreNameMarkup(
    el.dataset.username || el.dataset.name || '',
    el.dataset.version || '',
    showVersion,
    nameChars,
  );
}

let scoreHoverCard = null;
let scoreHoverTarget = null;
let scoreHoverKey = '';
let scoreHoverTargetHovered = false;
let scoreHoverCardHovered = false;
let scoreHoverHideTimer = 0;
let scoreHoverTouchOpen = false;
let scoreHoverTouchTrigger = null;

function isScoreHoverPointer(event) {
  return event.pointerType === 'mouse' || event.pointerType === 'pen';
}
let forwardConfirmUrl = '';

function hideForwardConfirm() {
  const modal = document.getElementById('forward-confirm-modal');
  if (modal) modal.hidden = true;
  forwardConfirmUrl = '';
}

function openForwardConfirmUrl() {
  const url = forwardConfirmUrl;
  hideForwardConfirm();
  if (url) window.open(url, '_blank', 'noopener,noreferrer');
}

function showForwardConfirm(url) {
  const modal = document.getElementById('forward-confirm-modal');
  const urlEl = document.getElementById('forward-confirm-url');
  if (!modal || !urlEl) {
    window.open(url, '_blank', 'noopener,noreferrer');
    return;
  }
  forwardConfirmUrl = url;
  urlEl.textContent = url;
  modal.hidden = false;
  document.getElementById('forward-confirm-open')?.focus();
}

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
  const versionRow = version
    ? '<div class="score-hover-row"><span class="score-hover-label">version</span><span>' + esc(version) + '</span></div>'
    : '';
  let sourceValue = '<span>' + esc(src) + '</span>';
  try {
    const sourceURL = new URL(src);
    if (sourceURL.protocol === 'http:' || sourceURL.protocol === 'https:') {
      sourceValue = '<a class="score-hover-src" href="' + esc(src) + '" target="_blank" rel="noopener noreferrer">repository ↗</a>';
    }
  } catch (e) {}
  const srcRow = src
    ? '<div class="score-hover-row"><span class="score-hover-label">src</span>' + sourceValue + '</div>'
    : '';
  const contactRow = contact
    ? '<div class="score-hover-row"><span class="score-hover-label">contact</span><span>' + esc(contact) + '</span></div>'
    : '';
  const resetRow = typeof adminSessionActive !== 'undefined' && adminSessionActive && target.dataset.oldOwner !== 'true'
    ? '<div class="score-hover-reset-row"><button type="button" class="score-hover-reset">reset password</button></div>'
    : '';
  return '<div class="score-hover-title">' + esc(username) + versionTag + '</div>'
    + versionRow
    + '<div class="score-hover-row"><span class="score-hover-label">first seen</span><span>' + formatFirstSeen(target.dataset.firstSeen) + '</span></div>'
    + contactRow + srcRow + resetRow;
}

function ensureScoreHoverCard() {
  if (scoreHoverCard) return scoreHoverCard;
  scoreHoverCard = document.createElement('div');
  scoreHoverCard.className = 'score-hover-card';
  scoreHoverCard.id = 'score-player-details';
  scoreHoverCard.hidden = true;
  scoreHoverCard.addEventListener('pointerenter', () => {
    if (scoreHoverTouchOpen) return;
    scoreHoverCardHovered = true;
    clearTimeout(scoreHoverHideTimer);
    scoreHoverHideTimer = 0;
  });
  scoreHoverCard.addEventListener('pointerleave', () => {
    if (scoreHoverTouchOpen) return;
    scoreHoverCardHovered = false;
    scheduleScoreHoverHide();
  });
  document.body.appendChild(scoreHoverCard);
  return scoreHoverCard;
}

function scoreHoverIdentity(target) {
  if (!target) return '';
  return (target.dataset.username || target.dataset.name || '') + '\u0000' + (target.dataset.version || '');
}

function findScoreHoverTarget() {
  if (!scoreHoverKey) return null;
  for (const target of document.querySelectorAll('.score-hover-target')) {
    if (scoreHoverIdentity(target) === scoreHoverKey) return target;
  }
  return null;
}

function hideScoreHover({ restoreFocus = false } = {}) {
  clearTimeout(scoreHoverHideTimer);
  scoreHoverHideTimer = 0;
  const trigger = scoreHoverTouchTrigger;
  if (trigger?.isConnected) trigger.setAttribute('aria-expanded', 'false');
  scoreHoverTarget = null;
  scoreHoverKey = '';
  scoreHoverTargetHovered = false;
  scoreHoverCardHovered = false;
  scoreHoverTouchOpen = false;
  scoreHoverTouchTrigger = null;
  if (scoreHoverCard) scoreHoverCard.hidden = true;
  if (restoreFocus && trigger?.isConnected) trigger.focus();
}

function scheduleScoreHoverHide() {
  clearTimeout(scoreHoverHideTimer);
  scoreHoverHideTimer = setTimeout(() => {
    scoreHoverHideTimer = 0;
    hideScoreHover();
  }, 100);
}

function showScoreHover(target, { touch = false, trigger = null } = {}) {
  clearTimeout(scoreHoverHideTimer);
  scoreHoverHideTimer = 0;
  if (scoreHoverTouchTrigger && scoreHoverTouchTrigger !== trigger && scoreHoverTouchTrigger.isConnected) {
    scoreHoverTouchTrigger.setAttribute('aria-expanded', 'false');
  }
  const card = ensureScoreHoverCard();
  scoreHoverTouchOpen = touch;
  scoreHoverTouchTrigger = touch ? trigger : null;
  if (touch && trigger) {
    trigger.setAttribute('aria-expanded', 'true');
    trigger.setAttribute('aria-controls', 'score-player-details');
  }
  scoreHoverTarget = target;
  scoreHoverKey = scoreHoverIdentity(target);
  card.classList.toggle('touch-open', touch);
  if (touch) {
    card.setAttribute('role', 'dialog');
    card.setAttribute('aria-label', 'player details for ' + (target.dataset.username || target.dataset.name || 'player'));
  } else {
    card.removeAttribute('role');
    card.removeAttribute('aria-label');
  }
  card.innerHTML = scoreHoverMarkup(target)
    + (touch ? '<button type="button" class="score-hover-close" aria-label="close player details">×</button>' : '');
  card.querySelector('.score-hover-close')?.addEventListener('click', () => hideScoreHover({ restoreFocus: true }));
  card.querySelector('.score-hover-reset')?.addEventListener('click', (event) => {
    event.stopPropagation();
    if (typeof resetAdminUserPassword === 'function') resetAdminUserPassword(target.dataset.username || '', card);
  });
  card.querySelector('.score-hover-src')?.addEventListener('click', (event) => {
    if (!getSwitch('confirmForwarding')) return;
    event.preventDefault();
    event.stopPropagation();
    const url = event.currentTarget.getAttribute('href') || '';
    hideScoreHover();
    showForwardConfirm(url);
  });
  card.hidden = false;
  // The hover target is the rendered name span, so the card follows the
  // visible username rather than the wider table cell around it.
  const anchor = target.querySelector('.namestr') || target;
  const rect = anchor.getBoundingClientRect();
  const gap = 6;
  const margin = 8;
  if (touch) {
    // A persistent touch card is easier to use as a bottom sheet than as a
    // small hover tooltip positioned beside a narrow table row.
    card.style.left = margin + 'px';
    card.style.right = margin + 'px';
    card.style.top = 'auto';
    card.style.bottom = margin + 'px';
  } else {
    const cardWidth = card.offsetWidth;
    const cardHeight = card.offsetHeight;
    // Keep the bio card beside the username. If the target is near the right
    // edge, use the space on its left rather than dropping the card below it.
    let left = rect.right + gap;
    if (left + cardWidth > window.innerWidth - margin) {
      left = rect.left - cardWidth - gap;
    }
    if (left < margin) left = margin;
    let top = rect.top;
    if (top + cardHeight > window.innerHeight - margin) top = window.innerHeight - cardHeight - margin;
    if (top < margin) top = margin;
    card.style.left = Math.round(left) + 'px';
    card.style.right = '';
    card.style.top = Math.round(top) + 'px';
    card.style.bottom = '';
  }
}

function restoreScoreHover() {
  if (!scoreHoverKey || !scoreHoverCard || scoreHoverCard.hidden) return;
  if (scoreHoverTouchOpen) {
    const target = findScoreHoverTarget();
    const trigger = scoreInfoTrigger(target);
    if (target) showScoreHover(target, { touch: true, trigger });
    else hideScoreHover();
    return;
  }
  // If the pointer has already left both the row and the card, let the
  // existing delayed hide finish instead of reviving the card on refresh.
  if (!scoreHoverTargetHovered && !scoreHoverCardHovered) return;
  const target = findScoreHoverTarget();
  if (target) {
    showScoreHover(target);
  } else {
    hideScoreHover();
  }
}

function refreshScoreHoverCard() {
  if (!scoreHoverKey || !scoreHoverCard || scoreHoverCard.hidden) return;
  const target = findScoreHoverTarget();
  const trigger = scoreInfoTrigger(target);
  if (target) showScoreHover(target, { touch: scoreHoverTouchOpen, trigger });
  else hideScoreHover();
}

function initScoreHover() {
  document.addEventListener('pointerover', (event) => {
    if (!isScoreHoverPointer(event)) return;
    const target = event.target.closest?.('.score-hover-target');
    if (!target) return;
    if (scoreHoverTouchTrigger?.isConnected) scoreHoverTouchTrigger.setAttribute('aria-expanded', 'false');
    scoreHoverTouchOpen = false;
    scoreHoverTouchTrigger = null;
    scoreHoverTargetHovered = true;
    if (target === scoreHoverTarget) return;
    showScoreHover(target);
  });
  document.addEventListener('pointerout', (event) => {
    if (!isScoreHoverPointer(event)) return;
    const target = event.target.closest?.('.score-hover-target');
    if (!target || target !== scoreHoverTarget) return;
    const related = event.relatedTarget;
    if (related && target.contains(related)) return;
    scoreHoverTargetHovered = false;
    if (related && scoreHoverCard?.contains(related)) return;
    scheduleScoreHoverHide();
  });
  document.addEventListener('click', (event) => {
    const info = event.target.closest?.('.score-info-button');
    if (info) {
      const target = info.closest('tr')?.querySelector('.score-hover-target');
      if (!target) return;
      event.preventDefault();
      event.stopPropagation();
      if (scoreHoverTouchOpen && target === scoreHoverTarget) {
        hideScoreHover({ restoreFocus: true });
      } else {
        showScoreHover(target, { touch: true, trigger: info });
      }
      return;
    }
    if (!scoreHoverTouchOpen || scoreHoverCard?.hidden) return;
    if (scoreHoverCard?.contains(event.target)) return;
    hideScoreHover();
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && scoreHoverTouchOpen) hideScoreHover({ restoreFocus: true });
  });
}

function initForwardConfirm() {
  document.querySelectorAll('[data-forward-close]').forEach((el) => {
    el.addEventListener('click', hideForwardConfirm);
  });
  document.getElementById('forward-confirm-cancel')?.addEventListener('click', hideForwardConfirm);
  document.getElementById('forward-confirm-open')?.addEventListener('click', openForwardConfirmUrl);
}

document.addEventListener('DOMContentLoaded', () => {
  initScoreHover();
  initForwardConfirm();
});

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
  renderScopeOptions(el, gameState.scoreboardScope);
  setActiveScopeOption(el, gameState.scoreboardScope);
  el.querySelectorAll('.scope-option').forEach((btn) => {
    btn.onclick = () => {
      const lobby = btn.dataset.scope === 'lobby' ? watchedLobby() : '';
      if (gameState.scoreboardScope === btn.dataset.scope && gameState.scoreboardLobby === lobby) return;
      gameState.scoreboardScope = btn.dataset.scope;
      gameState.scoreboardLobby = lobby;
      setActiveScopeOption(el, gameState.scoreboardScope);
      globalThis.viewerStore?.publish('scope', { dom: { scoreboard: true } });
      if (typeof requestViewerSubscription === 'function') requestViewerSubscription();
    };
  });
}

function updateChatTools() {
  const tools = document.getElementById('chat-tools');
  if (!tools) return;
  tools.hidden = gameState.boards.length <= 1;
  if (tools.hidden) return;
  const scope = document.getElementById('chat-scope');
  if (!scope) return;
  renderScopeOptions(scope, gameState.chatScope);
  setActiveScopeOption(scope, gameState.chatScope);
  scope.querySelectorAll('.scope-option').forEach((btn) => {
    btn.onclick = () => {
      const lobby = btn.dataset.scope === 'lobby' ? watchedLobby() : '';
      if (gameState.chatScope === btn.dataset.scope && gameState.chatLobby === lobby) return;
      gameState.chatScope = btn.dataset.scope;
      gameState.chatLobby = lobby;
      setActiveScopeOption(scope, gameState.chatScope);
      globalThis.viewerStore?.publish('scope', { dom: { scoreboard: false } });
      if (typeof requestViewerSubscription === 'function') requestViewerSubscription();
    };
  });
}

function setActiveScopeOption(root, selectedScope) {
  root.querySelectorAll('.scope-option').forEach((option) => {
    option.classList.toggle('active', option.dataset.scope === selectedScope);
  });
}

function renderScopeOptions(root, selectedScope) {
  const lobbySignature = gameState.lobbies && gameState.lobbies.length > 1 ? 'lobby' : '';
  if (root.dataset.scopeLobbies === lobbySignature) return;
  const options = [
    { scope: 'board', label: 'board' },
    { scope: 'global', label: 'global' },
  ];
  if (gameState.lobbies && gameState.lobbies.length > 1) options.push({ scope: 'lobby', label: 'lobby' });
  root.dataset.scopeLobbies = lobbySignature;
  root.innerHTML = '(' + options.map((option, index) => {
    const active = option.scope === selectedScope;
    const separator = index ? ' <span>/</span> ' : '';
    return separator + '<button class="scope-option' + (active ? ' active' : '') + '" data-scope="' + option.scope + '">' + esc(option.label) + '</button>';
  }).join('') + ')';
}

// One tmux-style tab per running board; the subscribed one carries the `*`.
// Click a tab (or use h / l / 1…9, wired in modal.js) to switch — switching
// just asks the server for that board's stream via watchBoard (ws.js).
function updateTabs() {
  const tabsEl = document.getElementById('tabs');
  if (!tabsEl) return;
  const current = gameState.game?.id;
  const boards = orderedBoards();
  tabsEl.innerHTML = boards.length
    ? boards.map((b, i) => {
        const active = b.id === current;
        const label = b.label || ('board-' + (i + 1));
        return `<span class="tab${active ? ' active' : ''}" data-id="${esc(b.id)}">${i + 1}:${esc(label)}${active ? '*' : ''}</span>`;
      }).join('')
    : '<span class="tab">no games</span>';
  tabsEl.querySelectorAll('.tab[data-id]').forEach((el) => {
    el.addEventListener('click', () => watchBoard(el.dataset.id));
  });
}

function scoreRow(p, i, includeInfo = true, nameChars = scoreNameChars) {
  const winner = gameState.lastWinners.includes(p.username) ? ' 🎉' : '';
  const old = p.oldOwner ? '<span class="old">(old owner' + p.oldOwner + ')</span>' : '';
  const wr = (p.winRatio * 100).toFixed(0) + '%';
  const c = playerColor(p.username);
  const label = scoreNameLabel(p);
  const followed = sameName(label, gameState.followName);
  const followedDead = followed && p.online !== false && !followNameIsAlive(label);
  const contact = p.bio?.contact || '';
  const src = p.bio?.src || '';
  const nameCharsData = nameChars > 0 ? String(nameChars) : '';
  const info = includeInfo
    ? '<td class="info"><button type="button" class="score-info-button" aria-label="show details for ' + esc(label) + '" aria-expanded="false" aria-controls="score-player-details">[i]</button></td>'
    : '';
  return '<tr data-score-key="' + esc(scoreRowKey(p)) + '"' + (followed ? ' class="followed"' : '') + '>'
    + '<td class="num">' + (i + 1) + '</td>'
    + '<td class="name" style="color:' + c + '"><span class="namestr score-hover-target score-follow-target" data-follow-name="' + esc(label) + '" data-name="' + esc(label) + '" data-username="' + esc(p.username) + '" data-version="' + esc(p.version || '') + '" data-show-version="' + (p.showVersion && p.version ? 'true' : 'false') + '" data-name-chars="' + nameCharsData + '" data-first-seen="' + (p.firstSeen || 0) + '" data-contact="' + esc(contact) + '" data-src="' + esc(src) + '" data-old-owner="' + (p.oldOwner ? 'true' : 'false') + '">' + scoreNameMarkup(p.username, p.version || '', !!p.showVersion, nameChars) + '</span>' + (followedDead ? ' <span class="follow-status">(currently dead)</span>' : '') + old + winner + '</td>'
    + info
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
    + '<span class="body">: ' + esc(m.message || '') + '</span>'
    + '</div>';
}

function showShutdownBanner(on) {
  const el = document.getElementById('shutdown-banner');
  if (el) el.hidden = !on;
}
