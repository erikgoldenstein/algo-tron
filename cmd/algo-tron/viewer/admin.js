// Admin login modal and short-lived admin-session indicator.
//
// Depends on: modal.js (toggleHelp).
// Provides: openAdminLogin, closeAdminLogin, refreshAdminStatus.

let adminLoginAttempt = '';
let adminLoginBusy = false;
let adminStatusRequestID = 0;
let adminSessionActive = false;
let resetPasswordUsername = '';
let resetPasswordCard = null;
let resetPasswordBusy = false;
let resetPasswordValue = '';

function renderAdminStatus(isAdmin) {
  adminSessionActive = isAdmin;
  const section = document.getElementById('admin-section');
  if (section) section.hidden = !isAdmin;
  if (!isAdmin) {
    const panel = document.getElementById('admin-lobbies');
    if (panel) panel.hidden = true;
  }
  if (typeof refreshScoreHoverCard === 'function') refreshScoreHoverCard();
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
    row.append(name, info);
    if (lobby.name !== 'default') {
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'admin-lobby-remove';
      remove.textContent = 'remove';
      remove.addEventListener('click', () => removeAdminLobby(lobby.name));
      row.appendChild(remove);
    }
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
      if (!response.ok) {
        return response.text().then((message) => {
          throw new Error(message.trim() || 'could not create lobby');
        });
      }
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

function resetAdminUserPassword(username, card) {
  if (!adminSessionActive || !username) return;
  resetPasswordUsername = username;
  resetPasswordCard = card || null;
  resetPasswordBusy = false;
  const modal = document.getElementById('reset-password-confirm-modal');
  const user = document.getElementById('reset-password-confirm-user');
  const status = document.getElementById('reset-password-confirm-status');
  const confirm = document.getElementById('reset-password-confirm');
  if (!modal || !user || !confirm) return;
  user.textContent = username;
  if (status) {
    status.textContent = '';
    status.classList.remove('error');
  }
  confirm.disabled = false;
  confirm.textContent = 'reset password';
  modal.hidden = false;
}

function closeAdminResetPasswordConfirm() {
  const modal = document.getElementById('reset-password-confirm-modal');
  if (modal) modal.hidden = true;
  if (!resetPasswordBusy) {
    resetPasswordUsername = '';
    resetPasswordCard = null;
  }
}

function setResetPasswordStatus(message, error = false) {
  const status = document.getElementById('reset-password-confirm-status');
  if (!status) return;
  status.textContent = message || '';
  status.classList.toggle('error', !!error);
}

function confirmAdminResetPassword() {
  if (resetPasswordBusy || !resetPasswordUsername) return;
  const username = resetPasswordUsername;
  const card = resetPasswordCard;
  const confirm = document.getElementById('reset-password-confirm');
  resetPasswordBusy = true;
  if (confirm) {
    confirm.disabled = true;
    confirm.textContent = 'resetting...';
  }
  const button = card?.querySelector('.score-hover-reset');
  if (button) {
    button.disabled = true;
    button.textContent = 'resetting...';
  }
  return fetch('/api/admin/users/' + encodeURIComponent(username) + '/reset-password', {
    method: 'GET',
    credentials: 'same-origin',
  })
    .then((response) => {
      if (response.status === 401) {
        renderAdminStatus(false);
        throw new Error('admin session expired');
      }
      if (!response.ok) throw new Error('could not reset password');
      return response.json();
    })
    .then((data) => {
      if (button) {
        button.disabled = false;
        button.textContent = 'reset again';
      }
      resetPasswordBusy = false;
      closeAdminResetPasswordConfirm();
      showAdminResetPassword(username, data.password);
      return data;
    })
    .catch((error) => {
      resetPasswordBusy = false;
      if (confirm) {
        confirm.disabled = false;
        confirm.textContent = 'reset password';
      }
      if (button) {
        button.disabled = false;
        button.textContent = 'reset password';
      }
      setResetPasswordStatus(error.message || 'could not reset password', true);
      return null;
    });
}

function showAdminResetPassword(username, password) {
  resetPasswordValue = password || '';
  const modal = document.getElementById('reset-password-modal');
  const user = document.getElementById('reset-password-user');
  const input = document.getElementById('reset-password-value');
  const eye = document.getElementById('reset-password-eye');
  const status = document.getElementById('reset-password-copy-status');
  if (!modal || !user || !input) return;
  user.textContent = username;
  input.type = 'password';
  input.value = resetPasswordValue;
  if (eye) {
    eye.textContent = '👁';
    eye.setAttribute('aria-label', 'show password');
    eye.title = 'show password';
  }
  if (status) {
    status.textContent = '';
    status.classList.remove('error');
  }
  modal.hidden = false;
}

function closeAdminResetPasswordModal() {
  const modal = document.getElementById('reset-password-modal');
  const input = document.getElementById('reset-password-value');
  if (modal) modal.hidden = true;
  if (input) {
    input.value = '';
    input.type = 'password';
  }
  resetPasswordValue = '';
}

function toggleAdminResetPasswordVisibility() {
  const input = document.getElementById('reset-password-value');
  const eye = document.getElementById('reset-password-eye');
  if (!input) return;
  const visible = input.type === 'text';
  input.type = visible ? 'password' : 'text';
  if (eye) {
    eye.textContent = '👁';
    eye.setAttribute('aria-label', visible ? 'show password' : 'hide password');
    eye.title = visible ? 'show password' : 'hide password';
  }
}

function copyAdminResetPassword() {
  if (!resetPasswordValue) return;
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(resetPasswordValue)
      .then(() => setResetPasswordCopyStatus(true))
      .catch(() => setResetPasswordCopyStatus(false));
    return;
  }
  const area = document.createElement('textarea');
  area.value = resetPasswordValue;
  area.setAttribute('readonly', '');
  area.style.position = 'fixed';
  area.style.opacity = '0';
  document.body.appendChild(area);
  area.select();
  const ok = document.execCommand('copy');
  area.remove();
  setResetPasswordCopyStatus(ok);
}

function setResetPasswordCopyStatus(ok) {
  const status = document.getElementById('reset-password-copy-status');
  if (!status) return;
  status.textContent = ok ? 'copied' : 'could not copy — select and copy manually';
  status.classList.toggle('error', !ok);
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
  status.textContent = '';
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
      return refreshAdminStatus();
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
  document.querySelectorAll('[data-reset-password-confirm-close]').forEach((el) => el.addEventListener('click', closeAdminResetPasswordConfirm));
  document.querySelectorAll('[data-reset-password-close]').forEach((el) => el.addEventListener('click', closeAdminResetPasswordModal));
  document.getElementById('reset-password-cancel')?.addEventListener('click', closeAdminResetPasswordConfirm);
  document.getElementById('reset-password-confirm')?.addEventListener('click', confirmAdminResetPassword);
  document.getElementById('reset-password-eye')?.addEventListener('click', toggleAdminResetPasswordVisibility);
  document.getElementById('reset-password-copy')?.addEventListener('click', copyAdminResetPassword);
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
  refreshAdminStatus();
});
