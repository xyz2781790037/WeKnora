import assert from 'node:assert/strict'
import test from 'node:test'

import { normalizePluginConfig, validatePluginConfig, type PluginSchema } from './schema'

const schema: PluginSchema = {
  additionalProperties: false,
  properties: {
    endpoint: { type: 'string', title: '服务地址', minLength: 3 },
    retries: { type: 'integer', title: '重试次数', minimum: 0, maximum: 5 },
    enabled: { type: 'boolean', title: '启用' },
    paths: { type: 'array', title: '路径', items: { type: 'string' } },
  },
  required: ['endpoint'],
}

test('normalizes non-string form values according to config.schema', () => {
  assert.deepEqual(
    normalizePluginConfig(schema, { endpoint: 'api.example.com', retries: '3', enabled: 'true', paths: '["docs/**"]' }),
    { endpoint: 'api.example.com', retries: 3, enabled: true, paths: ['docs/**'] },
  )
})

test('validates required fields and primitive constraints', () => {
  assert.equal(validatePluginConfig(schema, {}), '服务地址 为必填项')
  assert.equal(validatePluginConfig(schema, { endpoint: 'ab' }), '服务地址 至少需要 3 个字符')
  assert.equal(validatePluginConfig(schema, { endpoint: 'api', retries: 6 }), '重试次数 不能大于 5')
  assert.equal(validatePluginConfig(schema, { endpoint: 'api', retries: 1.5 }), '重试次数 的类型不正确')
  assert.equal(validatePluginConfig(schema, { endpoint: 'api', paths: ['docs', 1] }), '路径 的类型不正确')
})

test('rejects undeclared fields when additionalProperties is false', () => {
  assert.equal(validatePluginConfig(schema, { endpoint: 'api', token: 'secret' }), 'token 不是允许的配置项')
  assert.equal(validatePluginConfig({ properties: {} }, { legacy: 'value' }), null)
})

test('can ignore required secret fields when editing redacted configuration', () => {
  assert.equal(validatePluginConfig(schema, {}, { ignoreRequired: new Set(['endpoint']) }), null)
})
