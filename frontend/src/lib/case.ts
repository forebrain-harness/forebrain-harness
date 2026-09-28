import camelcaseKeys from 'camelcase-keys'
import snakecaseKeys from 'snakecase-keys'

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const proto = Object.getPrototypeOf(value)
  return proto === Object.prototype || proto === null
}

export function toCamelCase<T>(value: T): T {
  if (Array.isArray(value) || isPlainObject(value)) {
    return camelcaseKeys(value as Record<string, unknown> | Record<string, unknown>[], {
      deep: true,
    }) as T
  }
  return value
}

export function toSnakeCase<T>(value: T): T {
  if (Array.isArray(value) || isPlainObject(value)) {
    return snakecaseKeys(value as Record<string, unknown> | Record<string, unknown>[], {
      deep: true,
    }) as T
  }
  return value
}

export function parseJsonCamelCase<T>(raw: string): T {
  return toCamelCase(JSON.parse(raw) as unknown) as T
}
