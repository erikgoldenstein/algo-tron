// Help/settings modal: opens via the "settings" button or "?" key, closes
// with q/Esc. Also wires the keyboard shortcuts shown in the modal's "keys"
// section (c cycles colorscheme, z toggles low-fatigue mode, h/l and 1…9
// switch boards).
//
// Depends on: schemes.js (SCHEMES, SCHEME_KEYS, applyScheme, currentScheme),
// ws.js (watchBoard, stepBoard — resolved at event time), dom_follow.js
// (setFollowName, stepFollow), gameState.js.
// Provides: toggleHelp, cycleScheme.

function renderSchemes() {
  const root = document.getElementById('schemes');
  if (!root) return;
  root.innerHTML = SCHEME_KEYS.map((key) => {
    const s = SCHEMES[key];
    const swatches = s.players.map((c) => `<span class="swatch" style="background:${c}"></span>`).join('');
    const font = s.font ? `--scheme-font:${s.font};` : '';
    return `<button class="scheme${key === currentScheme ? ' active' : ''}" data-scheme="${key}"
        style="--scheme-bg:${s.bgElevated};--scheme-text:${s.text};${font}">
        <span class="swatches">${swatches}</span>
        <span class="scheme-name">${s.label}</span>
      </button>`;
  }).join('');
  root.querySelectorAll('.scheme').forEach((btn) => {
    btn.addEventListener('click', () => applyScheme(btn.dataset.scheme));
  });
}

// Boolean settings rendered as ASCII [x] / [ ] toggles in the options
// section. Read by other modules via getSwitch(key) in helpers.js.
const SWITCHES = [
  { key: 'scrollNames', label: 'scroll long names' },
];

function renderSwitches() {
  const root = document.getElementById('switches');
  if (!root) return;
  root.innerHTML = SWITCHES.map((s) => {
    const on = getSwitch(s.key);
    return `<span class="switch" data-key="${s.key}">`
      + `<span class="switch-box">[${on ? 'x' : ' '}]</span>`
      + `<span class="switch-label">${s.label}</span>`
      + `</span>`;
  }).join('');
  root.querySelectorAll('[data-key]').forEach((el) => {
    el.addEventListener('click', () => {
      const k = el.dataset.key;
      const next = !getSwitch(k);
      try { localStorage.setItem('algotron.switch.' + k, next ? '1' : '0'); } catch (e) {}
      renderSwitches();
    });
  });
}

function toggleHelp(force) {
  const m = document.getElementById('help-modal');
  if (!m) return;
  const shouldShow = force === undefined ? m.hidden : force;
  m.hidden = !shouldShow;
  if (shouldShow) { renderSchemes(); renderSwitches(); }
}

function cycleScheme() {
  const i = SCHEME_KEYS.indexOf(currentScheme);
  const next = SCHEME_KEYS[(i + 1) % SCHEME_KEYS.length];
  applyScheme(next);
  renderSchemes();
}

function scoreModalQuery(offset) {
  return {
    period: document.getElementById('scoreboard-period')?.dataset.value || 'online',
    sort: document.getElementById('scoreboard-sort')?.dataset.value || 'ts',
    search: document.getElementById('scoreboard-search')?.value || '',
    offset: offset || 0,
    limit: 25,
  };
}

function closeAppSelect(root) {
  root.classList.remove('open');
  const list = root.querySelector('.app-select-list');
  if (list) list.hidden = true;
}

// Custom dropdown: native <select> popups can't be themed on macOS, so we
// drive an app-styled list ourselves. The chosen value lives in the wrapper's
// data-value; onChange fires after each pick.
function initAppSelect(id, onChange) {
  const root = document.getElementById(id);
  if (!root) return;
  const list = root.querySelector('.app-select-list');
  const button = root.querySelector('.app-select-btn');
  const valueEl = root.querySelector('.app-select-value');
  if (!list || !button) return;
  const sync = () => {
    list.querySelectorAll('button').forEach((b) => {
      b.classList.toggle('selected', b.dataset.value === root.dataset.value);
    });
  };
  sync();
  button.addEventListener('click', (e) => {
    e.stopPropagation();
    const open = root.classList.toggle('open');
    list.hidden = !open;
    if (open) document.querySelectorAll('.app-select.open').forEach((o) => { if (o !== root) closeAppSelect(o); });
  });
  list.querySelectorAll('button').forEach((btn) => {
    btn.addEventListener('click', () => {
      root.dataset.value = btn.dataset.value;
      if (valueEl) valueEl.textContent = btn.textContent;
      sync();
      closeAppSelect(root);
      onChange();
    });
  });
}

function renderScoreboardModalRows() {
  const root = document.getElementById('scoreboard-modal-rows');
  if (!root) return;
  const q = scoreModalQuery(0);
  const key = scorePageKey(q.period, q.sort, q.search);
  const page = gameState.scorePages[key];
  const rows = page?.entries || [];
  root.innerHTML = rows.length
    ? rows.map(scoreRow).join('')
    : '<tr><td colspan="12" class="empty">nobody found</td></tr>';
  const asof = document.getElementById('scoreboard-asof');
  if (asof) asof.textContent = page?.computedAt ? 'as of ' + new Date(page.computedAt).toLocaleString() : '';
}

function openScoreboardModal() {
  const m = document.getElementById('scoreboard-modal');
  if (!m) return;
  m.hidden = false;
  requestScoreboard(scoreModalQuery(0));
  renderScoreboardModalRows();
}

function closeScoreboardModal() {
  const m = document.getElementById('scoreboard-modal');
  if (m) m.hidden = true;
  document.querySelectorAll('#scoreboard-modal .app-select.open').forEach(closeAppSelect);
}

function loadMoreSidebarScores() {
  if (!matchMedia('(min-width: 801px)').matches) return;
  const key = scorePageKey('online', 'ts', '');
  const page = gameState.scorePages[key];
  if (page && !page.hasMore) return;
  requestScoreboard({ period: 'online', sort: 'ts', search: '', offset: gameState.scoreboard.length, limit: 25 });
}

function loadMoreModalScores() {
  const q = scoreModalQuery(0);
  const key = scorePageKey(q.period, q.sort, q.search);
  const page = gameState.scorePages[key];
  if (!page?.hasMore) return;
  requestScoreboard({ period: q.period, sort: q.sort, search: q.search, offset: page.entries.length, limit: 25 });
}

document.addEventListener('DOMContentLoaded', () => {
  document.getElementById('help-btn')?.addEventListener('click', () => toggleHelp(true));
  document.getElementById('scoreboard-title')?.addEventListener('click', openScoreboardModal);
  document.querySelectorAll('[data-scoreboard-close]').forEach((el) => el.addEventListener('click', closeScoreboardModal));
  const refreshScoreboard = () => requestScoreboard(scoreModalQuery(0));
  initAppSelect('scoreboard-period', refreshScoreboard);
  initAppSelect('scoreboard-sort', refreshScoreboard);
  document.addEventListener('click', () => document.querySelectorAll('.app-select.open').forEach(closeAppSelect));
  document.getElementById('scoreboard-search')?.addEventListener('input', () => requestScoreboard(scoreModalQuery(0)));
  document.getElementById('scoreboard-modal-scroll')?.addEventListener('scroll', (e) => {
    const el = e.currentTarget;
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 24) loadMoreModalScores();
  });
  document.querySelector('.scoreboard-section')?.addEventListener('scroll', (e) => {
    const el = e.currentTarget;
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 24) loadMoreSidebarScores();
  });
  document.querySelectorAll('[data-close]').forEach((el) => {
    el.addEventListener('click', () => toggleHelp(false));
  });
  if (location.hash === '#help') toggleHelp(true);
});

document.addEventListener('keydown', (e) => {
  // Don't steal shortcuts while the user is typing in a field.
  const t = e.target;
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
  // Leave modifier combos (Ctrl+C copy, Cmd+C, etc.) to the browser.
  if (e.ctrlKey || e.metaKey || e.altKey) return;
  switch (e.key) {
    case '?':
      e.preventDefault();
      toggleHelp();
      return;
    case 'q':
    case 'Escape':
      if (!document.getElementById('scoreboard-modal')?.hidden) {
        e.preventDefault();
        closeScoreboardModal();
      } else if (!document.getElementById('help-modal')?.hidden) {
        e.preventDefault();
        toggleHelp(false);
      }
      return;
    case 'c':
      e.preventDefault();
      cycleScheme();
      return;
    case 'z':
      e.preventDefault();
      document.body.classList.toggle('low-fatigue');
      return;
    case 'h':
      e.preventDefault();
      stepBoard(-1);
      return;
    case 'l':
      e.preventDefault();
      stepBoard(1);
      return;
    case 'f': {
      e.preventDefault();
      if (gameState.followName) {
        setFollowName('');
      } else {
        const leader = gameState.scoreboard[0]?.username;
        if (leader) setFollowName(leader);
      }
      updateDom();
      return;
    }
    case 'j':
      e.preventDefault();
      stepFollow(1);
      return;
    case 'k':
      e.preventDefault();
      stepFollow(-1);
      return;
    default:
      if (e.key >= '1' && e.key <= '9') {
        const board = gameState.boards[Number(e.key) - 1];
        if (board) {
          e.preventDefault();
          watchBoard(board.id);
        }
      }
  }
});
