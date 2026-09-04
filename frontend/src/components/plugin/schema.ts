export interface PluginSchemaProperty {
  type?: 'string' | 'number' | 'integer' | 'boolean' | 'array'
  title?: string
  description?: string
  default?: unknown
  enum?: unknown[]
  items?: { type?: string }
  minLength?: number
  minimum?: number
  maximum?: number
}

export interface PluginSchema {
  properties?: Record<string, PluginSchemaProperty>
  required?: string[]
  additionalProperties?: boolean
}

export interface PluginConfigValidationOptions {
  ignoreRequired?: Set<string>
}

export function validatePluginConfig(
  schema: PluginSchema | undefined,
  values: Record<string, unknown>,
  options: PluginConfigValidationOptions = {},
): string | null {
  const properties = schema?.properties || {}
  for (const key of schema?.required || []) {
    if (options.ignoreRequired?.has(key)) continue
    const value = values[key]
    if (value === undefined || value === null || value === '' || (Array.isArray(value) && value.length === 0)) {
      return `${properties[key]?.title || key} 为必填项`
    }
  }
  for (const [key, value] of Object.entries(values)) {
    if (value === undefined || value === null || value === '') continue
    const property = properties[key]
    if (!property) {
      if (schema?.additionalProperties === false) return `${key} 不是允许的配置项`
      continue
    }
    const label = property.title || key
    if (!matchesType(property.type, value)) return `${label} 的类型不正确`
    if (property.enum?.length && !property.enum.some(item => String(item) === String(value))) {
      return `${label} 不在允许的选项中`
    }
    if (typeof value === 'string' && property.minLength !== undefined && [...value].length < property.minLength) {
      return `${label} 至少需要 ${property.minLength} 个字符`
    }
    const number = typeof value === 'number' ? value : Number(value)
    if ((property.type === 'number' || property.type === 'integer') && Number.isFinite(number)) {
      if (property.minimum !== undefined && number < property.minimum) return `${label} 不能小于 ${property.minimum}`
      if (property.maximum !== undefined && number > property.maximum) return `${label} 不能大于 ${property.maximum}`
    }
  }
  return null
}

export function normalizePluginConfig(
  schema: PluginSchema | undefined,
  values: Record<string, unknown>,
): Record<string, unknown> {
  const normalized: Record<string, unknown> = { ...values }
  for (const [key, value] of Object.entries(values)) {
    const type = schema?.properties?.[key]?.type
    if (typeof value !== 'string' || !type || type === 'string') continue
    try {
      const decoded = JSON.parse(value)
      if (matchesType(type, decoded)) normalized[key] = decoded
    } catch {
      // Keep legacy string values; validation will show a field-level error.
    }
  }
  return normalized
}

function matchesType(type: PluginSchemaProperty['type'], value: unknown): boolean {
  if (!type) return true
  if (type === 'string') return typeof value === 'string'
  if (type === 'boolean') return typeof value === 'boolean'
  if (type === 'array') return Array.isArray(value) && value.every(item => typeof item === 'string')
  const number = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(number)) return false
  return type === 'number' || Number.isInteger(number)
}
