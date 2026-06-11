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

document.addEventListener('DOMContentLoaded', () => {
  document.getElementById('help-btn')?.addEventListener('click', () => toggleHelp(true));
  document.querySelectorAll('[data-close]').forEach((el) => {
    el.addEventListener('click', () => toggleHelp(false));
  });
  if (location.hash === '#help') toggleHelp(true);
});

document.addEventListener('keydown', (e) => {
  // Don't steal shortcuts while the user is typing in a field.
  const t = e.target;
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
  switch (e.key) {
    case '?':
      e.preventDefault();
      toggleHelp();
      return;
    case 'q':
    case 'Escape':
      if (!document.getElementById('help-modal').hidden) {
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
      const leader = gameState.scoreboard[0]?.username;
      if (leader) { setFollowName(leader); updateDom(); }
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
