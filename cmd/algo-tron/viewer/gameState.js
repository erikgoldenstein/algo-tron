// Pure game-state tracking. The server sends four message types over the
// WebSocket; this file applies them to a single mutable `gameState` object
// that everything else (UI, canvas renderer) reads from.
//
// Wire protocol — see view.go for the canonical definition.
//   {type:"init", serverInfo, viewInfo, scoreboard, chartData, lastWinners, game?}
//   {type:"game", id, width, height, players:[{id,name,pos,moves,alive,chat?}]}
//   {type:"tick", positions:[[id,x,y]...], deaths?:[id], chats?:{id:msg}}
//   {type:"end",  scoreboard, chartData, lastWinners}
//   {type:"misc", content:"shutdown"} — lifecycle event; "shutdown" → banner.
//
// chartData is a 20-point series; each point is { name: i, [username]: elo, ... }.
// Players whose ScoreHistory predates elo tracking will be missing from the
// earlier points until enough new games have been played.
//
// `init` is the snapshot sent on connect. `game` resets per-game state when a
// new game begins. `tick` appends one move per alive player and updates chat /
// deaths. `end` refreshes scoreboard + chart at game-over.

const gameState = {
  serverInfo: [],
  viewInfo: [],
  scoreboard: [],
  chartData: [],
  lastWinners: [],
  game: null, // { id, width, height, players: { [id]: { id, name, pos, moves, alive, chat } } }
};

function applyMessage(msg) {
  switch (msg.type) {
    case 'init': applyInit(msg); break;
    case 'game': applyGame(msg); break;
    case 'tick': applyTick(msg); break;
    case 'end':  applyEnd(msg);  break;
  }
}

function applyInit(msg) {
  gameState.serverInfo  = msg.serverInfo  || [];
  gameState.viewInfo    = msg.viewInfo    || [];
  gameState.scoreboard  = msg.scoreboard  || [];
  gameState.chartData   = msg.chartData   || [];
  gameState.lastWinners = msg.lastWinners || [];
  gameState.game = msg.game ? buildGame(msg.game) : null;
}

function applyGame(msg) {
  gameState.game = buildGame(msg);
}

function applyTick(msg) {
  const g = gameState.game;
  if (!g) return;
  for (const [id, x, y] of msg.positions || []) {
    const p = g.players[id];
    if (!p) continue;
    p.pos = { x, y };
    p.moves.push(p.pos);
  }
  for (const id of msg.deaths || []) {
    const p = g.players[id];
    if (p) p.alive = false;
  }
  // Server sends only currently non-empty chats; anything not listed has expired.
  const chats = msg.chats || {};
  for (const id in g.players) {
    g.players[id].chat = chats[id] || '';
  }
}

function applyEnd(msg) {
  gameState.scoreboard  = msg.scoreboard  || [];
  gameState.chartData   = msg.chartData   || [];
  gameState.lastWinners = msg.lastWinners || [];
}

function buildGame(m) {
  const players = {};
  for (const p of m.players || []) {
    players[p.id] = {
      id: p.id,
      name: p.name,
      pos: p.pos,
      moves: p.moves ? p.moves.slice() : [p.pos],
      alive: p.alive !== false,
      chat: p.chat || '',
    };
  }
  return { id: m.id, width: m.width, height: m.height, players };
}
