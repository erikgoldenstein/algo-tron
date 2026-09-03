// Scoreboard scoreplot tab. The plot is fetched only while the tab is open;
// it is intentionally separate from the live WebSocket/render loop.
//
// Depends on: gameState.js, helpers.js (esc), schemes.js (playerColor),
// ws.js (requestScoreboard). Provides: setScoreboardModalView,
// updateScorePlotUsers.

let scorePlotView = 'scoreboard';
let scorePlotData = null;
let scorePlotSelected = [];
let scorePlotCandidates = new Map();
let scorePlotRequest = null;
let scorePlotRequestID = 0;
let scorePlotSearchTimer = 0;
let scorePlotModalHeight = 0;

function setScoreboardModalView(view) {
  scorePlotView = view === 'scoreplot' ? 'scoreplot' : 'scoreboard';
  const scoreboard = document.getElementById('scoreboard-view');
  const scoreplot = document.getElementById('scoreplot-view');
  const modal = document.querySelector('.scoreboard-modal-window');
  const scoreboardTab = document.getElementById('scoreboard-tab');
  const scoreplotTab = document.getElementById('scoreplot-tab');
  // Measure before hiding the scoreboard. Its fixed rows viewport is the
  // canonical modal height; the alternate view adopts that exact height.
  if (scorePlotView === 'scoreplot' && scoreboard && !scoreboard.hidden) {
    scorePlotModalHeight = scoreboard.offsetHeight;
  }
  if (scoreboard) scoreboard.hidden = scorePlotView !== 'scoreboard';
  if (scoreplot) scoreplot.hidden = scorePlotView !== 'scoreplot';
  if (scoreplot && scorePlotView === 'scoreplot' && scorePlotModalHeight) {
    scoreplot.style.height = scorePlotModalHeight + 'px';
  }
  if (modal) modal.classList.toggle('scoreplot-open', scorePlotView === 'scoreplot');
  if (scoreboardTab) {
    scoreboardTab.classList.toggle('active', scorePlotView === 'scoreboard');
    scoreboardTab.setAttribute('aria-pressed', scorePlotView === 'scoreboard');
  }
  if (scoreplotTab) {
    scoreplotTab.classList.toggle('active', scorePlotView === 'scoreplot');
    scoreplotTab.setAttribute('aria-pressed', scorePlotView === 'scoreplot');
  }
  if (scorePlotView === 'scoreplot') {
    updateScorePlotUsers();
    requestScoreboard({ period: 'all', sort: 'ts', search: '', offset: 0, limit: 50 });
    validateScorePlotRange();
    renderScorePlot();
  } else {
    if (scorePlotRequest) scorePlotRequest.abort();
    scorePlotRequest = null;
    scorePlotRequestID++;
    hideScorePlotOptions();
  }
}

function updateScorePlotUsers() {
  if (typeof gameState === 'undefined' || scorePlotView !== 'scoreplot') return;
  const pages = Object.values(gameState.scorePages || {});
  for (const page of pages) {
    for (const entry of page.entries || []) {
      if (entry.username && !entry.oldOwner) scorePlotCandidates.set(entry.username, entry);
    }
  }
  renderScorePlotUserOptions();
}

function fuzzyScore(text, query) {
  text = text.toLowerCase();
  query = query.toLowerCase().trim();
  if (!query) return 0;
  let at = 0;
  let score = 0;
  let previous = -2;
  for (const char of query) {
    const found = text.indexOf(char, at);
    if (found < 0) return -1;
    score += found === previous + 1 ? 3 : 1;
    if (found === 0 || text[found - 1] === '_' || text[found - 1] === '-') score += 2;
    previous = found;
    at = found + 1;
  }
  return score - (text.length - query.length) * 0.01;
}

function renderScorePlotUserOptions(show) {
  const root = document.getElementById('scoreplot-user-options');
  const input = document.getElementById('scoreplot-user-search');
  if (!root || !input) return;
  const query = input.value;
  const selected = new Set(scorePlotSelected.map((user) => user.username));
  const options = [...scorePlotCandidates.values()]
    .map((entry) => ({ entry, score: fuzzyScore(entry.username, query) }))
    .filter((item) => item.score >= 0 && !selected.has(item.entry.username))
    .sort((a, b) => b.score - a.score || a.entry.username.localeCompare(b.entry.username))
    .slice(0, 50);
  root.innerHTML = options.length
    ? options.map(({ entry }) => '<button type="button" data-scoreplot-user="' + esc(entry.username) + '">' + esc(entry.username) + '</button>').join('')
    : '<span class="scoreplot-no-users">no users found</span>';
  if (show !== undefined) root.hidden = !show;
}

function hideScorePlotOptions() {
  const root = document.getElementById('scoreplot-user-options');
  if (root) root.hidden = true;
}

function renderScorePlotSelection() {
  const root = document.getElementById('scoreplot-selected');
  if (!root) return;
  root.innerHTML = scorePlotSelected.map((user) => {
    const color = playerColor(user.username);
    return '<span class="scoreplot-chip" style="--scoreplot-user-color:' + esc(color) + '">'
      + '<span>' + esc(user.username) + '</span>'
      + '<button type="button" aria-label="remove ' + esc(user.username) + '" data-scoreplot-remove="' + esc(user.username) + '">×</button>'
      + '</span>';
  }).join('');
}

function addScorePlotUser(username) {
  if (!username || scorePlotSelected.some((user) => user.username === username)) return;
  if (scorePlotSelected.length >= 16) {
    setScorePlotStatus('maximum of 16 users selected', true);
    return;
  }
  scorePlotSelected.push({ username });
  renderScorePlotSelection();
  const input = document.getElementById('scoreplot-user-search');
  if (input) input.value = '';
  renderScorePlotUserOptions();
  hideScorePlotOptions();
  fetchScorePlot();
}

function removeScorePlotUser(username) {
  scorePlotSelected = scorePlotSelected.filter((user) => user.username !== username);
  renderScorePlotSelection();
  fetchScorePlot();
}

function parseScorePlotTime(raw, now) {
  const value = raw.trim();
  if (value.toLowerCase() === 'now') return now;
  if (/^\d+$/.test(value)) {
    const timestamp = Number(value);
    return Number.isSafeInteger(timestamp) ? timestamp : null;
  }
  if (!/^now[+-]/i.test(value)) return null;
  const add = value[3] === '+';
  let relative = value.slice(4);
  let unit = '';
  if (relative.endsWith('M')) {
    unit = 'M';
    relative = relative.slice(0, -1);
  } else if (relative.endsWith('y') || relative.endsWith('Y')) {
    unit = 'y';
    relative = relative.slice(0, -1);
  } else {
    const lower = relative.toLowerCase();
    for (const candidate of ['ms', 's', 'm', 'h', 'd', 'w']) {
      if (lower.endsWith(candidate)) {
        unit = candidate;
        relative = relative.slice(0, -candidate.length);
        break;
      }
    }
  }
  if (!unit || !/^\d+$/.test(relative)) return null;
  const amount = Number(relative);
  if (!Number.isSafeInteger(amount)) return null;
  if (unit === 'M' || unit === 'y') {
    const date = new Date(now);
    if (unit === 'M') date.setMonth(date.getMonth() + (add ? amount : -amount));
    else date.setFullYear(date.getFullYear() + (add ? amount : -amount));
    const timestamp = date.getTime();
    return Number.isFinite(timestamp) ? timestamp : null;
  }
  const multipliers = { ms: 1, s: 1000, m: 60000, h: 3600000, d: 86400000, w: 604800000 };
  const offset = amount * multipliers[unit];
  if (!Number.isSafeInteger(offset)) return null;
  return add ? now + offset : now - offset;
}

function scorePlotRange() {
  const fromInput = document.getElementById('scoreplot-from');
  const toInput = document.getElementById('scoreplot-to');
  const now = Date.now();
  const from = fromInput ? parseScorePlotTime(fromInput.value, now) : null;
  const to = toInput ? parseScorePlotTime(toInput.value, now) : null;
  let error = '';
  if (from === null) error = 'invalid time from';
  else if (to === null) error = 'invalid time to';
  else if (from > to) error = 'time from must not be after time to';
  if (fromInput) fromInput.classList.toggle('invalid', from === null || (to !== null && from > to));
  if (toInput) toInput.classList.toggle('invalid', to === null || (from !== null && from > to));
  return { from, to, error };
}

function validateScorePlotRange() {
  const range = scorePlotRange();
  const error = document.getElementById('scoreplot-time-error');
  if (error) error.textContent = range.error
    ? range.error + ' (use now, now-2d, now-1M, now-1Y, or Unix milliseconds)'
    : '';
  return range;
}

function setScorePlotStatus(text, error) {
  const status = document.getElementById('scoreplot-status');
  if (status) {
    status.textContent = text;
    status.classList.toggle('error', !!error);
  }
}

async function fetchScorePlot() {
  if (scorePlotView !== 'scoreplot') return;
  const range = validateScorePlotRange();
  if (range.error) {
    scorePlotData = null;
    if (scorePlotRequest) scorePlotRequest.abort();
    setScorePlotStatus(range.error, true);
    renderScorePlot();
    return;
  }
  if (!scorePlotSelected.length) {
    scorePlotData = null;
    setScorePlotStatus('select users to plot', false);
    renderScorePlot();
    return;
  }
  if (scorePlotRequest) scorePlotRequest.abort();
  const controller = new AbortController();
  scorePlotRequest = controller;
  const requestID = ++scorePlotRequestID;
  const params = new URLSearchParams({
    metric: document.getElementById('scoreplot-metric')?.value || 'trueskill',
    from: document.getElementById('scoreplot-from')?.value || '',
    to: document.getElementById('scoreplot-to')?.value || '',
  });
  for (const user of scorePlotSelected) params.append('user', user.username + '/*');
  setScorePlotStatus('loading...', false);
  scorePlotData = null;
  renderScorePlot();
  try {
    const response = await fetch('/api/history?' + params.toString(), { signal: controller.signal });
    if (!response.ok) throw new Error(await response.text() || 'request failed');
    const data = await response.json();
    if (requestID !== scorePlotRequestID || scorePlotView !== 'scoreplot') return;
    scorePlotData = data;
    setScorePlotStatus(data.series?.some((series) => series.points?.length) ? '' : 'no data in this time range', false);
    renderScorePlot();
  } catch (error) {
    if (error.name === 'AbortError') return;
    if (requestID !== scorePlotRequestID) return;
    scorePlotData = null;
    setScorePlotStatus('could not load score history', true);
    renderScorePlot();
  }
}

function renderScorePlot() {
  const canvas = document.getElementById('scoreplot-chart');
  const wrap = canvas?.parentElement;
  if (!canvas || !wrap || scorePlotView !== 'scoreplot') return;
  const width = Math.max(1, Math.floor(wrap.clientWidth));
  const height = Math.max(1, Math.floor(wrap.clientHeight));
  const dpr = window.devicePixelRatio || 1;
  canvas.width = width * dpr;
  canvas.height = height * dpr;
  canvas.style.width = width + 'px';
  canvas.style.height = height + 'px';
  const ctx = canvas.getContext('2d');
  ctx.scale(dpr, dpr);
  ctx.clearRect(0, 0, width, height);
  if (!scorePlotData?.series?.length) return;

  const styles = getComputedStyle(document.documentElement);
  const grid = styles.getPropertyValue('--grid').trim() || 'rgba(255,255,255,.15)';
  const text = styles.getPropertyValue('--text-muted').trim() || '#888';
  const left = 42, right = 10, top = 12, bottom = 24;
  const plotWidth = Math.max(1, width - left - right);
  const plotHeight = Math.max(1, height - top - bottom);
  const from = Number(scorePlotData.from);
  const to = Number(scorePlotData.to);
  const span = Math.max(1, to - from);
  const values = scorePlotData.series.flatMap((series) => (series.points || []).map((point) => Number(point.value)).filter(Number.isFinite));
  if (!values.length) return;
  let min = scorePlotData.metric === 'winrate' ? 0 : Math.min(...values);
  let max = scorePlotData.metric === 'winrate' ? 1 : Math.max(...values);
  if (min === max) { min -= 1; max += 1; }
  else if (scorePlotData.metric !== 'winrate') { const pad = (max - min) * 0.08; min -= pad; max += pad; }
  const x = (time) => left + ((time - from) / span) * plotWidth;
  const y = (value) => top + (1 - (value - min) / (max - min)) * plotHeight;
  const axisLabel = (time) => span <= 2 * 86400000
    ? new Date(time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : new Date(time).toLocaleDateString();

  ctx.font = canvasFont(10, '');
  ctx.fillStyle = text;
  ctx.textAlign = 'right';
  ctx.textBaseline = 'middle';
  for (let i = 0; i <= 4; i++) {
    const yy = top + plotHeight * i / 4;
    const label = max - (max - min) * i / 4;
    ctx.strokeStyle = grid;
    ctx.lineWidth = 1;
    ctx.beginPath(); ctx.moveTo(left, yy + .5); ctx.lineTo(width - right, yy + .5); ctx.stroke();
    ctx.fillText(scorePlotData.metric === 'winrate' ? (label * 100).toFixed(0) + '%' : label.toFixed(0), left - 6, yy);
  }
  ctx.textAlign = 'left'; ctx.textBaseline = 'alphabetic';
  ctx.fillText(axisLabel(from), left, height - 5);
  ctx.textAlign = 'right';
  ctx.fillText(axisLabel(to), width - right, height - 5);

  for (const series of scorePlotData.series) {
    const points = (series.points || []).filter((point) => Number.isFinite(Number(point.value)));
    if (!points.length) continue;
    ctx.strokeStyle = playerColor(series.username);
    ctx.fillStyle = ctx.strokeStyle;
    ctx.lineWidth = 1.5;
    for (let i = 0; i < points.length; i++) {
      const point = points[i];
      if (i > 0) {
        ctx.setLineDash(point.gap ? [3, 3] : []);
        ctx.beginPath(); ctx.moveTo(x(points[i - 1].time), y(points[i - 1].value)); ctx.lineTo(x(point.time), y(point.value)); ctx.stroke();
      }
      ctx.setLineDash([]);
      ctx.fillRect(x(point.time) - 2, y(point.value) - 2, 4, 4);
    }
  }
  ctx.setLineDash([]);
}

function resizeScorePlotModal() {
  const scoreboard = document.getElementById('scoreboard-view');
  if (scorePlotView === 'scoreboard' && scoreboard && !scoreboard.hidden) {
    scorePlotModalHeight = scoreboard.offsetHeight;
  }
  const scoreplot = document.getElementById('scoreplot-view');
  if (scoreplot && scorePlotView === 'scoreplot' && scorePlotModalHeight) {
    scoreplot.style.height = scorePlotModalHeight + 'px';
  }
  renderScorePlot();
}

function initScorePlot() {
  document.getElementById('scoreboard-tab')?.addEventListener('click', () => setScoreboardModalView('scoreboard'));
  document.getElementById('scoreplot-tab')?.addEventListener('click', () => setScoreboardModalView('scoreplot'));
  document.getElementById('scoreplot-metric')?.addEventListener('change', fetchScorePlot);
  for (const id of ['scoreplot-from', 'scoreplot-to']) {
    document.getElementById(id)?.addEventListener('input', fetchScorePlot);
  }
  const picker = document.getElementById('scoreplot-user-picker');
  const input = document.getElementById('scoreplot-user-search');
  const options = document.getElementById('scoreplot-user-options');
  picker?.addEventListener('click', (event) => { event.stopPropagation(); renderScorePlotUserOptions(true); });
  input?.addEventListener('input', () => {
    renderScorePlotUserOptions(true);
    clearTimeout(scorePlotSearchTimer);
    scorePlotSearchTimer = setTimeout(() => requestScoreboard({ period: 'all', sort: 'ts', search: input.value, offset: 0, limit: 50 }), 150);
  });
  options?.addEventListener('click', (event) => {
    const button = event.target.closest?.('[data-scoreplot-user]');
    if (button) addScorePlotUser(button.dataset.scoreplotUser);
  });
  document.getElementById('scoreplot-selected')?.addEventListener('click', (event) => {
    const button = event.target.closest?.('[data-scoreplot-remove]');
    if (button) removeScorePlotUser(button.dataset.scoreplotRemove);
  });
  document.addEventListener('click', () => hideScorePlotOptions());
  window.addEventListener('resize', resizeScorePlotModal);
}

document.addEventListener('DOMContentLoaded', initScorePlot);
