'use strict';

/* Страница входа/регистрации (/login.html). Шапка (имя пользователя, выход) — общая
 * из shared.js; после успеха возвращаемся на ?next= (или в каталог). */

const authTitle = document.getElementById('auth-title');
const authTabLogin = document.getElementById('auth-tab-login');
const authTabRegister = document.getElementById('auth-tab-register');
const authForm = document.getElementById('auth-form');
const authUsername = document.getElementById('auth-username');
const authPassword = document.getElementById('auth-password');
const authError = document.getElementById('auth-error');
const authSubmit = document.getElementById('auth-submit');

const loginParams = new URLSearchParams(location.search);
// ?next= — только свой относительный путь («//host» — внешний адрес, не пускаем).
let nextUrl = loginParams.get('next') || '/';
if (nextUrl.indexOf('/') !== 0 || nextUrl.indexOf('//') === 0) nextUrl = '/';
let authMode = loginParams.get('mode') === 'register' ? 'register' : 'login';

function updateAuthTabs() {
  authTabLogin.classList.toggle('active', authMode === 'login');
  authTabRegister.classList.toggle('active', authMode === 'register');
  authTitle.textContent = authMode === 'login' ? t('login') : t('register');
  authSubmit.textContent = authMode === 'login' ? t('login') : t('register');
  authPassword.autocomplete = authMode === 'login' ? 'current-password' : 'new-password';
}

authTabLogin.addEventListener('click', () => { authMode = 'login'; updateAuthTabs(); });
authTabRegister.addEventListener('click', () => { authMode = 'register'; updateAuthTabs(); });

authForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  authError.hidden = true;
  const username = authUsername.value.trim();
  const password = authPassword.value;
  if (!/^[a-zA-Z0-9_.-]{3,32}$/.test(username) || password.length < 6) {
    authError.textContent = t('authErrorShort');
    authError.hidden = false;
    return;
  }
  authSubmit.disabled = true;
  try {
    const res = await fetch('/api/auth/' + (authMode === 'login' ? 'login' : 'register'), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    });
    if (res.status === 503) {
      authError.textContent = t('authErrorAuth');
      authError.hidden = false;
      return;
    }
    if (res.status === 409) {
      authError.textContent = t('authErrorTaken');
      authError.hidden = false;
      return;
    }
    if (!res.ok) {
      authError.textContent = res.status === 401 ? t('authErrorInvalid') : t('authErrorServer');
      authError.hidden = false;
      return;
    }
    location.href = nextUrl;
  } catch (err) {
    authError.textContent = t('authErrorServer');
    authError.hidden = false;
  } finally {
    authSubmit.disabled = false;
  }
});

onLang(updateAuthTabs);

applyLang();
updateAuthTabs();
authUsername.focus();
initAuth();
