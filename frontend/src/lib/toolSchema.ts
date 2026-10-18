/**
 * The parameter table a tool's expanded row draws (plan 073), read from the
 * JSON Schema the runtime publishes for the tool's input.
 *
 * The rows come in the order the schema declares its properties — the order
 * the tool's own author wrote them in — and a property the schema marks as
 * required says so. Arrays name their element type (`string[]`), a property
 * with no type is `object`, and a schema that carries no properties has no
 * parameters to show.
 */
export interface ToolParam {
  name: string
  type: string
  required: boolean
  description: string
}

interface SchemaProperty {
  type?: unknown
  description?: unknown
  items?: { type?: unknown } | null
}

function paramType(property: SchemaProperty): string {
  const type = typeof property.type === 'string' ? property.type : ''
  if (type === 'array') {
    const item = property.items && typeof property.items === 'object' ? property.items.type : undefined
    return typeof item === 'string' && item ? `${item}[]` : 'array'
  }
  return type || 'object'
}

export function toolParams(schema: unknown): ToolParam[] {
  if (!schema || typeof schema !== 'object') return []
  const node = schema as { properties?: unknown; required?: unknown }
  if (!node.properties || typeof node.properties !== 'object') return []
  const required = Array.isArray(node.required) ? node.required.map((name) => String(name)) : []
  return Object.entries(node.properties as Record<string, unknown>).map(([name, raw]) => {
    const property = (raw && typeof raw === 'object' ? raw : {}) as SchemaProperty
    return {
      name,
      type: paramType(property),
      required: required.includes(name),
      description: typeof property.description === 'string' ? property.description : '',
    }
  })
}
