;(function (w, d) {
  if (!w.fetch || !w.Headers) return

  const loginForm = d.getElementById('login-form')
  const loginField = d.getElementById('login-field')
  const redirectInput = d.getElementById('redirect')
  const stateInput = d.getElementById('state')
  const clientIdInput = d.getElementById('client_id')
  const submitButton = d.getElementById('login-submit')
  const twoFactorPasscodeInput = d.getElementById('two-factor-passcode')
  const twoFactorTokenInput = d.getElementById('two-factor-token')
  const twoFactorTrustDeviceCheckbox = d.getElementById(
    'two-factor-trust-device'
  )
  const longRunSessionCheckbox = d.getElementById('long-run-session')

  let errorPanel = loginForm && loginForm.querySelector('.wizard-errors')

  const twoFactorTrustedDeviceTokenKey = 'two-factor-trusted-device-token'
  let localStorage = null
  try {
    localStorage = w.localStorage
  } catch (e) {
    // do nothing
  }

  const showError = function (message) {
    let error = 'The Cozy server is unavailable. Do you have network?'
    if (message) {
      error = '' + message
    }

    if (!errorPanel) {
      errorPanel = d.createElement('p')
      errorPanel.classList.add('wizard-errors', 'u-error')
      loginField.insertBefore(errorPanel, loginField.firstChild)
    }

    errorPanel.textContent = error
    submitButton.removeAttribute('disabled')
  }

  const onSubmitTwoFactorCode = function (event) {
    event.preventDefault()
    submitButton.setAttribute('disabled', true)

    const longRunSession =
      longRunSessionCheckbox && longRunSessionCheckbox.checked ? '1' : '0'
    const passcode = twoFactorPasscodeInput.value
    const token = twoFactorTokenInput.value
    const trustDevice = twoFactorTrustDeviceCheckbox.checked ? '1' : '0'
    const redirect = redirectInput.value + w.location.hash

    const headers = new Headers()
    headers.append('Content-Type', 'application/x-www-form-urlencoded')
    headers.append('Accept', 'application/json')

    let reqBody =
      'two-factor-passcode=' +
      encodeURIComponent(passcode) +
      '&long-run-session=' +
      encodeURIComponent(longRunSession) +
      '&two-factor-token=' +
      encodeURIComponent(token) +
      '&two-factor-generate-trusted-device-token=' +
      encodeURIComponent(trustDevice) +
      '&redirect=' +
      encodeURIComponent(redirect)

    // When 2FA is checked for moving a Cozy to this instance
    if (stateInput) {
      reqBody += '&state=' + encodeURIComponent(stateInput.value)
    }
    if (clientIdInput) {
      reqBody += '&client_id=' + encodeURIComponent(clientIdInput.value)
    }

    fetch('/auth/twofactor', {
      method: 'POST',
      headers: headers,
      body: reqBody,
      credentials: 'same-origin',
    })
      .then(function (response) {
        const loginSuccess = response.status < 400

        response
          .json()
          .then(function (body) {
            if (loginSuccess) {
              if (
                localStorage &&
                typeof body.two_factor_trusted_device_token == 'string'
              ) {
                localStorage.setItem(
                  twoFactorTrustedDeviceTokenKey,
                  body.two_factor_trusted_device_token
                )
              }
              if (body.redirect) {
                w.location = body.redirect
              }
            } else {
              showError(body.error)
              twoFactorPasscodeInput.classList.add('is-error')
              twoFactorPasscodeInput.select()
            }
          })
          .catch(showError)
      })
      .catch(showError)
  }

  loginForm.addEventListener('submit', onSubmitTwoFactorCode)
})(window, document)
