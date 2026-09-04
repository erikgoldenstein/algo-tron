// Admin login modal and short-lived admin-session indicator.
//
// Depends on: modal.js (toggleHelp).
// Provides: openAdminLogin, closeAdminLogin, refreshAdminStatus.

let adminLoginAttempt = '';
let adminLoginBusy = false;
let adminStatusRequestID = 0;

function renderAdminStatus(isAdmin) {
  const section = document.getElementById('admin-section');
  if (section) section.hidden = !isAdmin;
  if (!isAdmin) {
    const panel = document.getElementById('admin-lobbies');
    if (panel) panel.hidden = true;
  }
}

function setAdminLobbyStatus(message, error) {
  const status = document.getElementById('admin-lobby-status');
  if (!status) return;
  status.textContent = message || '';
  status.classList.toggle('error', !!error);
}

function renderAdminLobbies(lobbies) {
  const root = document.getElementById('admin-lobby-list');
  if (!root) return;
  root.replaceChildren();
  if (!lobbies.length) {
    const empty = document.createElement('div');
    empty.className = 'muted';
    empty.textContent = 'no named lobbies';
    root.appendChild(empty);
    return;
  }
  lobbies.forEach((lobby) => {
    const row = document.createElement('div');
    row.className = 'admin-lobby-row';
    const name = document.createElement('span');
    name.className = 'admin-lobby-name';
    name.textContent = lobby.name;
    const info = document.createElement('span');
    info.className = 'muted';
    info.textContent = (lobby.passwordRequired ? 'locked' : 'open') + ' · max ' + lobby.maxPlayersPerBoard + ' · ' + lobby.activePlayers + ' online';
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'admin-lobby-remove';
    remove.textContent = 'remove';
    remove.addEventListener('click', () => removeAdminLobby(lobby.name));
    row.append(name, info, remove);
    root.appendChild(row);
  });
}

function loadAdminLobbies() {
  return fetch('/api/admin/lobbies', { cache: 'no-store', credentials: 'same-origin' })
    .then((response) => {
      if (response.status === 401) {
        renderAdminStatus(false);
        throw new Error('admin session expired');
      }
      if (!response.ok) throw new Error('could not load lobbies');
      return response.json();
    })
    .then((lobbies) => {
      renderAdminLobbies(lobbies);
      setAdminLobbyStatus('');
      return lobbies;
    })
    .catch((error) => {
      setAdminLobbyStatus(error.message || 'could not load lobbies', true);
      return null;
    });
}

function removeAdminLobby(name) {
  setAdminLobbyStatus('removing...');
  return fetch('/api/admin/lobbies/' + encodeURIComponent(name), {
    method: 'DELETE',
    credentials: 'same-origin',
  })
    .then((response) => {
      if (response.status === 401) {
        renderAdminStatus(false);
        throw new Error('admin session expired');
      }
      if (!response.ok) throw new Error('could not remove lobby');
    })
    .then(() => loadAdminLobbies())
    .catch((error) => setAdminLobbyStatus(error.message || 'could not remove lobby', true));
}

function createAdminLobby(event) {
  event.preventDefault();
  const name = document.getElementById('admin-lobby-name')?.value || '';
  const password = document.getElementById('admin-lobby-password')?.value || '';
  const max = Number(document.getElementById('admin-lobby-max')?.value || 24);
  setAdminLobbyStatus('creating...');
  return fetch('/api/admin/lobbies', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify({ name, password, maxPlayersPerBoard: max }),
  })
    .then((response) => {
      if (response.status === 401) {
        renderAdminStatus(false);
        throw new Error('admin session expired');
      }
      if (!response.ok) throw new Error('could not create lobby');
      return response.json();
    })
    .then(() => {
      document.getElementById('admin-lobby-create')?.reset();
      const maxInput = document.getElementById('admin-lobby-max');
      if (maxInput) maxInput.value = '24';
      return loadAdminLobbies();
    })
    .catch((error) => setAdminLobbyStatus(error.message || 'could not create lobby', true));
}

function refreshAdminStatus() {
  const requestID = ++adminStatusRequestID;
  return fetch('/api/admin/status', { cache: 'no-store' })
    .then((response) => response.ok ? response.json() : { admin: false })
    .then((data) => {
      if (requestID !== adminStatusRequestID) return false;
      renderAdminStatus(data.admin === true);
      return data.admin === true;
    })
    .catch(() => {
      if (requestID !== adminStatusRequestID) return false;
      renderAdminStatus(false);
      return false;
    });
}

function openAdminLogin() {
  const modal = document.getElementById('admin-login-modal');
  const input = document.getElementById('admin-password');
  const status = document.getElementById('admin-login-status');
  if (!modal || !input || !status) return;
  adminLoginAttempt = '';
  adminLoginBusy = false;
  input.value = '';
  status.textContent = 'enter 64 characters';
  status.classList.remove('error');
  modal.hidden = false;
  input.focus();
}

function closeAdminLogin() {
  const modal = document.getElementById('admin-login-modal');
  if (modal) modal.hidden = true;
}

function submitAdminLogin(password) {
  if (adminLoginBusy) return;
  adminLoginBusy = true;
  const status = document.getElementById('admin-login-status');
  if (status) {
    status.textContent = 'checking...';
    status.classList.remove('error');
  }
  fetch('/api/admin/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify({ password }),
  })
    .then((response) => {
      if (!response.ok) throw new Error('invalid password');
      return response.json();
    })
    .then(() => {
      adminStatusRequestID++;
      renderAdminStatus(true);
      closeAdminLogin();
    })
    .catch((error) => {
      adminLoginBusy = false;
      if (status) {
        status.textContent = error.message || 'login failed';
        status.classList.add('error');
      }
    });
}

document.addEventListener('DOMContentLoaded', () => {
  document.getElementById('admin-hotspot')?.addEventListener('click', openAdminLogin);
  document.querySelectorAll('[data-admin-close]').forEach((el) => {
    el.addEventListener('click', closeAdminLogin);
  });
  document.getElementById('admin-lobbies-toggle')?.addEventListener('click', () => {
    const panel = document.getElementById('admin-lobbies');
    if (!panel) return;
    panel.hidden = !panel.hidden;
    if (!panel.hidden) loadAdminLobbies();
  });
  document.getElementById('admin-lobby-create')?.addEventListener('submit', createAdminLobby);
  document.getElementById('admin-password')?.addEventListener('input', (event) => {
    const password = event.target.value;
    const status = document.getElementById('admin-login-status');
    if (password.length < 64) {
      adminLoginAttempt = '';
      if (status) {
        status.textContent = `${password.length}/64`;
        status.classList.remove('error');
      }
      return;
    }
    if (password === adminLoginAttempt) return;
    adminLoginAttempt = password;
    submitAdminLogin(password);
  });
});
