import { ref } from 'vue'

import { forebrainApi, type LlmModelChoice } from '@/lib/api'

/**
 * The install's configured models, as every picker reads them (`GET
 * /api/llm-models/choices`, plan 265). One request serves the whole page: the
 * settings page alone has two model rows, and a chip beside them reads the same
 * list, so the answer is asked for once and kept for the life of the page.
 *
 * A failure is not a page error: a picker without options still shows the value
 * it holds, and the list is a convenience rather than the record.
 */
const choices = ref<LlmModelChoice[]>([])
let request: Promise<void> | null = null

export function useModelChoices() {
  function ensureChoices(): void {
    request ??= forebrainApi
      .llmModelChoices()
      .then((rows) => {
        choices.value = Array.isArray(rows) ? rows : []
      })
      .catch(() => {
        // Retried by the next surface that needs it: a failure is not cached.
        request = null
      })
  }
  return { choices, ensureChoices }
}
