// Pure utility functions used across the viewer. No state, no DOM, no
// network. If a helper doesn't depend on the canvas, the websocket, or any
// in-memory game state, it belongs here.

// Render a viewer ServerInfo {host, port, scheme} for display. Omits the
// port when it's the scheme default (443/https, 80/http) or absent (0), so
// inputs like "-public-view tron.erik.gdn" don't display a stray ":0".
function viewHostPort(v) {
  const host = v.host || '';
  const port = v.port || 0;
  const defaultPort = v.scheme === 'https' ? 443 : v.scheme === 'http' ? 80 : 0;
  if (!port || port === defaultPort) return host;
  return host + ':' + port;
}

function viewURL(v, path = '') {
  const hostPort = viewHostPort(v);
  if (!hostPort) return '';
  return (v.scheme || 'https') + '://' + hostPort + path;
}

// CRC-32 of a string. Used as a deterministic hash for color/palette lookups
// so a given player always lands on the same color across reloads.
function crc32(r) {
  const o = [];
  for (let c = 0; c < 256; c++) {
    let a = c;
    for (let f = 0; f < 8; f++) a = 1 & a ? 3988292384 ^ a >>> 1 : a >>> 1;
    o[c] = a;
  }
  let n = -1;
  for (let t = 0; t < r.length; t++) n = n >>> 8 ^ o[255 & (n ^ r.charCodeAt(t))];
  return (-1 ^ n) >>> 0;
}

function hexToRgb(hex) {
  const h = hex.replace('#', '');
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
}

function rgbToHsl(r, g, b) {
  r /= 255; g /= 255; b /= 255;
  const max = Math.max(r, g, b), min = Math.min(r, g, b);
  const l = (max + min) / 2;
  if (max === min) return [0, 0, l];
  const d = max - min;
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  let h;
  switch (max) {
    case r: h = (g - b) / d + (g < b ? 6 : 0); break;
    case g: h = (b - r) / d + 2; break;
    default: h = (r - g) / d + 4;
  }
  return [h / 6, s, l];
}

function hslToRgb(h, s, l) {
  if (s === 0) {
    const v = Math.round(l * 255);
    return [v, v, v];
  }
  const q = l < 0.5 ? l * (1 + s) : l + s - l * s;
  const p = 2 * l - q;
  const hue2rgb = (t) => {
    if (t < 0) t += 1;
    if (t > 1) t -= 1;
    if (t < 1 / 6) return p + (q - p) * 6 * t;
    if (t < 1 / 2) return q;
    if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6;
    return p;
  };
  return [
    Math.round(hue2rgb(h + 1 / 3) * 255),
    Math.round(hue2rgb(h) * 255),
    Math.round(hue2rgb(h - 1 / 3) * 255),
  ];
}

// WCAG-style relative-luminance pick: returns '#000' on bright backgrounds,
// '#fff' on dark ones. Used to keep the player-name pill label readable
// regardless of the bot's color.
function contrastText(rgbStr) {
  const m = rgbStr.match(/\d+/g);
  if (!m) return '#fff';
  const [r, g, b] = m.map(Number);
  const lum = (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
  return lum > 0.58 ? '#000' : '#fff';
}

// HTML escape — use whenever we put user-controlled strings into innerHTML.
function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// Settings toggles persisted in localStorage. The modal renders/owns these;
// other code just reads via getSwitch(key).
function getSwitch(key) {
  try { return localStorage.getItem('algotron.switch.' + key) === '1'; } catch (e) { return false; }
}

// Names get truncated to fit their container — the scoreboard cell or the
// canvas name pill — instead of a fixed character count. NAME_MAX is the
// fallback used when no measured width is available (canvas pills, or
// before the scoreboard has laid out). With the "scrollNames" switch on,
// the visible window scrolls so the full name eventually comes around.
const NAME_MAX = 20;
const NAME_GAP = '   ';
function displayName(name, maxChars) {
  const max = maxChars > 0 ? maxChars : NAME_MAX;
  if (name.length <= max) return name;
  if (!getSwitch('scrollNames')) return name.slice(0, max);
  const padded = name + NAME_GAP;
  const i = Math.floor(Date.now() / 250) % padded.length;
  return (padded + padded).slice(i, i + max);
}

// Number of monospace characters that fit inside `el` at its computed font,
// minus horizontal padding. The viewer's font stack is monospace so this is
// stable across columns. Returns 0 if the element isn't measurable yet.
const _charCanvas = document.createElement('canvas');
function fitChars(el) {
  if (!el) return 0;
  const style = getComputedStyle(el);
  const ctx = _charCanvas.getContext('2d');
  ctx.font = style.font || (style.fontSize + ' ' + style.fontFamily);
  const cw = ctx.measureText('M').width;
  if (!cw) return 0;
  const padL = parseFloat(style.paddingLeft) || 0;
  const padR = parseFloat(style.paddingRight) || 0;
  const avail = el.clientWidth - padL - padR;
  return Math.max(0, Math.floor(avail / cw));
}
