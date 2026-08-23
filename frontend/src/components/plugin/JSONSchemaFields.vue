<script setup lang="ts">
import { computed } from 'vue'

export interface JSONSchemaProperty {
  type?: 'string' | 'number' | 'integer' | 'boolean' | 'array'
  title?: string
  description?: string
  default?: unknown
  enum?: unknown[]
  minLength?: number
  minimum?: number
  maximum?: number
  items?: { type?: string }
}

export interface PluginJSONSchema {
  type?: string
  properties?: Record<string, JSONSchemaProperty>
  required?: string[]
}

const props = defineProps<{
  schema?: PluginJSONSchema
  modelValue: Record<string, unknown>
  secretFields?: string[]
  mode: 'settings' | 'secrets'
}>()

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, unknown>]
}>()

const secretSet = computed(() => new Set(props.secretFields || []))
const requiredSet = computed(() => new Set(props.schema?.required || []))
const fields = computed(() => Object.entries(props.schema?.properties || {})
  .filter(([key]) => (props.mode === 'secrets') === secretSet.value.has(key))
  .map(([key, definition]) => ({ key, definition })))

function update(key: string, value: unknown) {
  emit('update:modelValue', { ...props.modelValue, [key]: value })
}

function arrayText(key: string): string {
  const value = props.modelValue[key]
  return Array.isArray(value) ? value.join('\n') : ''
}

function updateArray(key: string, value: string) {
  update(key, value.split(/\r?\n/).map(item => item.trim()).filter(Boolean))
}

function updateValue(key: string, value: unknown) {
  update(key, value)
}

function updateString(key: string, value: unknown) {
  update(key, String(value ?? ''))
}

function updateArrayValue(key: string, value: unknown) {
  updateArray(key, String(value ?? ''))
}

function selectOptions(values: unknown[] = []) {
  return values.map(value => ({ label: String(value), value }))
}
</script>

<template>
  <div class="schema-fields">
    <div v-for="field in fields" :key="field.key" class="form-item schema-field">
      <label class="form-label" :class="{ required: requiredSet.has(field.key) }">
        {{ field.definition.title || field.key }}
      </label>

      <t-select
        v-if="field.definition.enum?.length"
        :value="modelValue[field.key]"
        :options="selectOptions(field.definition.enum)"
        clearable
        @change="updateValue(field.key, $event)"
      />
      <t-checkbox
        v-else-if="field.definition.type === 'boolean'"
        :checked="Boolean(modelValue[field.key])"
        @change="updateValue(field.key, $event)"
      >{{ field.definition.title || field.key }}</t-checkbox>
      <t-input-number
        v-else-if="field.definition.type === 'number' || field.definition.type === 'integer'"
        :value="modelValue[field.key] as number | undefined"
        :min="field.definition.minimum"
        :max="field.definition.maximum"
        @change="updateValue(field.key, $event)"
      />
      <t-textarea
        v-else-if="field.definition.type === 'array'"
        :value="arrayText(field.key)"
        :placeholder="'每行一个值'"
        :autosize="{ minRows: 2, maxRows: 7 }"
        spellcheck="false"
        @change="updateArrayValue(field.key, $event)"
      />
      <t-input
        v-else
        :value="String(modelValue[field.key] ?? '')"
        :type="mode === 'secrets' ? 'password' : 'text'"
        autocomplete="off"
        spellcheck="false"
        @change="updateString(field.key, $event)"
      >
        <template v-if="mode === 'secrets'" #prefix-icon><t-icon name="lock-on" /></template>
      </t-input>

      <p v-if="field.definition.description" class="form-desc">{{ field.definition.description }}</p>
    </div>
  </div>
</template>

<style scoped>
.schema-fields { display: grid; gap: 16px; }
.schema-field { margin: 0; }
</style>
