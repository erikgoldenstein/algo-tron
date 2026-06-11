// Pure game-state tracking. The server sends JSON messages over the
// WebSocket; this file applies them to a single mutable `gameState` object
// that everything else (UI, canvas renderer) reads from.
//
// Several boards can run at once. We only hold the full state of the board
// we are subscribed to (`game`); `boards` is the lightweight list of all
// running boards, used for the tab bar. ws.js owns the subscription
// (sending {"watch": id}); the server answers with a "game" snapshot.
//
// Wire protocol — see view.go for the canonical definition.
//   {type:"init",   serverInfo, viewInfo, scoreboard, chartData, lastWinners, boards, game?}
//   {type:"boards", boards:[{id,players,alive,names}...]} — a board started or ended
//   {type:"game",   id, width, height, boardScoreboard, boardChartData, players:[{id,name,pos,moves,alive,chat?}]}
//   {type:"tick",   gameId, positions:[[id,x,y]...], deaths?:[id], chats?:{id:msg}}
//   {type:"end",    gameId, scoreboard, chartData, lastWinners}
//   {type:"misc",   content:"shutdown"} — lifecycle event; "shutdown" → banner.
//
// chartData is a 20-point series; each point is { name: i, [username]: elo, ... }.
// Players whose ScoreHistory predates elo tracking will be missing from the
// earlier points until enough new games have been played.

const gameState = {
  serverInfo: [],
  viewInfo: [],
  scoreboard: [],
  boardScoreboard: [],
  boardChartData: [],
  scoreboardScope: 'board', // 'board' | 'global' | 'spectator'
  followName: '',
  followEditing: false,
  chartData: [],
  lastWinners: [],
  boards: [], // [{ id, players, alive }] — all running boards, tab bar order
  game: null, // subscribed board: { id, width, height, players: { [id]: { id, name, pos, moves, alive, chat } } }
};

function applyMessage(msg) {
  switch (msg.type) {
    case 'init':   applyInit(msg);  break;
    case 'boards': applyBoards(msg); break;
    case 'game':   applyGame(msg);  break;
    case 'tick':   applyTick(msg);  break;
    case 'end':    applyEnd(msg);   break;
  }
}

function applyInit(msg) {
  gameState.serverInfo  = msg.serverInfo  || [];
  gameState.viewInfo    = msg.viewInfo    || [];
  gameState.scoreboard  = msg.scoreboard  || [];
  gameState.boardScoreboard = msg.game?.boardScoreboard || [];
  gameState.boardChartData  = msg.game?.boardChartData  || [];
  gameState.chartData   = msg.chartData   || [];
  gameState.lastWinners = msg.lastWinners || [];
  gameState.boards      = msg.boards      || [];
  gameState.game = msg.game ? buildGame(msg.game) : null;
}

function applyGame(msg) {
  gameState.boardScoreboard = msg.boardScoreboard || [];
  gameState.boardChartData = msg.boardChartData || [];
  gameState.game = buildGame(msg);
}

function applyBoards(msg) {
  gameState.boards = msg.boards || [];
  if (gameState.game && !gameState.boards.some((b) => b.id === gameState.game.id)) {
    gameState.game = null;
    gameState.boardScoreboard = [];
    gameState.boardChartData = [];
  }
}

function applyTick(msg) {
  const g = gameState.game;
  if (!g || msg.gameId !== g.id) return;
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
