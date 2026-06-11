// Render timers and layout-driven scoreboard name reflow.
//
// Depends on: render.js (render), render_chart.js (renderChart), helpers.js
// (displayName, getSwitch, fitChars), dom.js (scoreNameChars).

setInterval(() => { render(); renderChart(); }, 1000 / 30);

// Tick scoreboard name cells so scrolling names slide in-place between
// websocket-driven full re-renders. No-op when the switch is off.
setInterval(() => {
  if (!getSwitch('scrollNames')) return;
  document.querySelectorAll('#scoreboard .namestr').forEach((el) => {
    el.textContent = displayName(el.dataset.name, scoreNameChars);
  });
}, 250);

// When the layout changes (window resize, modal opening, etc.) the name
// column's width changes too — re-measure and reflow the names. Skipping
// this would leave names truncated to their pre-resize length.
window.addEventListener('resize', () => {
  const scoreboardEl = document.getElementById('scoreboard');
  const firstNameCell = scoreboardEl?.querySelector('td.name');
  if (!firstNameCell) return;
  const cap = Math.max(0, fitChars(firstNameCell) - 2);
  if (cap === scoreNameChars) return;
  scoreNameChars = cap;
  scoreboardEl.querySelectorAll('.namestr').forEach((el) => {
    el.textContent = displayName(el.dataset.name, scoreNameChars);
  });
});
