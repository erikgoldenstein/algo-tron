// Follow-player controls in the scoreboard header.
//
// Depends on: helpers.js (esc), gameState.js.
// Runtime callbacks call updateDom and ensureWatched after all scripts load.
// Provides: updateFollowPlayer, setFollowName, stepFollow.

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
  input.oninput = () => setFollowName(input.value);
  input.onkeydown = (e) => {
    if (e.key !== 'Tab') return;
    const options = followOptions(input.value);
    if (!options.length) return;
    e.preventDefault();
    input.value = options[0];
    setFollowName(input.value);
    hideFollowOptions();
  };
  input.onblur = () => setTimeout(() => {
    if (!input.value.trim()) {
      gameState.followName = '';
      gameState.followEditing = false;
      updateDom();
    }
    hideFollowOptions();
  }, 0);
  updateFollowOptions();
}

function setFollowName(value) {
  gameState.followName = value.trim();
  updateFollowOptions();
  ensureWatched();
}

// stepFollow cycles the followed player through all known names ("j"/"k"
// keys); starts at the first name when nobody is followed yet.
function stepFollow(delta) {
  const names = allBoardNames();
  if (!names.length) return;
  const i = names.indexOf(gameState.followName);
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

function followOptions(value) {
  const q = value.trim().toLowerCase();
  return allBoardNames().filter((name) => !q || name.toLowerCase().startsWith(q));
}

function updateFollowOptions() {
  const input = document.getElementById('follow-player-input');
  const box = document.getElementById('follow-player-options');
  if (!input || !box || document.activeElement !== input) return;
  const options = followOptions(input.value);
  box.hidden = options.length === 0 || options.length >= 10;
  if (box.hidden) return;
  box.innerHTML = options.map((name) => '<button data-name="' + esc(name) + '">' + esc(name) + '</button>').join('');
  box.querySelectorAll('button').forEach((btn) => {
    btn.onmousedown = (e) => {
      e.preventDefault();
      input.value = btn.dataset.name;
      setFollowName(input.value);
      hideFollowOptions();
    };
  });
}

function hideFollowOptions() {
  const box = document.getElementById('follow-player-options');
  if (box) box.hidden = true;
}
