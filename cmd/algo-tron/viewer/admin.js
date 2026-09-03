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
