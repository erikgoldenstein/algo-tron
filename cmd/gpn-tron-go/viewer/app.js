let state = null;
let chat = [];
let last = {};

const crc32 = function (r) {
  for (var a, o = [], c = 0; c < 256; c++) {
    a = c;
    for (var f = 0; f < 8; f++) a = 1 & a ? 3988292384 ^ a >>> 1 : a >>> 1;
    o[c] = a;
  }
  for (var n = -1, t = 0; t < r.length; t++) n = n >>> 8 ^ o[255 & (n ^ r.charCodeAt(t))];
  return (-1 ^ n) >>> 0;
};

const color = (s) => '#' + ('000000' + crc32(s).toString(16)).slice(-6);

htmx.on('htmx:wsAfterMessage', (event) => {
  state = JSON.parse(event.detail.message);
  updateDom();
});

function updateDom() {
  if (!state) return;
  const game = state.serverInfoList[0] || { host: 'localhost', port: 4000 };
  const view = (state.viewInfoList || [])[0] || { host: location.hostname, port: Number(location.port || 443) };
  ports.innerHTML = '<li>- ' + view.port + ' [HTTP] (View server)</li><li>- ' + game.port + ' [TCP] (Game server)</li>';
  hosts.innerHTML = '<li>- ' + esc(view.host) + ' (View server)</li>' + state.serverInfoList.map((x) => '<li>- ' + esc(x.host) + ' (Game server)</li>').join('');
  scoreboard.innerHTML = state.scoreboard.length ? state.scoreboard.map(scoreRow).join('') : '<tr><td colspan="6">Nobody scored yet :(</td></tr>';

  for (const p of (state.game?.players || [])) {
    if (!p.chat) {
      last[p.name] = undefined;
      continue;
    }
    if (last[p.name] !== p.chat) {
      last[p.name] = p.chat;
      chat.push({ date: Date.now(), from: p.name, message: p.chat });
      chat = chat.slice(-30);
    }
  }
  document.getElementById('chat').innerHTML = [...chat].reverse().map(chatRow).join('');
}

function scoreRow(p, i) {
  const winner = state.lastWinners.includes(p.username) ? ' 🎉' : '';
  return '<tr><td>' + (i + 1) + '.</td><td style="color:' + color(p.username) + '">' + esc(p.username) + winner + '</td><td>' + p.winRatio.toFixed(2) + '</td><td>' + p.elo.toFixed(0) + '</td><td>' + p.wins + '</td><td>' + p.loses + '</td></tr>';
}

function chatRow(m) {
  const date = new Date(m.date).toISOString().replace(/^(\d+)-(\d+)-(\d+)T(\d+):(\d+):(\d+).*$/, '$3.$2.$1 - $4:$5:$6');
  return '<div style="margin:.5rem"><b>' + esc(m.from) + ' (' + date + ')</b><br>' + esc(m.message) + '</div>';
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

setInterval(render, 1000 / 30);

const wallSize = 1;
const floorSize = 16;
const roomSize = floorSize + wallSize;

function line(ctx, radius, color, from, to) {
  ctx.strokeStyle = color;
  ctx.lineWidth = radius * 2;
  ctx.beginPath();
  ctx.moveTo(from.x, from.y);
  ctx.lineTo(to.x, to.y);
  ctx.stroke();
}

function render() {
  const game = state?.game;
  const canvas = document.getElementById('game');
  if (!game || !canvas.parentElement) return;

  const ctx = canvas.getContext('2d');
  const size = Math.min(canvas.parentElement.clientHeight, canvas.parentElement.clientWidth);
  canvas.width = size;
  canvas.height = size;

  const viewFactor = size / (Math.max(game.width, game.height) * roomSize);
  const factoredRoomSize = roomSize * viewFactor;
  const playerRadius = floorSize * viewFactor * .4;

  renderBoard(ctx, game, size, factoredRoomSize);
  renderPlayers(ctx, game, factoredRoomSize, playerRadius);
}

function renderBoard(ctx, game, size, room) {
  ctx.fillStyle = '#090a35';
  ctx.fillRect(0, 0, size, size);
  ctx.strokeStyle = 'white';
  ctx.lineWidth = 1;
  for (let x = 0; x < game.width; x++) {
    const tmpX = x * room;
    ctx.beginPath();
    ctx.moveTo(tmpX, 0);
    ctx.lineTo(tmpX, size);
    ctx.stroke();
    for (let y = 0; y < game.height; y++) {
      const tmpY = y * room;
      ctx.beginPath();
      ctx.moveTo(0, tmpY);
      ctx.lineTo(size, tmpY);
      ctx.stroke();
    }
  }
}

function renderPlayers(ctx, game, room, radius) {
  for (const player of game.players) {
    if (!player.alive) continue;
    const playerColor = color(player.name);
    const x = player.pos.x * room + room / 2;
    const y = player.pos.y * room + room / 2;
    ctx.fillStyle = playerColor;

    renderTrail(ctx, game, player, room, radius, playerColor);
    renderHead(ctx, x, y, radius, playerColor);
    renderName(ctx, player.name, x, y, playerColor);
    if (player.chat) renderChat(ctx, player.chat, x, y, room);
  }
}

function renderTrail(ctx, game, player, room, radius, playerColor) {
  for (let i = 1; i < player.moves.length; i++) {
    const from = player.moves[i - 1];
    const to = player.moves[i];
    let ax = from.x, ay = from.y, bx = to.x, by = to.y;

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

function renderName(ctx, name, x, y, playerColor) {
  ctx.font = 'bold 18px serif';
  const width = ctx.measureText(name).width;
  const nameX = x - width / 2 - 10;
  const nameY = y - 59;
  ctx.fillStyle = playerColor;
  ctx.strokeStyle = 'white';
  ctx.lineWidth = 2;
  ctx.beginPath();
  ctx.rect(nameX, nameY, width + 10, 28);
  ctx.fill();
  ctx.stroke();
  ctx.fillStyle = 'white';
  ctx.textBaseline = 'top';
  ctx.fillText(name, nameX + 5, nameY + 5);
}

function renderChat(ctx, message, x, y, room) {
  ctx.fillStyle = 'white';
  ctx.fillRect(x - 10, y + room - 20, ctx.measureText(message).width + 20, 40);
  ctx.fillStyle = 'black';
  ctx.fillText(message, x, y + room);
}

async function talks() {
  try {
    const response = await fetch('https://cfp.gulas.ch/gpn22/schedule/v/0.26/widget/v2.json').then((r) => r.json());
    const now = new Date();
    const upcoming = response.talks.map((talk) => {
      const room = response.rooms.find((x) => x.id === talk.room) || {};
      return {
        title: typeof talk.title === 'string' ? talk.title : (talk.title.de || talk.title.en),
        room: typeof room.name === 'string' ? room.name : (room.name?.de || room.name?.en || 'Unknown'),
        start: new Date(talk.start),
        end: new Date(talk.end),
      };
    }).filter((talk) => talk.end > now).sort((a, b) => a.start - b.start);

    const current = upcoming.filter((talk) => talk.start <= now && talk.end > now);
    let next = upcoming.slice(current.length);
    if (next.length) next = next.filter((talk) => talk.start < new Date(+next[0].start + 7200000));
    currentTalks.innerHTML = current.map(talkHTML).join('');
    nextTalks.innerHTML = next.map(talkHTML).join('');
  } catch (e) {}
}

function talkHTML(talk) {
  return '<div><b>' + esc(talk.title) + '</b><br>' + esc(talk.room) + ' (' + talk.start.toTimeString().split(' ')[0] + ' - ' + talk.end.toTimeString().split(' ')[0] + ')<hr></div>';
}

talks();
setInterval(talks, 60000);
