// Event-driven render scheduling and layout-driven scoreboard name reflow.
//
// This is an ES module so rendering has one explicit subscription point while
// the older viewer modules can continue to publish simple invalidations.
// Depends on: store.js, render.js (render), render_chart.js (renderChart),
// helpers.js (getSwitch, fitChars), dom.js (scoreNameChars, renderScoreName).

import { viewerStore } from './store.js';
import { render } from './render.js';
import { renderChart } from './render_chart.js';

let frame = 0;
let boardDirty = true;
let chartDirty = true;

function scheduleRender({ board = false, chart = false } = {}) {
  boardDirty ||= board;
  chartDirty ||= chart;
  if (frame) return;
  frame = requestAnimationFrame(() => {
    frame = 0;
    if (boardDirty) render();
    if (chartDirty) renderChart();
    boardDirty = false;
    chartDirty = false;
  });
}

viewerStore.subscribe(({ type, payload }) => {
  const scoreboardResponse = type === 'scoreboard';
  const domOptions = {
    scoreboard: !['tick', 'chat', 'chat_snapshot'].includes(type),
    renderModal: !scoreboardResponse,
    shell: !['tick', 'chat', 'chat_snapshot', 'scoreboard'].includes(type),
    stats: type === 'tick' || type === 'scoreboard',
    chat: type !== 'scoreboard',
    ...payload?.dom,
  };
  globalThis.updateDom?.(domOptions);
  if (scoreboardResponse && !document.getElementById('scoreboard-modal')?.hidden) {
    globalThis.renderScoreboardModalRows?.();
  }
  if (type === 'end') globalThis.scheduleScorePlotRefresh?.();

  switch (type) {
    case 'init':
    case 'game':
      scheduleRender({ board: true, chart: true });
      break;
    case 'tick':
      scheduleRender({ board: true });
      break;
    case 'end':
    case 'scoreboard':
    case 'scope':
      scheduleRender({ chart: true });
      break;
    case 'theme':
    case 'follow':
      scheduleRender({ board: true, chart: true });
      break;
    case 'resize':
      scheduleRender({ board: true, chart: true });
      break;
  }
});

scheduleRender({ board: true, chart: true });

// Tick scoreboard name cells so scrolling names slide in-place between
// websocket-driven full re-renders. No-op when the switch is off.
setInterval(() => {
  if (!getSwitch('scrollNames')) return;
  document.querySelectorAll('#scoreboard .namestr, #scoreboard-modal-rows .namestr').forEach((el) => {
    renderScoreName(el);
  });
}, 250);

// When the layout changes (window resize, modal opening, etc.) the name
// column's width changes too — re-measure and reflow the names. Skipping
// this would leave names truncated to their pre-resize length.
window.addEventListener('resize', () => {
  viewerStore.publish('resize');
  const scoreboardEl = document.getElementById('scoreboard');
  const firstNameCell = scoreboardEl?.querySelector('td.name');
  if (!firstNameCell) return;
  const cap = Math.max(0, fitChars(firstNameCell) - 2);
  if (cap === scoreNameChars) return;
  scoreNameChars = cap;
  scoreboardEl.querySelectorAll('.namestr').forEach((el) => {
    renderScoreName(el);
  });
});
