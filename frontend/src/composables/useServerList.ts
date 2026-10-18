import { ref, watch, type Ref } from 'vue'

import { getErrorMessage, type ListPageMeta, type ListQuery } from '@/lib/api'

/**
 * A server-paged list (plan 071): every table on the web asks the server for
 * one page — filtered, ordered there — and this composable owns the state that
 * decides which page that is.
 *
 * What it answers: `load()` once at the start (the caller mounts the list) and
 * `reload()` after a write. From then on it loads itself — a new search term
 * after a 300ms pause, a changed filter, order or page size from page 1, and a
 * page click straight to that page. The server's own `page` is written back
 * without asking for it again (a delete that empties the last page lands on the
 * one before it, and that row set is already the answer), and only the newest
 * request's answer is applied, so a slow earlier page can never overwrite a
 * newer one.
 */
export interface ServerListOptions {
  pageSize?: number
  sort?: string
  order?: 'asc' | 'desc'
  filters?: Record<string, unknown>
}

/** How long a search keystroke waits before it asks the server. */
const SEARCH_DEBOUNCE_MS = 300

export interface ServerList<Row> {
  rows: Ref<Row[]>
  total: Ref<number>
  page: Ref<number>
  pageSize: Ref<number>
  q: Ref<string>
  sort: Ref<string>
  order: Ref<'asc' | 'desc'>
  filters: Ref<Record<string, unknown>>
  loading: Ref<boolean>
  error: Ref<string>
  load: () => Promise<void>
  reload: () => Promise<void>
}

export function useServerList<Row>(
  fetch: (query: ListQuery) => Promise<{ rows: Row[] } & ListPageMeta>,
  options: ServerListOptions = {},
): ServerList<Row> {
  const rows = ref<Row[]>([]) as Ref<Row[]>
  const total = ref(0)
  const page = ref(1)
  const pageSize = ref(options.pageSize ?? 10)
  const q = ref('')
  const sort = ref(options.sort ?? '')
  const order = ref<'asc' | 'desc'>(options.order ?? 'desc')
  const filters = ref<Record<string, unknown>>({ ...(options.filters ?? {}) })
  const loading = ref(false)
  const error = ref('')

  // The newest request's sequence number: an answer whose number is stale is
  // dropped instead of applied.
  let latest = 0
  // Set while the served page is written back, so that write does not read as
  // the person turning a page.
  let applyingPage = false
  let searchTimer: ReturnType<typeof setTimeout> | null = null

  async function load(): Promise<void> {
    const sequence = ++latest
    loading.value = true
    error.value = ''
    try {
      const answer = await fetch({
        q: q.value,
        sort: sort.value || undefined,
        order: order.value,
        page: page.value,
        pageSize: pageSize.value,
        ...filters.value,
      })
      if (sequence !== latest) return
      rows.value = answer.rows
      total.value = answer.total
      if (answer.page !== page.value) {
        applyingPage = true
        page.value = answer.page
      }
    } catch (cause) {
      if (sequence !== latest) return
      error.value = getErrorMessage(cause)
    } finally {
      if (sequence === latest) loading.value = false
    }
  }

  /** Back to page 1 for a change that redefines the result set. */
  function firstPage(): void {
    if (page.value === 1) return
    applyingPage = true
    page.value = 1
  }

  watch(page, () => {
    if (applyingPage) {
      applyingPage = false
      return
    }
    void load()
  })

  watch([sort, order], () => {
    firstPage()
    void load()
  })

  watch(pageSize, () => {
    firstPage()
    void load()
  })

  watch(filters, () => {
    firstPage()
    void load()
  }, { deep: true })

  // Typing in the search box asks the server once the person pauses.
  watch(q, () => {
    if (searchTimer) clearTimeout(searchTimer)
    searchTimer = setTimeout(() => {
      searchTimer = null
      firstPage()
      void load()
    }, SEARCH_DEBOUNCE_MS)
  })

  return {
    rows,
    total,
    page,
    pageSize,
    q,
    sort,
    order,
    filters,
    loading,
    error,
    load,
    reload: load,
  }
}
