import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { setUser, getUser, getToken, setToken, getErrorMessage, forebrainApi } from '@/lib/api'

const ANON_CLIENT_KEY = 'forebrain_anonymous_client_id'
const LAST_SESSION_KEY = 'forebrain_last_session_id'

const clientId = ref<string | null>(null)
const lastSessionId = ref<string | null>(null)

function ensureAnonymousClientId(): string {
  try {
    if (typeof localStorage === 'undefined') {
      return `anon-${crypto.randomUUID()}`
    }
    let id = localStorage.getItem(ANON_CLIENT_KEY)
    if (!id) {
      id = crypto.randomUUID()
      localStorage.setItem(ANON_CLIENT_KEY, id)
    }
    return id
  } catch {
    return `anon-${crypto.randomUUID()}`
  }
}

function initFromStorage() {
  const user = getUser()
  if (user?.clientId) {
    clientId.value = user.clientId
    lastSessionId.value = user.lastSessionId ?? null
    if (!lastSessionId.value && typeof localStorage !== 'undefined') {
      try {
        lastSessionId.value = localStorage.getItem(LAST_SESSION_KEY)
      } catch {
        lastSessionId.value = null
      }
    }
    return
  }
  clientId.value = ensureAnonymousClientId()
  try {
    if (typeof localStorage !== 'undefined') {
      lastSessionId.value = localStorage.getItem(LAST_SESSION_KEY)
    }
  } catch {
    lastSessionId.value = null
  }
}
initFromStorage()

/**
 * The conversation the browser was last on, for a view that needs to ask about
 * a session it does not own — the MCP page reports the servers of the Runner
 * that session runs on, which is not necessarily the primary agent's.
 */
export function lastSessionIdValue(): string {
  return String(lastSessionId.value ?? '').trim()
}

export function persistLastSessionId(sid: string | null) {
  lastSessionId.value = sid
  try {
    if (typeof localStorage === 'undefined') return
    if (sid && sid.trim()) {
      localStorage.setItem(LAST_SESSION_KEY, sid.trim())
    } else {
      localStorage.removeItem(LAST_SESSION_KEY)
    }
  } catch {
    //
  }
}

export function useAuth() {
  const router = useRouter()

  const isAuthenticated = computed(() => !!clientId.value)

  async function checkAuth(): Promise<boolean> {
    initFromStorage()
    return true
  }

  async function login(username: string, password: string): Promise<{ success: boolean; message?: string }> {
    try {
      const tokenStr = await forebrainApi.authLogin(username, password)
      if (typeof tokenStr === 'string') {
        setToken(tokenStr)
        const data = await forebrainApi.authCurrent()
        if (data?.clientId) {
          setUser(data)
          clientId.value = data.clientId
          lastSessionId.value = data.lastSessionId ?? null
        } else {
          clientId.value = username
        }
      }
      return { success: true }
    } catch (e) {
      return { success: false, message: getErrorMessage(e) }
    }
  }

  async function register(username: string, password: string): Promise<{ success: boolean; message?: string }> {
    try {
      const tokenStr = await forebrainApi.authRegister(username, password)
      if (typeof tokenStr === 'string') {
        setToken(tokenStr)
        const data = await forebrainApi.authCurrent()
        if (data?.clientId) {
          setUser(data)
          clientId.value = data.clientId
          lastSessionId.value = data.lastSessionId ?? null
        } else {
          clientId.value = username
        }
      }
      return { success: true }
    } catch (e) {
      return { success: false, message: getErrorMessage(e) }
    }
  }

  async function logout(): Promise<void> {
    try {
      if (getToken()) {
        await forebrainApi.authLogout()
      }
    } catch {
      //
    } finally {
      setUser(null)
      setToken(null)
      try {
        if (typeof localStorage !== 'undefined') {
          localStorage.removeItem(ANON_CLIENT_KEY)
        }
      } catch {
        //
      }
      clientId.value = ensureAnonymousClientId()
      persistLastSessionId(null)
      router.push('/')
    }
  }

  return {
    clientId,
    lastSessionId,
    isAuthenticated,
    checkAuth,
    login,
    register,
    logout,
  }
}
