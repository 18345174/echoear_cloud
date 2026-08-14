(function () {
  'use strict'

  var state = { user: null, agents: [], connecting: '' }
  var loginView = document.getElementById('login-view')
  var agentsView = document.getElementById('agents-view')
  var loginForm = document.getElementById('login-form')
  var loginButton = document.getElementById('login-button')
  var loginError = document.getElementById('login-error')
  var logoutButton = document.getElementById('logout')
  var refreshButton = document.getElementById('refresh')
  var accountSummary = document.getElementById('account-summary')
  var agentList = document.getElementById('agent-list')
  var emptyState = document.getElementById('empty-state')
  var pageStatus = document.getElementById('page-status')

  function setStatus(message) {
    pageStatus.textContent = message || ''
  }

  function showLogin(message) {
    state.user = null
    state.agents = []
    loginView.hidden = false
    agentsView.hidden = true
    logoutButton.hidden = true
    loginError.hidden = !message
    loginError.textContent = message || ''
    setStatus('')
  }

  function showAgents() {
    loginView.hidden = true
    agentsView.hidden = false
    logoutButton.hidden = false
    accountSummary.textContent = state.user ? '已登录为 ' + state.user.username : ''
  }

  async function request(path, options) {
    var response = await fetch(path, Object.assign({
      credentials: 'same-origin',
      headers: { Accept: 'application/json' }
    }, options || {}))
    var payload
    try {
      payload = await response.json()
    } catch (_) {
      throw new Error('服务器返回了无效响应')
    }
    if (!response.ok || payload.code !== 0) {
      var error = new Error(payload.message || '请求失败')
      error.status = response.status
      throw error
    }
    return payload.data || {}
  }

  function capabilities(agent) {
    if (agent.capabilities && typeof agent.capabilities === 'object') return agent.capabilities
    try { return JSON.parse(agent.capabilities || '{}') } catch (_) { return {} }
  }

  function agentReady(agent) {
    var caps = capabilities(agent)
    return Boolean(agent.online && caps.hapi && caps.runtime_ready && agent.public_key && agent.key_id)
  }

  function renderAgents() {
    agentList.replaceChildren()
    emptyState.hidden = state.agents.length !== 0
    state.agents.forEach(function (agent) {
      var ready = agentReady(agent)
      var row = document.createElement('article')
      row.className = 'agent-row'

      var details = document.createElement('div')
      var heading = document.createElement('div')
      heading.className = 'agent-name'
      var dot = document.createElement('span')
      dot.className = 'status-dot' + (agent.online ? ' online' : '')
      dot.setAttribute('aria-label', agent.online ? '在线' : '离线')
      var name = document.createElement('strong')
      name.textContent = agent.host_name || agent.agent_id || '未命名 Agent'
      heading.append(dot, name)

      var meta = document.createElement('p')
      meta.className = 'agent-meta'
      var access = agent.access_type === 'shared' ? '共享给我的' : '我的电脑'
      var runtime = ready ? 'HAPI 可用' : (agent.online ? 'HAPI 未就绪' : '离线')
      meta.textContent = [agent.platform, access, runtime].filter(Boolean).join(' · ')
      details.append(heading, meta)

      var button = document.createElement('button')
      button.type = 'button'
      button.className = 'primary-button agent-action'
      button.textContent = state.connecting === agent.access_id ? '连接中' : '连接'
      button.disabled = !ready || Boolean(state.connecting)
      button.addEventListener('click', function () { connectAgent(agent) })
      row.append(details, button)
      agentList.append(row)
    })
  }

  async function loadAgents() {
    refreshButton.disabled = true
    setStatus('正在刷新 Agent...')
    try {
      var data = await request('/api/v1/echoear/agents')
      state.agents = Array.isArray(data.agents) ? data.agents : []
      renderAgents()
      setStatus(state.agents.length ? 'Agent 列表已更新' : '')
    } catch (error) {
      if (error.status === 401) return showLogin('登录已过期，请重新登录')
      setStatus(error.message || 'Agent 加载失败')
    } finally {
      refreshButton.disabled = false
    }
  }

  function bytesToBase64(bytes) {
    var binary = ''
    for (var index = 0; index < bytes.length; index += 1) binary += String.fromCharCode(bytes[index])
    return btoa(binary)
  }

  function base64ToBytes(value) {
    var binary = atob(value)
    var bytes = new Uint8Array(binary.length)
    for (var index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
    return bytes
  }

  async function encryptedConnectionRequest(agent) {
    if (!window.crypto || !window.crypto.subtle) throw new Error('当前浏览器不支持安全连接所需的 Web Crypto')
    var requestId = 'hapi-' + crypto.randomUUID()
    var keyBytes = crypto.getRandomValues(new Uint8Array(32))
    var nonce = crypto.getRandomValues(new Uint8Array(12))
    var aad = 'echoear-control-v2|' + agent.agent_id + '|hapi_connection||' + requestId + '|' + agent.key_id
    var pem = String(agent.public_key || '').replace('-----BEGIN PUBLIC KEY-----', '').replace('-----END PUBLIC KEY-----', '').replace(/\s/g, '')
    var publicKey = await crypto.subtle.importKey(
      'spki', base64ToBytes(pem), { name: 'RSA-OAEP', hash: 'SHA-256' }, false, ['encrypt']
    )
    var aesKey = await crypto.subtle.importKey('raw', keyBytes, { name: 'AES-GCM' }, false, ['encrypt', 'decrypt'])
    var plaintext = new TextEncoder().encode(JSON.stringify({
      version: 2, operation: 'hapi_connection', task_id: '', request_id: requestId
    }))
    var ciphertext = await crypto.subtle.encrypt(
      { name: 'AES-GCM', iv: nonce, additionalData: new TextEncoder().encode(aad), tagLength: 128 },
      aesKey,
      plaintext
    )
    var encryptedKey = await crypto.subtle.encrypt({ name: 'RSA-OAEP' }, publicKey, keyBytes)
    return {
      requestId: requestId,
      aesKey: aesKey,
      body: {
        request_id: requestId,
        envelope: {
          version: 2,
          operation: 'hapi_connection',
          task_id: '',
          request_id: requestId,
          algorithm: 'RSA-OAEP-256+A256GCM',
          key_id: agent.key_id,
          encrypted_key: bytesToBase64(new Uint8Array(encryptedKey)),
          nonce: bytesToBase64(nonce),
          ciphertext: bytesToBase64(new Uint8Array(ciphertext)),
          aad: aad
        }
      }
    }
  }

  async function waitForConnection(agent, encrypted) {
    var connectionPath = '/api/v1/echoear/agents/' + encodeURIComponent(agent.access_id) + '/hapi/connection'
    var first = await request(connectionPath, {
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify(encrypted.body)
    })
    if (first.encrypted_payload) return first.encrypted_payload

    var responsePath = '/api/v1/echoear/agents/' + encodeURIComponent(agent.access_id) + '/hapi/responses/' + encodeURIComponent(encrypted.requestId)
    for (var attempt = 0; attempt < 20; attempt += 1) {
      await new Promise(function (resolve) { setTimeout(resolve, 500) })
      try {
        var result = await request(responsePath)
        if (result.encrypted_payload) return result.encrypted_payload
      } catch (error) {
        if (error.status !== 404) throw error
      }
    }
    throw new Error('电脑未及时返回 HAPI 连接信息')
  }

  async function decryptDescriptor(agent, encrypted, rawBlob) {
    var blob = typeof rawBlob === 'string' ? JSON.parse(rawBlob) : rawBlob
    var aad = 'echoear-response-v1|' + agent.agent_id + '|hapi_connection|' + encrypted.requestId
    if (!blob || blob.version !== 1 || blob.aad !== aad) throw new Error('HAPI 连接响应校验失败')
    var plaintext = await crypto.subtle.decrypt(
      {
        name: 'AES-GCM',
        iv: base64ToBytes(blob.nonce),
        additionalData: new TextEncoder().encode(aad),
        tagLength: 128
      },
      encrypted.aesKey,
      base64ToBytes(blob.ciphertext)
    )
    var descriptor = JSON.parse(new TextDecoder().decode(plaintext))
    if (descriptor.request_id !== encrypted.requestId || descriptor.runtime !== 'hapi' || !descriptor.access_token) {
      throw new Error('电脑返回的 HAPI 连接信息不完整')
    }
    return descriptor
  }

  async function connectAgent(agent) {
    state.connecting = agent.access_id
    renderAgents()
    setStatus('正在建立端到端加密连接...')
    try {
      var encrypted = await encryptedConnectionRequest(agent)
      var blob = await waitForConnection(agent, encrypted)
      var descriptor = await decryptDescriptor(agent, encrypted, blob)
      var gateway = window.location.origin + '/api/v1/echoear/agents/' + encodeURIComponent(agent.access_id) + '/hapi/gateway/' + encodeURIComponent(encrypted.requestId)
      setStatus('连接成功，正在打开 HAPI...')
      window.location.assign('/hapi/?hub=' + encodeURIComponent(gateway) + '#token=' + encodeURIComponent(descriptor.access_token))
    } catch (error) {
      state.connecting = ''
      renderAgents()
      setStatus(error.message || '连接失败')
    }
  }

  loginForm.addEventListener('submit', async function (event) {
    event.preventDefault()
    loginButton.disabled = true
    loginError.hidden = true
    setStatus('正在登录...')
    try {
      var form = new FormData(loginForm)
      var data = await request('/api/v1/auth/web-login', {
        method: 'POST',
        headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: form.get('username'), password: form.get('password') })
      })
      state.user = { username: data.username }
      loginForm.reset()
      showAgents()
      await loadAgents()
    } catch (error) {
      showLogin(error.message || '登录失败')
    } finally {
      loginButton.disabled = false
    }
  })

  logoutButton.addEventListener('click', async function () {
    logoutButton.disabled = true
    try { await request('/api/v1/auth/logout', { method: 'POST' }) } catch (_) {}
    logoutButton.disabled = false
    showLogin('')
  })

  refreshButton.addEventListener('click', loadAgents)

  async function start() {
    try {
      var data = await request('/api/v1/auth/me')
      state.user = { username: data.username }
      showAgents()
      await loadAgents()
    } catch (error) {
      showLogin(error.status === 401 ? '' : (error.message || '无法连接服务器'))
    }
  }

  start()
})()
