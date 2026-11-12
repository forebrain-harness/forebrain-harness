import type { SessionModelOption, SessionModelResponse } from './api'

/**
 * The option a conversation's selection names: the configured row by id when
 * the selection carries one, else the provider+model pair (a row written before
 * ids existed). The effort chip reads its levels and its configured effort.
 */
export function sessionModelOptionInForce(data: SessionModelResponse | null): SessionModelOption | undefined {
  if (!data) return undefined
  return data.modelId
    ? data.options.find((candidate) => candidate.id === data.modelId)
    : data.options.find((candidate) => candidate.provider === data.provider && candidate.model === data.model)
}
