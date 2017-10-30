/* global Headers, fetch */
(function (window, document) {
  if (!window.fetch || !window.Headers || !window.FormData) return

  const loginForm = document.getElementById('login-form')
  const resetForm = document.getElementById('renew-passphrase-form')

  const url = loginForm && loginForm.getAttribute('action')

  const passphraseInput = document.getElementById('password')
  const redirectInput = document.getElementById('redirect')
  const submitButton = document.getElementById('login-submit')
  const twoFactorPasscodeInput = document.getElementById('two-factor-passcode')
  const twoFactorTokenInput = document.getElementById('two-factor-token')
  const twoFactorForm = document.getElementById('two-factor-form')
  const passwordForm = document.getElementById("password-form")

  let errorPanel = loginForm && loginForm.querySelector('.errors')

  const showError = function (error) {
    if (error) {
      error = '' + error
    } else {
      error = 'The Cozy server is unavailable. Do you have network?'
    }

    if (!errorPanel) {
      errorPanel = document.createElement('div')
      errorPanel.classList.add('errors')
      loginForm.appendChild(errorPanel)
    }

    if (!errorPanel.firstChild) {
      errorPanel.appendChild(document.createElement('p'))
    }

    errorPanel.firstChild.textContent = error;
    submitButton.removeAttribute('disabled')
  }

  const onSubmitPassphrase = function(event) {
    event.preventDefault()
    submitButton.setAttribute('disabled', true)

    const passphrase = passphraseInput.value
    const redirect = redirectInput.value + window.location.hash
    let headers = new Headers()
    headers.append('Content-Type', 'application/x-www-form-urlencoded')
    headers.append('Accept', 'application/json')
    fetch('/auth/login', {
      method: 'POST',
      headers: headers,
      body: 'passphrase=' + encodeURIComponent(passphrase) + '&redirect=' + encodeURIComponent(redirect),
      credentials: 'same-origin'
    }).then((response) => {
      const loginSuccess = response.status < 400
      response.json().then((body) => {
        if (loginSuccess) {
          if (body.two_factor_token) {
            renderTwoFactorForm(body.two_factor_token)
            return
          }
          submitButton.innerHTML = '<svg width="16" height="16"><use xlink:href="#fa-check"/></svg>'
          submitButton.classList.add('btn-success')
          if (body.redirect) {
            window.location = body.redirect
          } else {
            form.submit()
          }
        } else {
          showError(body.error)
          passphraseInput.select()
        }
      }).catch(showError)
    }).catch(showError)
  }

  const onSubmitTwoFactorCode = function(event) {
    event.preventDefault()
    submitButton.setAttribute('disabled', true)

    const passcode = twoFactorPasscodeInput.value
    const token = twoFactorTokenInput.value
    const redirect = redirectInput.value + window.location.hash
    let headers = new Headers()
    headers.append('Content-Type', 'application/x-www-form-urlencoded')
    headers.append('Accept', 'application/json')
    const reqBody = encodeURIComponent('two-factor-passcode') + '=' + encodeURIComponent(passcode) +
      '&' + encodeURIComponent('two-factor-token') + '=' + encodeURIComponent(token) +
      '&' + encodeURIComponent('redirect') + '=' + encodeURIComponent(redirect);
    fetch('/auth/login', {
      method: 'POST',
      headers: headers,
      body: reqBody,
      credentials: 'same-origin'
    }).then((response) => {
      const loginSuccess = response.status < 400
      response.json().then((body) => {
        if (loginSuccess) {
          submitButton.innerHTML = '<svg width="16" height="16"><use xlink:href="#fa-check"/></svg>'
          submitButton.classList.add('btn-success')
          if (body.redirect) {
            window.location = body.redirect
          } else {
            form.submit()
          }
        } else {
          showError(body.error)
          twoFactorPasscodeInput.select()
        }
      }).catch(showError)
    }).catch(showError)
  }

  function renderTwoFactorForm(twoFactorToken) {
    twoFactorForm.classList.remove('display-none')
    passwordForm.classList.add('display-none')
    submitButton.removeAttribute('disabled')
    twoFactorTokenInput.value = twoFactorToken
    twoFactorPasscodeInput.value = ''
    twoFactorPasscodeInput.focus()
    loginForm.removeEventListener('submit', onSubmitPassphrase)
    loginForm.addEventListener('submit', onSubmitTwoFactorCode)
  }

  loginForm && loginForm.addEventListener('submit', onSubmitPassphrase)

  resetForm && resetForm.addEventListener('submit', (event) => {
    event.preventDefault()
    const { label } = window.password.getStrength(passphraseInput.value)
    if (label == 'weak') {
      return false
    } else {
      resetForm.submit()
    }
  })

  resetForm && passphraseInput.addEventListener('input', function(event) {
    const { label } = window.password.getStrength(event.target.value)
    submitButton[label == 'weak' ? 'setAttribute' : 'removeAttribute']('disabled', '')
  })

  passphraseInput.focus()
  loginForm && submitButton.removeAttribute('disabled')
})(window, document)
