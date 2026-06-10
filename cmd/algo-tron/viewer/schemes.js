// Color schemes, palette expansion, and theme application.
//
// Each scheme defines 4 anchor player colors plus the CSS-variable values
// for the UI chrome (bg / text / accent / grid / etc). The anchors get
// expanded by expandPalette() into PALETTE_SIZE distinguishable variants
// (hue rotations + lightness twists) so a single scheme can seat 30+
// players without obvious color collisions.
//
// To add a new scheme: append to SCHEMES with the same shape. CSS variables
// are written via applyScheme(), which also sets body.scheme-<name> so the
// stylesheet can override structural things like font-family per scheme
// (see body.scheme-gpn in style.css).
//
// Depends on: helpers.js (crc32, hexToRgb, rgbToHsl, hslToRgb).
// Provides: SCHEMES, SCHEME_KEYS, currentScheme, applyScheme, playerColor,
// canvasFont, paletteFor.

const SCHEMES = {
  catppuccin: {
    label: 'catppuccin',
    bg: '#1e1e2e', bgElevated: '#313244', border: '#45475a',
    grid: 'rgba(205,214,244,0.15)',
    text: '#cdd6f4', textMuted: '#7f849c', textDim: '#45475a',
    accent: '#f5c2e7', live: '#a6e3a1',
    players: ['#f38ba8', '#fab387', '#a6e3a1', '#89b4fa'],
  },
  phosphor: {
    label: 'phosphor',
    bg: '#0a120a', bgElevated: '#101e10', border: '#1f3a1f',
    grid: 'rgba(0,255,102,0.15)',
    text: '#c2f5b9', textMuted: '#5a8c4d', textDim: '#2d4a26',
    accent: '#00ff66', live: '#00ff66',
    players: ['#00ff66', '#33ee88', '#66ffaa', '#00cc55'],
  },
  gruvbox: {
    label: 'gruvbox',
    bg: '#282828', bgElevated: '#3c3836', border: '#504945',
    grid: 'rgba(235,219,178,0.15)',
    text: '#ebdbb2', textMuted: '#a89984', textDim: '#665c54',
    accent: '#fe8019', live: '#b8bb26',
    players: ['#fb4934', '#fabd2f', '#b8bb26', '#83a598'],
  },
  nord: {
    label: 'nord',
    bg: '#2e3440', bgElevated: '#3b4252', border: '#434c5e',
    grid: 'rgba(216,222,233,0.15)',
    text: '#d8dee9', textMuted: '#7b8497', textDim: '#4c566a',
    accent: '#88c0d0', live: '#a3be8c',
    players: ['#bf616a', '#ebcb8b', '#a3be8c', '#88c0d0'],
  },
  amber: {
    label: 'amber',
    bg: '#1a0f00', bgElevated: '#2d1a00', border: '#4d2e00',
    grid: 'rgba(255,176,0,0.15)',
    text: '#ffb000', textMuted: '#8c5e00', textDim: '#3d2900',
    accent: '#ff8c00', live: '#ffd700',
    players: ['#ffb000', '#ff8c00', '#ffd700', '#ff6f00'],
  },
  gpn: {
    label: 'gpn',
    bg: '#000020', bgElevated: '#000040', border: '#1a1a55',
    grid: 'rgba(255,255,255,0.32)',
    text: '#ffffff', textMuted: '#9a9ab8', textDim: '#3a3a5c',
    accent: '#66ffcc', live: '#66ffcc',
    players: ['#556b2f', '#5fffd4', '#3030ff', '#aa3399'],
    font: '"Times New Roman", Georgia, serif',
  },
  'gruvbox-light': {
    label: 'gruvbox-light',
    bg: '#fbf1c7', bgElevated: '#ebdbb2', border: '#d5c4a1',
    grid: 'rgba(60,56,54,0.16)',
    text: '#3c3836', textMuted: '#7c6f64', textDim: '#bdae93',
    accent: '#af3a03', live: '#79740e',
    players: ['#9d0006', '#b57614', '#79740e', '#076678'],
  },
  latte: {
    label: 'latte',
    bg: '#eff1f5', bgElevated: '#e6e9ef', border: '#dce0e8',
    grid: 'rgba(76,79,105,0.16)',
    text: '#4c4f69', textMuted: '#7c7f93', textDim: '#bcc0cc',
    accent: '#ea76cb', live: '#40a02b',
    players: ['#d20f39', '#df8e1d', '#40a02b', '#1e66f5'],
  },
  zen: {
    label: 'zen',
    bg: '#16161d', bgElevated: '#1f1f28', border: '#2a2a37',
    grid: 'rgba(220,215,186,0.14)',
    text: '#dcd7ba', textMuted: '#727169', textDim: '#3a3a40',
    accent: '#7fb4ca', live: '#98bb6c',
    players: ['#c34043', '#dca561', '#76946a', '#7e9cd8'],
  },
  'zen-light': {
    label: 'zen-light',
    bg: '#f2ecbc', bgElevated: '#e5e0b8', border: '#c8c093',
    grid: 'rgba(84,84,100,0.17)',
    text: '#545464', textMuted: '#8a8980', textDim: '#a09a82',
    accent: '#4d699b', live: '#6f894e',
    players: ['#c84053', '#cc6d00', '#6f894e', '#4d699b'],
  },
};

const SCHEME_KEYS = Object.keys(SCHEMES);
const DEFAULT_SCHEME = 'catppuccin';
let currentScheme = DEFAULT_SCHEME;

function applyScheme(name) {
  const s = SCHEMES[name];
  if (!s) return;
  currentScheme = name;
  const r = document.documentElement.style;
  r.setProperty('--bg', s.bg);
  r.setProperty('--bg-elevated', s.bgElevated);
  r.setProperty('--border', s.border);
  r.setProperty('--grid', s.grid);
  r.setProperty('--text', s.text);
  r.setProperty('--text-muted', s.textMuted);
  r.setProperty('--text-dim', s.textDim);
  r.setProperty('--accent', s.accent);
  r.setProperty('--live', s.live);

  for (const k of SCHEME_KEYS) document.body.classList.remove('scheme-' + k);
  document.body.classList.add('scheme-' + name);

  try { localStorage.setItem('algotron.scheme', name); } catch (e) {}
  document.querySelectorAll('.scheme').forEach((el) => {
    el.classList.toggle('active', el.dataset.scheme === name);
  });
}

// expandPalette derives PALETTE_SIZE distinguishable colors from a scheme's
// 4 anchors by rotating hue and twisting lightness. Result is cached per
// scheme so we only compute each palette once.
const PALETTE_SIZE = 32;
const HUE_SHIFTS = [0, 18, -18, 36, -36, 9, -9, 27];
const LIGHT_TWISTS = [0, 0.10, -0.10, 0.18, -0.18, 0.05, -0.05, 0.13];

function expandPalette(base) {
  const perAnchor = Math.floor(PALETTE_SIZE / base.length);
  const out = [];
  for (const hex of base) {
    const [r, g, b] = hexToRgb(hex);
    const [h, s, l] = rgbToHsl(r, g, b);
    for (let i = 0; i < perAnchor; i++) {
      const newH = (((h * 360) + HUE_SHIFTS[i % HUE_SHIFTS.length]) + 360) % 360 / 360;
      const newL = Math.max(0.22, Math.min(0.82, l + LIGHT_TWISTS[i % LIGHT_TWISTS.length]));
      const [nr, ng, nb] = hslToRgb(newH, s, newL);
      out.push(`rgb(${nr}, ${ng}, ${nb})`);
    }
  }
  return out;
}

const paletteCache = {};
function paletteFor(scheme) {
  if (!paletteCache[scheme]) paletteCache[scheme] = expandPalette(SCHEMES[scheme].players);
  return paletteCache[scheme];
}

// playerColor is deterministic per name. Joining/leaving never reshuffles
// existing players' colors.
function playerColor(name) {
  const palette = paletteFor(currentScheme);
  return palette[crc32(name) % palette.length];
}

// canvasFont builds a canvas font string honoring the current scheme's
// font family (gpn switches the arena to serif together with the UI).
function canvasFont(px, weight) {
  const s = SCHEMES[currentScheme];
  const fam = s.font || '"Roboto Mono", ui-monospace, monospace';
  return `${weight || ''} ${px}px ${fam}`.trim();
}

// Restore the persisted scheme (or honor a #scheme=... hash override) as
// early as possible so the UI doesn't flash the default first.
(function initScheme() {
  const hashMatch = location.hash.match(/scheme=([\w-]+)/);
  if (hashMatch && SCHEMES[hashMatch[1]]) {
    applyScheme(hashMatch[1]);
    return;
  }
  let saved = null;
  try { saved = localStorage.getItem('algotron.scheme'); } catch (e) {}
  applyScheme(saved && SCHEMES[saved] ? saved : DEFAULT_SCHEME);
})();
