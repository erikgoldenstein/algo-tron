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
//   {type:"init",   serverInfo, viewInfo, scoreboard, chartData, lastWinners, boards, lobbies, chat, game?}
//   {type:"boards", boards:[{id,tick,players,alive,names}...], lobbies:[...]} — board/lobby state
//   {type:"game",   id, width, height, boardScoreboard, boardChartData, players:[{id,name,version?,bio?,pos,moves,alive,chat?}]}
//   {type:"tick",   gameId, positions:[[id,x,y]...], deaths?:[id], chats?:{id:msg}}
//   {type:"end",    gameId, scoreboard, chartData, lastWinners}
//   {type:"chat_snapshot", messages:[...]} — current chat subscription history.
//   {type:"misc",   content:"shutdown"} — lifecycle event; "shutdown" → banner.
//
// chartData is a 20-point series; each point is { name: i, [username]: elo, ... }.
// Players whose ScoreHistory predates elo tracking will be missing from the
// earlier points until enough new games have been played.

const screenMode = location.pathname.replace(/\/+$/, '') === '/screen';
const initialLobbyPreference = screenMode
  ? (new URLSearchParams(location.search).get('lobby') || '').trim()
  : '';

const gameState = {
  screenMode,
  serverInfo: [],
  viewInfo: [],
  scoreboard: [],
  boardScoreboard: [],
  boardChartData: [],
  scoreboardScope: screenMode ? 'global' : 'board', // 'board' | 'lobby' | 'global'
  scoreboardLobby: '',
  chatScope: 'board', // 'board' | 'lobby' | 'global'; screen mode keeps chat local
  chatLobby: '',
  lobbies: null,
  lobbyPreference: initialLobbyPreference,
  autoLobby: '',
  globalPlayers: null,
  globalAlive: null,
  followName: '',
  followEditing: false,
  chartData: [],
  lastWinners: [],
  chatLog: [],
  lobbyScoreboards: {},
  lobbyStats: {},
  lobbyChartData: {},
  scorePages: {},
  boards: [], // [{ id, tick, players, alive }] — all running boards from the server
  game: null, // subscribed board: { id, width, height, players: { [id]: { id, name, pos, moves, alive, chat } } }
};

// Keep board navigation and the tab bar in the same stable order: lobby name
// alphabetically, then the numeric board suffix. The server's board order is
// still used for lifecycle decisions; this is only the viewer's presentation
// order.
function orderedBoards() {
  return gameState.boards
    .map((board, index) => ({ board, index }))
    .sort((a, b) => {
      const lobbyA = String(a.board.lobby || 'default').toLocaleLowerCase();
      const lobbyB = String(b.board.lobby || 'default').toLocaleLowerCase();
      if (lobbyA !== lobbyB) return lobbyA < lobbyB ? -1 : 1;

      const numberA = Number(String(a.board.label || '').match(/-(\d+)$/)?.[1]);
      const numberB = Number(String(b.board.label || '').match(/-(\d+)$/)?.[1]);
      if (Number.isFinite(numberA) && Number.isFinite(numberB) && numberA !== numberB) {
        return numberA - numberB;
      }
      return a.index - b.index;
    })
    .map((entry) => entry.board);
}

function applyMessage(msg) {
  switch (msg.type) {
    case 'init':   applyInit(msg);  break;
    case 'boards': applyBoards(msg); break;
    case 'game':   applyGame(msg);  break;
    case 'tick':   applyTick(msg);  break;
    case 'end':    applyEnd(msg);   break;
    case 'scoreboard': applyScoreboard(msg); break;
    case 'chat':   applyChat(msg);  break;
    case 'chat_snapshot': applyChatSnapshot(msg); break;
  }
}

function applyInit(msg) {
  gameState.serverInfo  = msg.serverInfo  || [];
  gameState.viewInfo    = msg.viewInfo    || [];
  gameState.scoreboard  = msg.scoreboard  || [];
  gameState.scorePages[scorePageKey('online', 'ts', '', '')] = { entries: gameState.scoreboard.slice(), hasMore: !!msg.scoreboardHasMore, period: 'online', sort: 'ts', search: '', lobby: '', computedAt: msg.computedAt || Date.now() };
  gameState.boardScoreboard = msg.game?.boardScoreboard || [];
  gameState.boardChartData  = msg.game?.boardChartData  || [];
  gameState.chartData   = msg.chartData   || [];
  gameState.lastWinners = msg.lastWinners || [];
  gameState.boards      = msg.boards      || [];
  gameState.lobbies = Array.isArray(msg.lobbies) ? msg.lobbies : null;
  gameState.chatLog = msg.chat || [];
  gameState.globalPlayers = Number.isFinite(msg.globalPlayers) ? msg.globalPlayers : null;
  gameState.globalAlive = Number.isFinite(msg.globalAlive) ? msg.globalAlive : null;
  gameState.game = msg.game ? buildGame(msg.game) : null;
}

function applyGame(msg) {
  gameState.boardScoreboard = msg.boardScoreboard || [];
  gameState.boardChartData = msg.boardChartData || [];
  gameState.game = buildGame(msg);
}

function applyBoards(msg) {
  gameState.boards = msg.boards || [];
  gameState.lobbies = Array.isArray(msg.lobbies) ? msg.lobbies : null;
  gameState.globalPlayers = Number.isFinite(msg.globalPlayers) ? msg.globalPlayers : null;
  gameState.globalAlive = Number.isFinite(msg.globalAlive) ? msg.globalAlive : null;
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
  if (msg.scoreboardScope === 'global') {
    gameState.scoreboard = msg.scoreboard || [];
    gameState.scorePages[scorePageKey('online', 'ts', '', '')] = { entries: gameState.scoreboard.slice(), hasMore: !!msg.scoreboardHasMore, period: 'online', sort: 'ts', search: '', lobby: '', computedAt: msg.computedAt || Date.now() };
    gameState.chartData = msg.chartData || [];
  } else if (msg.scoreboardScope === 'lobby' && msg.lobby) {
    gameState.lobbyScoreboards[msg.lobby] = msg.scoreboard || [];
    if (msg.chartData) gameState.lobbyChartData[msg.lobby] = msg.chartData;
    gameState.scorePages[scorePageKey('online', 'ts', '', msg.lobby)] = { entries: gameState.lobbyScoreboards[msg.lobby].slice(), hasMore: !!msg.scoreboardHasMore, period: 'online', sort: 'ts', search: '', lobby: msg.lobby, computedAt: msg.computedAt || Date.now() };
  }
  gameState.lastWinners = msg.lastWinners || [];
}

function applyScoreboard(msg) {
  storeScoreboardPage(msg, true);
}

function applyChatSnapshot(msg) {
  gameState.chatLog = (msg.messages || []).slice(-100);
}

function watchedLobby() {
  return gameState.game?.lobby
    || gameState.boards.find((board) => board.id === gameState.game?.id)?.lobby
    || '';
}

function storeScoreboardPage(msg, updateLive) {
  const key = scorePageKey(msg.period, msg.sort, msg.search, msg.lobby || '');
  const prev = msg.offset ? (gameState.scorePages[key]?.entries || []) : [];
  gameState.scorePages[key] = {
    entries: prev.concat(msg.entries || []),
    hasMore: !!msg.hasMore,
    period: msg.period || 'online',
    sort: msg.sort || 'ts',
    search: msg.search || '',
    lobby: msg.lobby || '',
    computedAt: msg.computedAt || Date.now(),
  };
  if (updateLive && (msg.period || 'online') === 'online' && (msg.sort || 'ts') === 'ts' && !(msg.search || '')) {
    if (msg.lobby) {
      gameState.lobbyScoreboards[msg.lobby] = gameState.scorePages[key].entries;
      if (msg.chartData) gameState.lobbyChartData[msg.lobby] = msg.chartData;
      if (Number.isFinite(msg.players) && Number.isFinite(msg.alive)) {
        gameState.lobbyStats[msg.lobby] = { players: msg.players, alive: msg.alive };
      }
    } else {
      gameState.scoreboard = gameState.scorePages[key].entries;
      if (msg.chartData) gameState.chartData = msg.chartData;
    }
  }
}

function applyChat(msg) {
  gameState.chatLog.push(msg);
  if (gameState.chatLog.length > 100) gameState.chatLog.shift();
}

function scorePageKey(period, sort, search, lobby) {
  return (period || 'online') + '|' + (sort || 'ts') + '|' + (search || '') + '|' + (lobby || '');
}

function buildGame(m) {
  const players = {};
  for (const p of m.players || []) {
    players[p.id] = {
      id: p.id,
      name: p.name,
      version: p.version || '',
      bio: p.bio || {},
      pos: p.pos,
      moves: p.moves ? p.moves.slice() : [p.pos],
      alive: p.alive !== false,
      chat: p.chat || '',
    };
  }
  return { id: m.id, lobby: m.lobby || '', width: m.width, height: m.height, players };
}
