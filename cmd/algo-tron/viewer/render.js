// Canvas rendering for the game board. render_chart.js draws the TrueSkill
// chart; render_loop.js owns timers.
//
// The board is one canvas; the TrueSkill chart is another. Both are redrawn
// each frame from current gameState — no incremental damage tracking.
//
// Depends on: helpers.js (contrastText), schemes.js (currentScheme,
// SCHEMES, playerColor, canvasFont), gameState.js (gameState).

const wallSize = 1;
const floorSize = 16;
const roomSize = floorSize + wallSize;

function line(ctx, radius, color, from, to) {
  ctx.strokeStyle = color;
  ctx.lineWidth = radius * 2;
  ctx.lineCap = 'round';
  ctx.lineJoin = 'round';
  ctx.beginPath();
  ctx.moveTo(from.x, from.y);
  ctx.lineTo(to.x, to.y);
  ctx.stroke();
}

function render() {
  const game = gameState.game;
  const canvas = document.getElementById('game');
  if (!game || !canvas.parentElement) return;

  const ctx = canvas.getContext('2d');
  const dpr = window.devicePixelRatio || 1;
  const viewport = window.visualViewport || { width: window.innerWidth, height: window.innerHeight };
  const size = Math.floor(Math.min(
    canvas.parentElement.clientHeight,
    canvas.parentElement.clientWidth,
    viewport.height,
    viewport.width,
  ));
  canvas.width = size * dpr;
  canvas.height = size * dpr;
  canvas.style.width = size + 'px';
  canvas.style.height = size + 'px';
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

  const viewFactor = size / (Math.max(game.width, game.height) * roomSize);
  const factoredRoomSize = roomSize * viewFactor;
  const playerRadius = floorSize * viewFactor * .42;

  renderBoard(ctx, game, size, factoredRoomSize);
  renderPlayers(ctx, game, factoredRoomSize, playerRadius);
}

function renderBoard(ctx, game, size, room) {
  const s = SCHEMES[currentScheme];
  ctx.fillStyle = s.bg;
  ctx.fillRect(0, 0, size, size);
  ctx.strokeStyle = s.grid;
  ctx.lineWidth = 1;
  // The last vertical/horizontal grid line lands at x=size (or y=size),
  // which with the +0.5 alignment would fall outside the canvas and not
  // render. Clamp the closing edges to size-0.5 so the board is fully
  // boxed in on the right and bottom.
  for (let x = 0; x <= game.width; x++) {
    const tmpX = Math.min(Math.round(x * room) + 0.5, size - 0.5);
    ctx.beginPath();
    ctx.moveTo(tmpX, 0);
    ctx.lineTo(tmpX, size);
    ctx.stroke();
  }
  for (let y = 0; y <= game.height; y++) {
    const tmpY = Math.min(Math.round(y * room) + 0.5, size - 0.5);
    ctx.beginPath();
    ctx.moveTo(0, tmpY);
    ctx.lineTo(size, tmpY);
    ctx.stroke();
  }
}

function renderPlayers(ctx, game, room, radius) {
  // Two passes: trails+heads first, then name+chat overlays so labels never
  // get drawn over by another player's trail.
  for (const player of Object.values(game.players)) {
    if (!player.alive) continue;
    const c = playerColor(player.name);
    const x = player.pos.x * room + room / 2;
    const y = player.pos.y * room + room / 2;
    // The followed player gets a subtle low-opacity outline under their
    // tron-line so they're easy to spot without dominating the board.
    if (gameState.followName && player.name === gameState.followName) {
      renderFollowOutline(ctx, game, player, room, radius, c);
    }
    renderTrail(ctx, game, player, room, radius, c);
    renderHead(ctx, x, y, radius, c);
  }
  for (const player of Object.values(game.players)) {
    if (!player.alive) continue;
    const c = playerColor(player.name);
    const x = player.pos.x * room + room / 2;
    const y = player.pos.y * room + room / 2;
    renderName(ctx, player.name, x, y, c);
    if (player.chat) renderChat(ctx, player.chat, x, y, c);
  }
}

// Outline for the followed player. Drawn at full alpha onto an offscreen
// canvas, then composited once at low alpha — stroking directly with
// globalAlpha would stack opacity where the round line caps overlap and
// create blotches at the corners.
const _outlineCanvas = document.createElement('canvas');
function renderFollowOutline(ctx, game, player, room, radius, playerColor) {
  const main = ctx.canvas;
  if (_outlineCanvas.width !== main.width || _outlineCanvas.height !== main.height) {
    _outlineCanvas.width = main.width;
    _outlineCanvas.height = main.height;
  }
  const octx = _outlineCanvas.getContext('2d');
  octx.setTransform(1, 0, 0, 1, 0, 0);
  octx.clearRect(0, 0, _outlineCanvas.width, _outlineCanvas.height);
  octx.setTransform(ctx.getTransform());
  // Proportional plus a constant: on dense boards the lines render thin and
  // a purely proportional outline becomes too subtle to spot.
  const r = radius * 1.4 + 1.5;
  renderTrail(octx, game, player, room, r, playerColor);
  renderHead(octx, player.pos.x * room + room / 2, player.pos.y * room + room / 2, r, playerColor);
  ctx.save();
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.globalAlpha = 0.18;
  ctx.drawImage(_outlineCanvas, 0, 0);
  ctx.restore();
}

function renderTrail(ctx, game, player, room, radius, playerColor) {
  for (let i = 1; i < player.moves.length; i++) {
    const from = player.moves[i - 1];
    const to = player.moves[i];
    let ax = from.x, ay = from.y, bx = to.x, by = to.y;

    // Stretch trail across wrap boundaries: emit a stub on the opposite
    // side so the visual trail looks continuous through the wrap.
    if (from.x === 0 && to.x === game.width - 1) {
      ax = 0; bx = -1;
      line(ctx, radius, playerColor, { x: game.width * room + room / 2, y: to.y * room + room / 2 }, { x: (game.width - 1) * room + room / 2, y: to.y * room + room / 2 });
    }
    if (from.x === game.width - 1 && to.x === 0) {
      ax = game.width - 1; bx = game.width;
      line(ctx, radius, playerColor, { x: -room + room / 2, y: to.y * room + room / 2 }, { x: room / 2, y: to.y * room + room / 2 });
    }
    if (from.y === 0 && to.y === game.height - 1) {
      ay = 0; by = -1;
      line(ctx, radius, playerColor, { x: to.x * room + room / 2, y: game.height * room + room / 2 }, { x: to.x * room + room / 2, y: (game.height - 1) * room + room / 2 });
    }
    if (from.y === game.height - 1 && to.y === 0) {
      ay = game.height - 1; by = game.height;
      line(ctx, radius, playerColor, { x: to.x * room + room / 2, y: -room + room / 2 }, { x: to.x * room + room / 2, y: room / 2 });
    }

    line(ctx, radius, playerColor, { x: ax * room + room / 2, y: ay * room + room / 2 }, { x: bx * room + room / 2, y: by * room + room / 2 });
    renderHead(ctx, ax * room + room / 2, ay * room + room / 2, radius, playerColor);
  }
}

function renderHead(ctx, x, y, radius, playerColor) {
  ctx.fillStyle = playerColor;
  ctx.beginPath();
  ctx.arc(x, y, radius, 0, 2 * Math.PI);
  ctx.fill();
}

// Name pill above the player head — player-colored fill, label color
// auto-picked for contrast so the name stays readable on any palette.
function renderName(ctx, name, x, y, playerColor) {
  const s = SCHEMES[currentScheme];
  const labelColor = contrastText(playerColor);
  // Smaller pills on mobile (same breakpoint as the CSS) so they don't
  // swamp the smaller board.
  const mobile = window.innerWidth <= 1100;
  ctx.font = canvasFont(mobile ? 10 : 14, 'bold');
  name = displayName(name);
  const w = ctx.measureText(name).width;
  const padX = mobile ? 4 : 6;
  const boxW = w + padX * 2;
  const boxH = mobile ? 14 : 20;
  const boxX = x - boxW / 2;
  const boxY = y - boxH - (mobile ? 14 : 22);
  ctx.fillStyle = playerColor;
  ctx.fillRect(boxX, boxY, boxW, boxH);
  ctx.strokeStyle = s.text;
  ctx.lineWidth = 1;
  ctx.strokeRect(boxX + 0.5, boxY + 0.5, boxW - 1, boxH - 1);
  ctx.fillStyle = labelColor;
  ctx.textBaseline = 'middle';
  ctx.fillText(name, boxX + padX, boxY + boxH / 2 + 1);
}

// Chat bubble drawn just below the player head.
function renderChat(ctx, message, x, y, playerColor) {
  const s = SCHEMES[currentScheme];
  ctx.font = canvasFont(12);
  const w = ctx.measureText(message).width;
  const padX = 6;
  const boxW = w + padX * 2;
  const boxH = 18;
  const boxX = x - boxW / 2;
  const boxY = y + 22;
  ctx.fillStyle = s.bgElevated;
  ctx.fillRect(boxX, boxY, boxW, boxH);
  ctx.strokeStyle = playerColor;
  ctx.lineWidth = 1;
  ctx.strokeRect(boxX + 0.5, boxY + 0.5, boxW - 1, boxH - 1);
  ctx.fillStyle = s.text;
  ctx.textBaseline = 'middle';
  ctx.fillText(message, boxX + padX, boxY + boxH / 2 + 1);
}
