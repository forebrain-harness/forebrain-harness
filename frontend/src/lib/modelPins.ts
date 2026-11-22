/**
 * One pin as a person reads it (plan 347). The API sends the structure, not a
 * label, because the page's language lives in localStorage and never reaches
 * the request's Accept-Language: each surface writes its own words.
 */
import type { I18nKey } from '@/locales'
import type { LlmModelPinRef } from './api'

/** The app's own translator (src/locales/index.ts:5197), so `t` from useI18n passes. */
type Translate = (key: I18nKey, params?: Record<string, string | number>) => string

export function modelPinLabel(t: Translate, pin: LlmModelPinRef): string {
  switch (pin.kind) {
    case 'plan_review':
      return t('models.pinPlanReview')
    case 'goal_evaluator':
      return t('models.pinGoalEvaluator')
    default:
      return t('models.pinSubagent', { name: pin.name ?? '' }).trim()
  }
}

export function modelPinLabels(t: Translate, pins?: LlmModelPinRef[]): string[] {
  return (pins ?? []).map((pin) => modelPinLabel(t, pin))
}
