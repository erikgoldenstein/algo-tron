// TrueSkill chart rendering. render_loop.js owns timers.
//
// Depends on: schemes.js (playerColor), gameState.js.
// Provides: renderChart.

function renderChart() {
  const canvas = document.getElementById('chart');
  if (!canvas?.parentElement) return;
  const ctx = canvas.getContext('2d');
  const dpr = window.devicePixelRatio || 1;
  const width = canvas.parentElement.clientWidth;
  const height = canvas.parentElement.clientHeight;
  canvas.width = width * dpr;
  canvas.height = height * dpr;
  canvas.style.width = width + 'px';
  canvas.style.height = height + 'px';
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, width, height);

  const boardScope = gameState.boards.length > 1 && gameState.scoreboardScope === 'board';
  const data = (boardScope ? gameState.boardChartData : gameState.chartData) || [];
  const names = [...new Set(data.flatMap((p) => Object.keys(p).filter((k) => k !== 'name')))].sort();
  if (!data.length || !names.length) return;

  let lo = Infinity, hi = -Infinity;
  for (const point of data) {
    for (const name of names) {
      const v = chartPoint(point[name]);
      if (!v) continue;
      lo = Math.min(lo, v.mu - v.sigma);
      hi = Math.max(hi, v.mu + v.sigma);
    }
  }
  if (!isFinite(lo) || !isFinite(hi) || lo === hi) {
    lo = (lo || 250) - 50;
    hi = (hi || 250) + 50;
  }

  const pad = { top: 4, right: 4, bottom: 4, left: 4 };
  const plotW = width - pad.left - pad.right;
  const plotH = height - pad.top - pad.bottom;
  const x = (i) => pad.left + i / Math.max(data.length - 1, 1) * plotW;
  const y = (v) => pad.top + (1 - (v - lo) / (hi - lo)) * plotH;

  ctx.lineCap = 'round';
  ctx.lineJoin = 'round';
  for (const name of names) {
    const color = playerColor(name);
    ctx.beginPath();
    let started = false;
    data.forEach((point, i) => {
      const v = chartPoint(point[name]);
      if (!v) return;
      if (!started) { ctx.moveTo(x(i), y(v.mu - v.sigma)); started = true; }
      else ctx.lineTo(x(i), y(v.mu - v.sigma));
    });
    if (started) {
      for (let i = data.length - 1; i >= 0; i--) {
        const v = chartPoint(data[i][name]);
        if (v) ctx.lineTo(x(i), y(v.mu + v.sigma));
      }
      ctx.closePath();
      ctx.globalAlpha = 0.08;
      ctx.fillStyle = color;
      ctx.fill();
      ctx.globalAlpha = 1;
    }

    ctx.lineWidth = 1.25;
    ctx.strokeStyle = color;
    ctx.beginPath();
    started = false;
    data.forEach((point, i) => {
      const v = chartPoint(point[name]);
      if (!v) return;
      if (!started) { ctx.moveTo(x(i), y(v.mu)); started = true; }
      else ctx.lineTo(x(i), y(v.mu));
    });
    ctx.stroke();
  }
}

function chartPoint(v) {
  if (typeof v === 'number') return { mu: v, sigma: 0 };
  if (v && typeof v.mu === 'number') return { mu: v.mu, sigma: typeof v.sigma === 'number' ? v.sigma : 0 };
  return null;
}
