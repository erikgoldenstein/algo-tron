// Follow-player controls in the scoreboard header.
//
// Depends on: helpers.js (esc), gameState.js.
// Runtime callbacks call updateDom and ensureWatched after all scripts load.
// Provides: updateFollowPlayer, setFollowName, stepFollow, clearFollow.

let followOptionIndex = -1;

function updateFollowPlayer() {
  const start = document.getElementById('follow-player-start');
  const editor = document.getElementById('follow-player-editor');
  const input = document.getElementById('follow-player-input');
  if (!start || !editor || !input) return;

  const editing = gameState.followEditing || gameState.followName;
  start.hidden = !!editing;
  editor.hidden = !editing;
  start.onclick = () => {
    gameState.followEditing = true;
    updateDom();
    input.focus();
  };
  if (!editing) return;

  if (document.activeElement !== input && input.value.trim() !== gameState.followName) {
    input.value = gameState.followName;
  }
  input.oninput = () => {
    gameState.followName = input.value.trim();
    followOptionIndex = -1;
    updateFollowOptions();
  };
  input.onkeydown = (e) => {
    const options = followOptions(input.value);
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      if (!options.length) return;
      e.preventDefault();
      const delta = e.key === 'ArrowDown' ? 1 : -1;
      const visibleCount = Math.min(options.length, 10);
      followOptionIndex = (followOptionIndex + delta + visibleCount) % visibleCount;
      updateFollowOptions();
      return;
    }
    if (e.key === 'Escape') {
      e.preventDefault();
      followOptionIndex = -1;
      hideFollowOptions();
      return;
    }
    if (e.key !== 'Tab' && e.key !== 'Enter') return;
    const selected = options[followOptionIndex >= 0 ? followOptionIndex : 0];
    if (!selected) return;
    e.preventDefault();
    input.value = selected;
    setFollowName(selected);
    hideFollowOptions();
  };
  input.onblur = () => setTimeout(() => {
    if (!input.value.trim()) {
      clearFollow();
      updateDom();
    }
    hideFollowOptions();
  }, 0);
  updateFollowOptions();
}

function setFollowName(value) {
  const trimmed = value.trim();
  if (!trimmed) {
    clearFollow();
    updateDom();
    return;
  }
  // Choosing a player is an explicit viewer preference and overrides a
  // screen URL's lobby preference.
  gameState.lobbyPreference = '';
  gameState.autoLobby = '';
  gameState.followName = allBoardNames().find((name) => sameName(name, trimmed)) || trimmed;
  gameState.followEditing = false;
  followOptionIndex = -1;
  updateFollowOptions();
  ensureWatched({ preserveFollow: true });
  updateDom({ scoreboard: false });
}

function clearFollow() {
  gameState.followName = '';
  gameState.followEditing = false;
  followOptionIndex = -1;
  const input = document.getElementById('follow-player-input');
  if (input) input.value = '';
  hideFollowOptions();
}

// stepFollow cycles the followed player through all known names ("j"/"k"
// keys); starts at the first name when nobody is followed yet.
function stepFollow(delta) {
  const names = allBoardNames();
  if (!names.length) return;
  const i = names.findIndex((name) => sameName(name, gameState.followName));
  const next = i < 0 ? names[0] : names[(i + delta + names.length) % names.length];
  setFollowName(next);
  updateDom();
}

function allBoardNames() {
  const seen = new Set();
  for (const b of gameState.boards) {
    for (const name of b.names || []) seen.add(name);
  }
  return [...seen].sort();
}

function followNameIsAlive(name = gameState.followName) {
  return !!name && allBoardNames().some((candidate) => sameName(candidate, name));
}

function followOptions(value) {
  const q = value.trim().toLowerCase();
  return allBoardNames().filter((name) => !q || name.toLowerCase().includes(q));
}

function updateFollowOptions() {
  const input = document.getElementById('follow-player-input');
  const box = document.getElementById('follow-player-options');
  if (!input || !box || document.activeElement !== input) return;
  const options = followOptions(input.value);
  const visible = options.slice(0, 10);
  box.hidden = visible.length === 0;
  if (box.hidden) return;
  input.setAttribute('aria-expanded', 'true');
  box.innerHTML = visible.map((name, i) => '<button type="button" role="option" aria-selected="' + (i === followOptionIndex) + '" class="' + (i === followOptionIndex ? 'active' : '') + '" data-name="' + esc(name) + '">' + esc(name) + '</button>').join('');
  box.querySelectorAll('button').forEach((btn) => {
    btn.onpointerdown = (e) => {
      e.preventDefault();
      input.value = btn.dataset.name;
      setFollowName(input.value);
      hideFollowOptions();
    };
  });
}

function hideFollowOptions() {
  const box = document.getElementById('follow-player-options');
  const input = document.getElementById('follow-player-input');
  if (box) box.hidden = true;
  if (input) input.setAttribute('aria-expanded', 'false');
}

// Scoreboard names are useful, unambiguous follow targets. This is bound
// after each scoreboard render because rows are intentionally rebuilt.
function bindScoreFollowTargets(root = document) {
  root.querySelectorAll?.('.score-follow-target').forEach((el) => {
    el.onclick = (event) => {
      event.preventDefault();
      event.stopPropagation();
      if (sameName(el.dataset.followName || '', gameState.followName)) {
        clearFollow();
      } else {
        setFollowName(el.dataset.followName || '');
      }
      updateDom();
    };
    el.title = 'follow ' + (el.dataset.followName || 'player');
  });
}
