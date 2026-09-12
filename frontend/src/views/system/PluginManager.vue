<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  checkPluginHealth,
  disablePlugin,
  enablePlugin,
  getPluginOperation,
  inspectPluginManifest,
  listPluginHistory,
  listPluginMarketplace,
  listPluginEvents,
  listPluginTypeSpecifications,
  listPlugins,
  startPluginInstall,
  startPluginLatestUpgrade,
  startPluginUpgrade,
  checkPluginUpdate,
  uninstallPlugin,
  type InstalledPlugin,
  type PluginHistoryEntry,
  type PluginInstallPayload,
  type PluginManifestInspection,
  type PluginMarketplaceItem,
  type PluginMarketplaceResult,
  type PluginOperation,
  type PluginRuntimeEvent,
  type PluginTypeSpecification,
} from '@/api/plugin'

const pluginTypeDefinitions = {
  data_source: { label: '数据源', icon: 'data-base', theme: 'primary', className: 'data-source' },
  document_parser: { label: '文档解析', icon: 'file', theme: 'success', className: 'document-parser' },
  web_search: { label: '联网搜索', icon: 'search', theme: 'warning', className: 'web-search' },
  model_provider: { label: '模型供应商', icon: 'chat', theme: 'default', className: 'model-provider' },
  retrieval_engine: { label: '检索引擎', icon: 'server', theme: 'danger', className: 'retrieval-engine' },
} as const
type PluginType = keyof typeof pluginTypeDefinitions
type PluginTypeFilter = 'all' | PluginType
type PluginOriginFilter = 'all' | 'builtin' | 'external'

const pluginTypeOptions: Array<{ value: PluginTypeFilter; label: string }> = [
  { value: 'all', label: '全部' },
  { value: 'data_source', label: '数据源' },
  { value: 'document_parser', label: '文档解析' },
  { value: 'web_search', label: '联网搜索' },
  { value: 'model_provider', label: '模型供应商' },
  { value: 'retrieval_engine', label: '检索引擎' },
]

interface PluginDetailView {
  id: string
  name: string
  description: string
  version: string
  protocolVersion: string
  weknoraVersion: string
  image: string
  types: string[]
  capabilities: string[]
  configSchema: Record<string, unknown>
  secretFields: string[]
  permissions: {
    network: boolean
    allowedHosts: string[]
    dataAccess: string[]
  }
  supplyChain: {
    publisher: string
    sourceRepository: string
    imageDigest: string
    signature: string
  }
  resources: {
    memoryBytes: number
    nanoCPUs: number
    pidsLimit: number
    maxConcurrency: number
    callsPerMinute: number
  }
  origin: 'builtin' | 'external' | 'marketplace'
  repositoryURL?: string
  updatedAt?: string
}

const activeSection = ref<'marketplace' | 'installed'>('marketplace')
const pluginTypeFilter = ref<PluginTypeFilter>('all')
const pluginOriginFilter = ref<PluginOriginFilter>('all')
const loading = ref(false)
const operatingID = ref('')
const plugins = ref<InstalledPlugin[]>([])
const marketplaceLoading = ref(false)
const marketplaceError = ref('')
const marketplaceSearch = ref('')
const marketplacePage = ref(1)
const marketplaceCompatibleOnly = ref(false)
const marketplaceCertification = ref('')
const marketplaceSort = ref<'updated' | 'stars' | 'name'>('updated')
const marketplace = ref<PluginMarketplaceResult | null>(null)
const editorVisible = ref(false)
const editorMode = ref<'install' | 'upgrade'>('install')
const editingPlugin = ref<InstalledPlugin | null>(null)
const manifestYAML = ref('')
const manifestURL = ref('')
const manifestSource = ref<'url' | 'yaml'>('url')
const callTimeoutSeconds = ref(600)
const manifestOperationStatus = ref<'idle' | 'running' | 'success' | 'error'>('idle')
const manifestOperationError = ref('')
const manifestOperation = ref<PluginOperation | null>(null)
const manifestInspection = ref<PluginManifestInspection | null>(null)
const manifestInspecting = ref(false)
const permissionsConfirmed = ref(false)
const upgradeLatest = ref(false)
const eventsVisible = ref(false)
const eventsLoading = ref(false)
const eventPlugin = ref<InstalledPlugin | null>(null)
const runtimeEvents = ref<PluginRuntimeEvent[]>([])
const eventKind = ref('')
const eventOutcome = ref('')
const eventFrom = ref('')
const eventTo = ref('')
const detailVisible = ref(false)
const detailLoading = ref(false)
const detailPlugin = ref<PluginDetailView | null>(null)
const detailHistory = ref<PluginHistoryEntry[]>([])
const typeSpecifications = ref<PluginTypeSpecification[]>([])

const installedPluginsByType = computed(() => filterPluginsByType(plugins.value))
const builtinCount = computed(() => installedPluginsByType.value.filter(item => item.origin === 'builtin').length)
const externalCount = computed(() => installedPluginsByType.value.filter(item => item.origin === 'external').length)
const filteredPlugins = computed(() => {
  if (pluginOriginFilter.value === 'all') return installedPluginsByType.value
  return installedPluginsByType.value.filter(item => item.origin === pluginOriginFilter.value)
})
const filteredMarketplaceItems = computed(() => marketplace.value?.items || [])
const installedByID = computed(() => new Map(plugins.value.map(plugin => [plugin.id, plugin])))
const marketplaceHasPrevious = computed(() => marketplacePage.value > 1)
const marketplaceHasNext = computed(() => {
  if (!marketplace.value) return false
  return marketplacePage.value * marketplace.value.page_size < marketplace.value.repository_count
})
const manifestOperating = computed(() => manifestOperationStatus.value === 'running')
const manifestCanExecute = computed(() => Boolean(manifestInspection.value && permissionsConfirmed.value))
const manifestSteps = computed(() => manifestOperation.value?.steps || [])
const detailConfigSchema = computed(() => JSON.stringify(detailPlugin.value?.configSchema || {}, null, 2))
const detailTypeSpecifications = computed(() => {
  const types = new Set(detailPlugin.value?.types || [])
  return typeSpecifications.value.filter(item => types.has(item.type))
})

function filterPluginsByType<T extends { types: string[] }>(items: T[]): T[] {
  if (pluginTypeFilter.value === 'all') return items
  return items.filter(item => item.types.includes(pluginTypeFilter.value))
}

function pluginTypeInfo(type?: string) {
  return pluginTypeDefinitions[type as PluginType] || {
    label: type || '其他插件',
    icon: 'extension',
    theme: 'default' as const,
    className: 'other',
  }
}

async function refresh() {
  loading.value = true
  try {
    plugins.value = await listPlugins()
  } catch (error: any) {
    MessagePlugin.error(error?.message || '加载插件失败')
  } finally {
    loading.value = false
  }
}

async function refreshMarketplace(force = false) {
  marketplaceLoading.value = true
  marketplaceError.value = ''
  try {
    marketplace.value = await listPluginMarketplace({
      search: marketplaceSearch.value.trim() || undefined,
      type: pluginTypeFilter.value === 'all' ? undefined : pluginTypeFilter.value,
      compatible_only: marketplaceCompatibleOnly.value || undefined,
      certification: marketplaceCertification.value || undefined,
      sort: marketplaceSort.value,
      page: marketplacePage.value,
      page_size: 12,
      refresh: force || undefined,
    })
  } catch (error: any) {
    marketplace.value = null
    marketplaceError.value = error?.message || '加载插件市场失败'
  } finally {
    marketplaceLoading.value = false
  }
}

function searchMarketplace() {
  marketplacePage.value = 1
  void refreshMarketplace(true)
}

function changeMarketplacePage(offset: number) {
  marketplacePage.value += offset
  void refreshMarketplace()
}

function openEditor(mode: 'install' | 'upgrade', plugin: InstalledPlugin | null, url = '', latest = false) {
  editorMode.value = mode
  editingPlugin.value = plugin
  manifestYAML.value = ''
  manifestURL.value = url
  manifestSource.value = 'url'
  callTimeoutSeconds.value = plugin?.call_timeout_seconds || 600
  manifestOperationStatus.value = 'idle'
  manifestOperationError.value = ''
  manifestOperation.value = null
  manifestInspection.value = null
  permissionsConfirmed.value = false
  upgradeLatest.value = latest
  editorVisible.value = true
}

function openInstall(url = '') {
  openEditor('install', null, url)
}

function openUpgrade(plugin: InstalledPlugin) {
  openEditor('upgrade', plugin)
}

function openLatestUpgrade(plugin: InstalledPlugin) {
  openEditor('upgrade', plugin, plugin.source_manifest_url || '', true)
}

function marketplaceAction(plugin: PluginMarketplaceItem) {
  openInstall(plugin.manifest_url)
}

function marketplaceActionLabel(plugin: PluginMarketplaceItem) {
  return installedByID.value.has(plugin.id) ? '已安装' : '安装'
}

function marketplaceActionDisabled(plugin: PluginMarketplaceItem) {
  return !plugin.compatible || installedByID.value.has(plugin.id)
}

async function submitManifest() {
  if (manifestOperationStatus.value === 'success') {
    editorVisible.value = false
    return
  }
  if (manifestSource.value === 'url' && !manifestURL.value.trim()) {
    MessagePlugin.warning('请输入 plugin.yaml 的 HTTPS 地址')
    return
  }
  if (manifestSource.value === 'yaml' && !manifestYAML.value.trim()) {
    MessagePlugin.warning('请粘贴 plugin.yaml 内容')
    return
  }
  const payload = manifestPayload()
  if (!manifestInspection.value) {
    await inspectCurrentManifest(payload)
    return
  }
  if (!manifestCanExecute.value) {
    MessagePlugin.warning('请确认插件申请的权限')
    return
  }
  payload.permissions_confirmed = true
  payload.permission_digest = manifestInspection.value.permission_digest
  manifestOperationStatus.value = 'running'
  manifestOperationError.value = ''
  operatingID.value = editingPlugin.value?.id || '__install__'
  try {
    const existingOperation = manifestOperation.value
    const started = existingOperation?.status === 'running'
      ? existingOperation
      : editorMode.value === 'upgrade' && editingPlugin.value
        ? upgradeLatest.value
          ? await startPluginLatestUpgrade(editingPlugin.value.id, payload)
          : await startPluginUpgrade(editingPlugin.value.id, payload)
        : await startPluginInstall(payload)
    manifestOperation.value = started
    const completed = await waitForPluginOperation(started)
    if (completed.status === 'error') {
      throw new Error(completed.error || '插件操作失败')
    }
    manifestOperationStatus.value = 'success'
    MessagePlugin.success(editorMode.value === 'upgrade' ? '插件升级成功' : '插件安装并校验成功，启用后才会提供能力')
    await refresh()
  } catch (error: any) {
    manifestOperationStatus.value = 'error'
    manifestOperationError.value = error?.message || '插件操作失败'
    MessagePlugin.error(manifestOperationError.value)
  } finally {
    operatingID.value = ''
  }
}

function manifestPayload(): PluginInstallPayload {
  return {
    manifest_yaml: manifestSource.value === 'yaml' ? manifestYAML.value : undefined,
    manifest_url: manifestSource.value === 'url' ? manifestURL.value.trim() : undefined,
    call_timeout_seconds: callTimeoutSeconds.value,
    plugin_id: editorMode.value === 'upgrade' ? editingPlugin.value?.id : undefined,
  }
}

function resetManifestOperation() {
  manifestOperationStatus.value = 'idle'
  manifestOperationError.value = ''
  manifestOperation.value = null
  manifestInspection.value = null
  permissionsConfirmed.value = false
}

async function inspectCurrentManifest(payload = manifestPayload()) {
  manifestInspecting.value = true
  manifestOperationError.value = ''
  try {
    manifestInspection.value = await inspectPluginManifest(payload)
    permissionsConfirmed.value = false
  } catch (error: any) {
    manifestOperationError.value = error?.message || '插件清单预检失败'
    MessagePlugin.error(manifestOperationError.value)
  } finally {
    manifestInspecting.value = false
  }
}

async function waitForPluginOperation(initial: PluginOperation): Promise<PluginOperation> {
  let operation = initial
  while (operation.status === 'running') {
    await new Promise(resolve => window.setTimeout(resolve, 700))
    operation = await getPluginOperation(operation.id)
    manifestOperation.value = operation
  }
  return operation
}

function recordValue(value: unknown): Record<string, any> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, any> : {}
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter(item => typeof item === 'string') : []
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function memoryLimitText(value: number): string {
  return value > 0 ? `${Math.round(value / 1024 / 1024)} MiB` : '继承运行时上限'
}

function cpuLimitText(value: number): string {
  return value > 0 ? `${value / 1_000_000_000} 核` : '继承运行时上限'
}

function quotaLimitText(value: number, suffix = ''): string {
  return value > 0 ? `${value}${suffix}` : '继承运行时上限'
}

function installedPluginDetail(plugin: InstalledPlugin): PluginDetailView {
  const manifest = recordValue(plugin.manifest)
  const spec = recordValue(manifest.spec)
  const config = recordValue(spec.config)
  const permissions = recordValue(spec.permissions)
  const supplyChain = recordValue(spec.supply_chain)
  const resources = recordValue(spec.resources)
  return {
    id: plugin.id,
    name: plugin.name,
    description: plugin.description,
    version: plugin.version,
    protocolVersion: plugin.protocol_version,
    weknoraVersion: plugin.weknora_version_constraint,
    image: plugin.image,
    types: plugin.types,
    capabilities: plugin.capabilities,
    configSchema: recordValue(config.schema || manifest.config_schema),
    secretFields: stringArray(config.secret_fields),
    permissions: {
      network: permissions.network === true,
      allowedHosts: stringArray(permissions.allowed_hosts),
      dataAccess: stringArray(permissions.data_access),
    },
    supplyChain: {
      publisher: typeof supplyChain.publisher === 'string' ? supplyChain.publisher : '',
      sourceRepository: typeof supplyChain.source_repository === 'string' ? supplyChain.source_repository : '',
      imageDigest: typeof supplyChain.image_digest === 'string' ? supplyChain.image_digest : '',
      signature: typeof supplyChain.signature === 'string' ? supplyChain.signature : '',
    },
    resources: {
      memoryBytes: numberValue(resources.memory_bytes),
      nanoCPUs: numberValue(resources.nano_cpus),
      pidsLimit: numberValue(resources.pids_limit),
      maxConcurrency: numberValue(resources.max_concurrency),
      callsPerMinute: numberValue(resources.calls_per_minute),
    },
    origin: plugin.origin,
    updatedAt: plugin.updated_at,
  }
}

function marketplacePluginDetail(plugin: PluginMarketplaceItem): PluginDetailView {
  return {
    id: plugin.id,
    name: plugin.name,
    description: plugin.description,
    version: plugin.version,
    protocolVersion: plugin.protocol_version,
    weknoraVersion: plugin.weknora_version_constraint,
    image: plugin.image,
    types: plugin.types,
    capabilities: plugin.capabilities,
    configSchema: plugin.config_schema || {},
    secretFields: plugin.secret_fields || [],
    permissions: {
      network: plugin.permissions.network,
      allowedHosts: plugin.permissions.allowed_hosts,
      dataAccess: plugin.permissions.data_access,
    },
    supplyChain: {
      publisher: plugin.supply_chain?.publisher || '',
      sourceRepository: plugin.supply_chain?.source_repository || '',
      imageDigest: plugin.supply_chain?.image_digest || '',
      signature: plugin.supply_chain?.signature || '',
    },
    resources: {
      memoryBytes: plugin.resources?.memory_bytes || 0,
      nanoCPUs: plugin.resources?.nano_cpus || 0,
      pidsLimit: plugin.resources?.pids_limit || 0,
      maxConcurrency: plugin.resources?.max_concurrency || 0,
      callsPerMinute: plugin.resources?.calls_per_minute || 0,
    },
    origin: 'marketplace',
    repositoryURL: plugin.repository_url,
    updatedAt: plugin.updated_at,
  }
}

async function openInstalledDetail(plugin: InstalledPlugin) {
  await openPluginDetail(installedPluginDetail(plugin), plugin.origin === 'external')
}

async function openMarketplaceDetail(plugin: PluginMarketplaceItem) {
  const installed = installedByID.value.get(plugin.id)
  if (installed) {
    await openPluginDetail({
      ...installedPluginDetail(installed),
      repositoryURL: plugin.repository_url,
    }, installed.origin === 'external')
    return
  }
  await openPluginDetail(marketplacePluginDetail(plugin), false)
}

async function openPluginDetail(detail: PluginDetailView, loadHistory: boolean) {
  detailPlugin.value = detail
  detailHistory.value = []
  detailLoading.value = false
  detailVisible.value = true
  if (!loadHistory) return
  detailLoading.value = true
  try {
    detailHistory.value = await listPluginHistory(detail.id)
  } catch (error: any) {
    MessagePlugin.error(error?.message || '加载插件更新记录失败')
  } finally {
    detailLoading.value = false
  }
}

function detailTime(value?: string) {
  return value ? new Date(value).toLocaleString() : '-'
}

function historyDescription(entry: PluginHistoryEntry) {
  if (entry.action === 'plugin.installed') return `安装 v${entry.new_version || '-'}`
  return `从 v${entry.old_version || '-'} 升级到 v${entry.new_version || '-'}`
}

async function openEvents(plugin: InstalledPlugin) {
  eventPlugin.value = plugin
  eventsVisible.value = true
  eventsLoading.value = true
  eventKind.value = ''
  eventOutcome.value = ''
  eventFrom.value = ''
  eventTo.value = ''
  await loadPluginEvents()
}

async function loadPluginEvents() {
  if (!eventPlugin.value) return
  eventsLoading.value = true
  try {
    runtimeEvents.value = await listPluginEvents(eventPlugin.value.id, {
      kind: eventKind.value || undefined,
      outcome: (eventOutcome.value || undefined) as 'success' | 'failed' | 'denied' | undefined,
      from: localDateTimeISO(eventFrom.value),
      to: localDateTimeISO(eventTo.value),
    })
  } catch (error: any) {
    runtimeEvents.value = []
    MessagePlugin.error(error?.message || '加载运行日志失败')
  } finally {
    eventsLoading.value = false
  }
}

function localDateTimeISO(value: string): string | undefined {
  if (!value) return undefined
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString()
}

async function refreshPluginUpdate(plugin: InstalledPlugin) {
  operatingID.value = plugin.id
  try {
    const updated = await checkPluginUpdate(plugin.id)
    const index = plugins.value.findIndex(item => item.id === plugin.id)
    if (index >= 0) plugins.value[index] = updated
    MessagePlugin.success(updated.update_available ? `发现新版本 v${updated.latest_version}` : updated.update_message || '当前已是最新版本')
  } catch (error: any) {
    MessagePlugin.error(error?.message || '检查更新失败')
    await refresh()
  } finally {
    operatingID.value = ''
  }
}

function eventTime(value?: string) {
  return value ? new Date(value).toLocaleString() : '-'
}

function eventTheme(event: PluginRuntimeEvent) {
  if (event.kind.endsWith('failed') || event.kind === 'network_denied') return 'danger'
  if (event.kind.endsWith('succeeded') || event.kind === 'started' || event.kind === 'installed') return 'success'
  return 'default'
}

async function run(plugin: InstalledPlugin, action: 'enable' | 'disable' | 'health' | 'uninstall') {
  operatingID.value = plugin.id
  try {
    if (action === 'enable') await enablePlugin(plugin.id)
    if (action === 'disable') await disablePlugin(plugin.id)
    if (action === 'health') await checkPluginHealth(plugin.id)
    if (action === 'uninstall') await uninstallPlugin(plugin.id)
    const messages = {
      enable: '插件已启用',
      disable: '插件已停用',
      health: '健康检查已完成',
      uninstall: '插件已卸载',
    }
    MessagePlugin.success(messages[action])
    await refresh()
  } catch (error: any) {
    MessagePlugin.error(error?.message || '插件操作失败')
  } finally {
    operatingID.value = ''
  }
}

function stateTheme(plugin: InstalledPlugin) {
  if (plugin.runtime_state === 'running') return 'success'
  if (plugin.runtime_state === 'unhealthy' || plugin.runtime_state === 'error') return 'danger'
  return 'default'
}

onMounted(() => {
  void Promise.all([
    refresh(),
    refreshMarketplace(),
    listPluginTypeSpecifications().then(items => { typeSpecifications.value = items }),
  ])
})

watch(activeSection, value => {
  if (value === 'marketplace') {
    marketplacePage.value = 1
    void refreshMarketplace()
  }
})

watch(pluginTypeFilter, () => {
  if (activeSection.value === 'marketplace') {
    marketplacePage.value = 1
    void refreshMarketplace()
  }
})

watch([manifestSource, manifestURL, manifestYAML], () => {
  if (manifestOperationStatus.value === 'idle') {
    manifestInspection.value = null
    permissionsConfirmed.value = false
  }
})
</script>

<template>
  <div class="plugin-manager">
    <div class="plugin-header">
      <div>
        <h2>插件中心</h2>
        <p>发现、安装和管理系统级插件。外部插件在隔离容器中运行，不能直接访问数据库或 Redis。</p>
      </div>
      <div class="plugin-header-actions">
        <t-button
          v-if="activeSection === 'marketplace'"
          variant="outline"
          :loading="marketplaceLoading"
          @click="refreshMarketplace(true)"
        >刷新市场</t-button>
        <t-button theme="primary" @click="openInstall()">
          <template #icon><t-icon name="add" /></template>
          手动安装
        </t-button>
      </div>
    </div>

    <t-radio-group v-model="activeSection" class="plugin-sections" variant="default-filled">
      <t-radio-button value="marketplace">插件市场</t-radio-button>
      <t-radio-button value="installed">已安装插件</t-radio-button>
    </t-radio-group>

    <div class="plugin-type-filter">
      <span>插件类型</span>
      <t-radio-group v-model="pluginTypeFilter" variant="default-filled">
        <t-radio-button
          v-for="option in pluginTypeOptions"
          :key="option.value"
          :value="option.value"
        >{{ option.label }}</t-radio-button>
      </t-radio-group>
    </div>

    <template v-if="activeSection === 'marketplace'">
      <div class="marketplace-toolbar">
        <t-input
          v-model="marketplaceSearch"
          clearable
          placeholder="搜索插件名称或仓库"
          @enter="searchMarketplace"
        />
        <t-button :loading="marketplaceLoading" @click="searchMarketplace">搜索</t-button>
        <t-select
          v-model="marketplaceCertification"
          clearable
          placeholder="全部来源"
          :options="[
            { label: '官方', value: 'official' },
            { label: '已认证', value: 'verified' },
            { label: '社区', value: 'community' },
          ]"
          @change="searchMarketplace"
        />
        <t-select
          v-model="marketplaceSort"
          :options="[
            { label: '最近更新', value: 'updated' },
            { label: '最多收藏', value: 'stars' },
            { label: '名称排序', value: 'name' },
          ]"
          @change="searchMarketplace"
        />
        <t-checkbox v-model="marketplaceCompatibleOnly" @change="searchMarketplace">仅显示兼容插件</t-checkbox>
      </div>

      <t-alert
        v-if="marketplace?.stale"
        theme="warning"
        title="正在显示缓存数据"
        message="GitHub 暂时不可用，结果来自最近一次成功同步。"
      />

      <t-alert
        v-if="marketplaceError"
        theme="error"
        title="插件市场加载失败"
        :message="marketplaceError"
      />

      <div v-if="marketplace" class="marketplace-summary">
        <span>Topic：{{ marketplace.topic }}</span>
        <span>匹配插件 {{ marketplace.repository_count }}</span>
        <span>GitHub 候选仓库 {{ marketplace.github_repository_count }}</span>
        <span v-if="marketplace.skipped_count">本页忽略无效清单 {{ marketplace.skipped_count }}</span>
      </div>

      <t-loading :loading="marketplaceLoading" show-overlay>
        <div v-if="filteredMarketplaceItems.length" class="plugin-list marketplace-list">
          <article v-for="plugin in filteredMarketplaceItems" :key="`${plugin.repository}:${plugin.id}`" class="plugin-card">
            <div class="plugin-main">
              <div class="plugin-icon" :class="`plugin-icon--${pluginTypeInfo(plugin.types[0]).className}`">
                <t-icon :name="pluginTypeInfo(plugin.types[0]).icon" />
              </div>
              <div class="plugin-copy">
                <div class="plugin-title-row">
                  <strong>{{ plugin.name }}</strong>
                  <t-tag
                    v-for="type in plugin.types"
                    :key="type"
                    size="small"
                    variant="light"
                    :theme="pluginTypeInfo(type).theme"
                  >{{ pluginTypeInfo(type).label }}</t-tag>
                  <t-tag size="small" variant="light">v{{ plugin.version }}</t-tag>
                  <t-tag
                    size="small"
                    variant="light"
                    :theme="plugin.compatible ? 'success' : 'warning'"
                  >{{ plugin.compatible ? '兼容' : '不兼容' }}</t-tag>
                  <t-tag v-if="plugin.certification === 'official'" size="small" theme="primary">官方</t-tag>
                  <t-tag v-else-if="plugin.certification === 'verified'" size="small" theme="success">已认证</t-tag>
                  <t-tag v-else size="small" variant="outline">社区</t-tag>
                </div>
                <p>{{ plugin.description || '暂无描述' }}</p>
                <div class="plugin-meta">
                  <a :href="plugin.repository_url" target="_blank" rel="noopener noreferrer">{{ plugin.repository }}</a>
                  <span>作者：{{ plugin.author }}</span>
                  <span>★ {{ plugin.stars }}</span>
                </div>
                <div class="marketplace-permissions">
                  <span>权限：</span>
                  <t-tag v-if="plugin.permissions.network" size="small" variant="light" theme="warning">访问外网</t-tag>
                  <t-tag
                    v-for="access in plugin.permissions.data_access"
                    :key="access"
                    size="small"
                    variant="light"
                  >{{ access }}</t-tag>
                  <span v-if="!plugin.permissions.network && !plugin.permissions.data_access.length">无额外权限</span>
                </div>
                <div v-if="!plugin.compatible" class="plugin-health">{{ plugin.compatibility_message }}</div>
              </div>
            </div>
            <div class="plugin-actions">
              <t-button size="small" variant="text" @click="openMarketplaceDetail(plugin)">详情</t-button>
              <t-button
                size="small"
                :disabled="marketplaceActionDisabled(plugin)"
                @click="marketplaceAction(plugin)"
              >{{ marketplaceActionLabel(plugin) }}</t-button>
            </div>
          </article>
        </div>
        <t-empty
          v-else-if="!marketplaceLoading && !marketplaceError"
          :description="pluginTypeFilter === 'all' ? '暂未发现有效插件。请确认仓库已添加 weknora-plugin Topic，且根目录包含 plugin.yaml。' : '当前类型暂无插件'"
        />
      </t-loading>

      <div v-if="marketplace && (marketplaceHasPrevious || marketplaceHasNext)" class="marketplace-pagination">
        <t-button variant="outline" :disabled="!marketplaceHasPrevious" @click="changeMarketplacePage(-1)">上一页</t-button>
        <span>第 {{ marketplacePage }} 页</span>
        <t-button variant="outline" :disabled="!marketplaceHasNext" @click="changeMarketplacePage(1)">下一页</t-button>
      </div>
    </template>

    <template v-else>
      <div class="plugin-summary">
        <button
          type="button"
          class="plugin-summary__item"
          :class="{ 'plugin-summary__item--active': pluginOriginFilter === 'all' }"
          @click="pluginOriginFilter = 'all'"
        >全部 {{ builtinCount + externalCount }}</button>
        <button
          type="button"
          class="plugin-summary__item"
          :class="{ 'plugin-summary__item--active': pluginOriginFilter === 'builtin' }"
          @click="pluginOriginFilter = 'builtin'"
        >内置 {{ builtinCount }}</button>
        <button
          type="button"
          class="plugin-summary__item"
          :class="{ 'plugin-summary__item--active': pluginOriginFilter === 'external' }"
          @click="pluginOriginFilter = 'external'"
        >外部 {{ externalCount }}</button>
      </div>

      <t-loading :loading="loading" show-overlay>
        <div v-if="filteredPlugins.length" class="plugin-list">
          <article v-for="plugin in filteredPlugins" :key="plugin.id" class="plugin-card">
            <div class="plugin-main">
              <div class="plugin-icon" :class="`plugin-icon--${pluginTypeInfo(plugin.types[0]).className}`">
                <t-icon :name="pluginTypeInfo(plugin.types[0]).icon" />
              </div>
              <div class="plugin-copy">
                <div class="plugin-title-row">
                  <strong>{{ plugin.name }}</strong>
                  <t-tag
                    v-for="type in plugin.types"
                    :key="type"
                    size="small"
                    variant="light"
                    :theme="pluginTypeInfo(type).theme"
                  >{{ pluginTypeInfo(type).label }}</t-tag>
                  <t-tag size="small" variant="light">{{ plugin.origin === 'builtin' ? '内置' : '外部' }}</t-tag>
                  <t-tag size="small" variant="light" :theme="stateTheme(plugin)">{{ plugin.runtime_state }}</t-tag>
                  <t-tag v-if="plugin.update_available" size="small" variant="light" theme="warning">可升级到 v{{ plugin.latest_version }}</t-tag>
                </div>
                <p>{{ plugin.description || '暂无描述' }}</p>
                <div class="plugin-meta">
                  <span>{{ plugin.id }}</span>
                  <span>v{{ plugin.version }}</span>
                  <span v-if="plugin.connector_type">数据源：{{ plugin.connector_type }}</span>
                </div>
                <div v-if="plugin.health_message" class="plugin-health">{{ plugin.health_message }}</div>
                <div v-if="plugin.last_health_at" class="plugin-health">
                  最近检查：{{ detailTime(plugin.last_health_at) }} · 连续失败 {{ plugin.consecutive_health_failures || 0 }} 次 · 自动恢复 {{ plugin.recovery_attempts || 0 }} 次
                </div>
                <div v-if="plugin.update_message" class="plugin-health">{{ plugin.update_message }}</div>
              </div>
            </div>

            <div class="plugin-actions">
              <t-button size="small" variant="text" @click="openInstalledDetail(plugin)">详情</t-button>
              <template v-if="plugin.origin === 'external'">
                <t-button
                  v-if="plugin.status !== 'enabled'"
                  size="small"
                  :loading="operatingID === plugin.id"
                  @click="run(plugin, 'enable')"
                >启用</t-button>
                <t-button
                  v-else
                  size="small"
                  variant="outline"
                  :loading="operatingID === plugin.id"
                  @click="run(plugin, 'disable')"
                >停用</t-button>
                <t-button size="small" variant="text" :disabled="plugin.status !== 'enabled'" @click="run(plugin, 'health')">健康检查</t-button>
                <t-button size="small" variant="text" @click="openEvents(plugin)">运行日志</t-button>
                <t-button size="small" variant="text" :loading="operatingID === plugin.id" @click="refreshPluginUpdate(plugin)">检查更新</t-button>
                <t-button
                  v-if="plugin.update_available"
                  size="small"
                  theme="warning"
                  @click="openLatestUpgrade(plugin)"
                >升级到 v{{ plugin.latest_version }}</t-button>
                <t-button size="small" variant="text" @click="openUpgrade(plugin)">升级</t-button>
                <t-popconfirm
                  content="卸载前必须先停用，且不能仍有数据源实例使用此插件。"
                  @confirm="run(plugin, 'uninstall')"
                >
                  <t-button size="small" variant="text" theme="danger" :disabled="plugin.status === 'enabled'">卸载</t-button>
                </t-popconfirm>
              </template>
            </div>
          </article>
        </div>
        <t-empty v-else-if="!loading" :description="pluginTypeFilter === 'all' ? '暂无插件' : '当前类型暂无插件'" />
      </t-loading>
    </template>

    <t-dialog
      v-model:visible="editorVisible"
      :header="editorMode === 'install' ? '安装插件' : `升级 ${editingPlugin?.name || ''}`"
      width="680px"
      :confirm-btn="{
        content: manifestOperationStatus === 'success'
          ? '完成'
          : manifestOperationStatus === 'error'
            ? '重试'
            : !manifestInspection
              ? '预检清单与权限'
              : editorMode === 'install' ? '确认并安装' : '确认并升级',
        loading: manifestOperating || manifestInspecting,
      }"
      @confirm="submitManifest"
    >
      <div class="manifest-form">
        <template v-if="manifestOperationStatus === 'idle'">
          <label>清单来源</label>
          <t-radio-group v-model="manifestSource" variant="default-filled">
            <t-radio-button value="url">清单地址</t-radio-button>
            <t-radio-button value="yaml">粘贴 YAML</t-radio-button>
          </t-radio-group>
          <template v-if="manifestSource === 'url'">
            <label>plugin.yaml HTTPS 地址</label>
            <t-input v-model="manifestURL" placeholder="https://example.com/plugin.yaml" />
            <p>WeKnora 会安全下载并校验清单，适合插件市场或仓库 Release 地址。</p>
          </template>
          <template v-else>
            <label>plugin.yaml</label>
            <t-textarea
              v-model="manifestYAML"
              placeholder="粘贴完整插件清单"
              :autosize="{ minRows: 14, maxRows: 22 }"
              spellcheck="false"
            />
          </template>
          <label>单次调用超时（秒）</label>
          <t-input-number v-model="callTimeoutSeconds" :min="1" :max="3600" />
          <p>镜像地址从清单的 spec.image 读取；安装阶段会拉取镜像并执行协议握手，成功后默认保持停用。</p>

          <section v-if="manifestInspection" class="permission-review">
            <div class="permission-review__title">
              <strong>确认 {{ manifestInspection.name }} v{{ manifestInspection.version }} 的权限</strong>
              <t-tag theme="warning" variant="light">安装前必须确认</t-tag>
            </div>
            <div class="plugin-detail-tags">
              <t-tag :theme="manifestInspection.permissions.network ? 'warning' : 'success'" variant="light">
                {{ manifestInspection.permissions.network ? '允许访问外网' : '禁止访问外网' }}
              </t-tag>
              <t-tag v-for="host in manifestInspection.permissions.allowed_hosts" :key="host" variant="outline">域名：{{ host }}</t-tag>
              <t-tag v-for="access in manifestInspection.permissions.data_access" :key="access" variant="outline">数据：{{ access }}</t-tag>
              <t-tag v-for="capability in manifestInspection.capabilities" :key="capability" variant="outline">能力：{{ capability }}</t-tag>
            </div>
            <div class="permission-review__security">
              <strong>供应链</strong>
              <p>发布者：{{ manifestInspection.supply_chain?.publisher || '未声明' }}</p>
              <p>源码：{{ manifestInspection.supply_chain?.source_repository || '未声明' }}</p>
              <p>镜像摘要：{{ manifestInspection.supply_chain?.image_digest || '未声明' }}</p>
              <p>{{ manifestInspection.supply_chain?.signature ? '已声明 Ed25519 签名，安装时由运行时验证' : '未声明镜像签名，是否允许安装由运行时策略决定' }}</p>
            </div>
            <div class="permission-review__security">
              <strong>资源与调用额度</strong>
              <p>
                内存 {{ memoryLimitText(manifestInspection.resources?.memory_bytes || 0) }}；
                CPU {{ cpuLimitText(manifestInspection.resources?.nano_cpus || 0) }}；
                PID {{ quotaLimitText(manifestInspection.resources?.pids_limit || 0) }}
              </p>
              <p>
                最大并发 {{ quotaLimitText(manifestInspection.resources?.max_concurrency || 0) }}；
                每分钟调用 {{ quotaLimitText(manifestInspection.resources?.calls_per_minute || 0, ' 次') }}
              </p>
            </div>
            <t-alert
              v-if="manifestInspection.permission_changes?.length"
              theme="warning"
              title="新版本权限发生变化"
              :message="manifestInspection.permission_changes?.join('；') || ''"
            />
            <p v-if="manifestInspection.secret_fields?.length">密钥字段：{{ manifestInspection.secret_fields.join('、') }}。密钥不会通过普通配置接口回显。</p>
            <t-checkbox v-model="permissionsConfirmed">我已阅读并同意授予以上权限</t-checkbox>
          </section>
          <t-alert v-if="manifestOperationError" theme="error" title="清单预检失败" :message="manifestOperationError" />
        </template>

        <div v-else class="manifest-progress" role="status" aria-live="polite">
          <div v-if="!manifestSteps.length" class="manifest-progress__starting">
            <t-loading size="small" />
            <span>正在创建后端操作任务...</span>
          </div>
          <div
            v-for="(step, index) in manifestSteps"
            :key="step.key"
            class="manifest-step"
            :class="`manifest-step--${step.status}`"
          >
            <div class="manifest-step__rail">
              <span class="manifest-step__dot">
                <t-icon v-if="step.status === 'success'" name="check" />
                <t-icon v-else-if="step.status === 'error'" name="close" />
                <span v-else>{{ index + 1 }}</span>
              </span>
              <span v-if="index < manifestSteps.length - 1" class="manifest-step__line" />
            </div>
            <div class="manifest-step__content">
              <strong>{{ step.title }}</strong>
              <p>{{ step.description }}</p>
            </div>
          </div>
          <t-alert
            v-if="manifestOperationStatus === 'error'"
            theme="error"
            title="插件操作失败"
            :message="manifestOperationError"
          >
            <template #operation>
              <t-button size="small" variant="text" @click="resetManifestOperation">重新预检</t-button>
            </template>
          </t-alert>
          <t-alert
            v-else-if="manifestOperationStatus === 'success'"
            theme="success"
            :title="editorMode === 'install' ? '插件安装成功' : '插件升级成功'"
            :message="editorMode === 'install' ? '插件当前保持停用，请在列表中确认后启用。' : '新版本已经生效。'"
          />
          <p v-else class="manifest-progress__hint">镜像下载耗时取决于镜像大小和网络速度，请不要关闭页面。</p>
        </div>
      </div>
    </t-dialog>

    <t-dialog
      v-model:visible="eventsVisible"
      :header="`${eventPlugin?.name || ''} 运行日志`"
      width="820px"
      :footer="false"
    >
      <t-loading :loading="eventsLoading">
        <div class="event-filters">
          <t-input v-model="eventKind" clearable placeholder="事件类型，例如 call_failed" />
          <t-select
            v-model="eventOutcome"
            clearable
            placeholder="全部结果"
            :options="[
              { label: '成功', value: 'success' },
              { label: '失败', value: 'failed' },
              { label: '已拦截', value: 'denied' },
            ]"
          />
          <input v-model="eventFrom" class="event-date-input" type="datetime-local" aria-label="开始时间" />
          <input v-model="eventTo" class="event-date-input" type="datetime-local" aria-label="结束时间" />
          <t-button size="small" :loading="eventsLoading" @click="loadPluginEvents">查询</t-button>
        </div>
        <div v-if="runtimeEvents.length" class="event-list">
          <div v-for="event in runtimeEvents" :key="event.sequence" class="event-row">
            <span class="event-time">{{ eventTime(event.occurred_at) }}</span>
            <t-tag size="small" variant="light" :theme="eventTheme(event)">{{ event.kind }}</t-tag>
            <span class="event-message">{{ event.message }}</span>
            <span v-if="event.details?.duration_ms" class="event-duration">{{ event.details.duration_ms }} ms</span>
            <span v-if="event.details?.code" class="event-code">{{ event.details.code }}</span>
            <span v-if="event.details?.outcome" class="event-code">{{ event.details.outcome }}</span>
          </div>
        </div>
        <t-empty v-else-if="!eventsLoading" description="暂无运行事件" />
      </t-loading>
    </t-dialog>

    <t-drawer
      v-model:visible="detailVisible"
      :header="detailPlugin?.name || '插件详情'"
      size="760px"
      :footer="false"
      destroy-on-close
    >
      <t-loading :loading="detailLoading">
        <div v-if="detailPlugin" class="plugin-detail">
          <section class="plugin-detail-section">
            <h3>基本信息</h3>
            <div class="plugin-detail-grid">
              <span>插件 ID</span><code>{{ detailPlugin.id }}</code>
              <span>插件版本</span><strong>v{{ detailPlugin.version }}</strong>
              <span>协议版本</span><strong>v{{ detailPlugin.protocolVersion || '-' }}</strong>
              <span>WeKnora 兼容范围</span><strong>{{ detailPlugin.weknoraVersion || '未限制' }}</strong>
              <span>镜像</span><code>{{ detailPlugin.image }}</code>
              <span>来源</span><strong>{{ detailPlugin.origin === 'builtin' ? '内置' : detailPlugin.origin === 'external' ? '外部安装' : '插件市场' }}</strong>
              <span>最近更新</span><strong>{{ detailTime(detailPlugin.updatedAt) }}</strong>
            </div>
            <p>{{ detailPlugin.description || '暂无描述' }}</p>
            <a v-if="detailPlugin.repositoryURL" :href="detailPlugin.repositoryURL" target="_blank" rel="noopener noreferrer">查看插件仓库</a>
          </section>

          <section class="plugin-detail-section">
            <h3>扩展能力</h3>
            <div class="plugin-detail-tags">
              <t-tag
                v-for="type in detailPlugin.types"
                :key="type"
                variant="light"
                :theme="pluginTypeInfo(type).theme"
              >{{ pluginTypeInfo(type).label }}</t-tag>
              <t-tag v-for="capability in detailPlugin.capabilities" :key="capability" variant="outline">{{ capability }}</t-tag>
            </div>
          </section>

          <section class="plugin-detail-section">
            <h3>权限声明</h3>
            <div class="plugin-detail-tags">
              <t-tag :theme="detailPlugin.permissions.network ? 'warning' : 'success'" variant="light">
                {{ detailPlugin.permissions.network ? '允许访问外网' : '禁止访问外网' }}
              </t-tag>
              <t-tag v-for="host in detailPlugin.permissions.allowedHosts" :key="host" variant="outline">域名：{{ host }}</t-tag>
              <t-tag v-for="access in detailPlugin.permissions.dataAccess" :key="access" variant="outline">数据：{{ access }}</t-tag>
            </div>
          </section>

          <section class="plugin-detail-section">
            <h3>供应链校验</h3>
            <div class="plugin-detail-grid">
              <span>发布者</span><strong>{{ detailPlugin.supplyChain.publisher || '未声明' }}</strong>
              <span>源码仓库</span><code>{{ detailPlugin.supplyChain.sourceRepository || '未声明' }}</code>
              <span>镜像摘要</span><code>{{ detailPlugin.supplyChain.imageDigest || '未声明' }}</code>
              <span>Ed25519 签名</span><strong>{{ detailPlugin.supplyChain.signature ? '已声明' : '未声明' }}</strong>
            </div>
          </section>

          <section class="plugin-detail-section">
            <h3>资源与调用额度</h3>
            <div class="plugin-detail-grid">
              <span>内存</span><strong>{{ memoryLimitText(detailPlugin.resources.memoryBytes) }}</strong>
              <span>CPU</span><strong>{{ cpuLimitText(detailPlugin.resources.nanoCPUs) }}</strong>
              <span>PID</span><strong>{{ quotaLimitText(detailPlugin.resources.pidsLimit) }}</strong>
              <span>最大并发</span><strong>{{ quotaLimitText(detailPlugin.resources.maxConcurrency) }}</strong>
              <span>每分钟调用</span><strong>{{ quotaLimitText(detailPlugin.resources.callsPerMinute, ' 次') }}</strong>
            </div>
          </section>

          <section class="plugin-detail-section">
            <h3>配置 JSON Schema</h3>
            <div v-if="detailPlugin.secretFields.length" class="plugin-detail-secret">
              密钥字段：{{ detailPlugin.secretFields.join('、') }}
            </div>
            <pre>{{ detailConfigSchema }}</pre>
            <div v-for="specification in detailTypeSpecifications" :key="specification.type" class="plugin-type-specification">
              <strong>{{ pluginTypeInfo(specification.type).label }}配置规范</strong>
              <p>配置作用域：{{ specification.config_scope }}</p>
              <p>清单必填：{{ specification.required_manifest_fields.join('、') }}</p>
              <p>密钥字段：{{ specification.supported_secret_fields.length ? specification.supported_secret_fields.join('、') : '不支持' }}</p>
              <p>能力示例：{{ specification.capability_examples.join('、') }}</p>
            </div>
          </section>

          <section class="plugin-detail-section">
            <h3>更新记录</h3>
            <div v-if="detailHistory.length" class="plugin-history">
              <div v-for="entry in detailHistory" :key="entry.id" class="plugin-history-item">
                <span class="plugin-history-dot" />
                <div>
                  <strong>{{ historyDescription(entry) }}</strong>
                  <p>{{ detailTime(entry.created_at) }} · 操作者 {{ entry.actor_user_id || '系统' }}</p>
                </div>
              </div>
            </div>
            <p v-else>{{ detailPlugin.origin === 'marketplace' ? '尚未安装，无本地更新记录。' : detailPlugin.origin === 'builtin' ? '内置插件跟随 WeKnora 版本更新。' : '暂无更新记录。' }}</p>
          </section>
        </div>
      </t-loading>
    </t-drawer>
  </div>
</template>

<style scoped>
.plugin-manager { min-height: 480px; color: var(--td-text-color-primary); }
.plugin-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 24px; margin-bottom: 18px; }
.plugin-header h2 { margin: 0 0 8px; font-size: 22px; }
.plugin-header p { margin: 0; color: var(--td-text-color-secondary); }
.plugin-header-actions { display: flex; gap: 8px; }
.plugin-sections { margin-bottom: 18px; }
.plugin-type-filter { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; margin-bottom: 18px; color: var(--td-text-color-secondary); font-size: 13px; }
.plugin-summary { display: flex; gap: 22px; margin-bottom: 14px; border-bottom: 1px solid var(--td-component-stroke); }
.plugin-summary__item { position: relative; padding: 0 0 10px; border: 0; color: var(--td-text-color-secondary); background: transparent; cursor: pointer; font: inherit; font-size: 13px; }
.plugin-summary__item:hover { color: var(--td-text-color-primary); }
.plugin-summary__item--active { color: var(--td-brand-color); font-weight: 600; }
.plugin-summary__item--active::after { position: absolute; right: 0; bottom: -1px; left: 0; height: 2px; border-radius: 2px; background: var(--td-brand-color); content: ''; }
.marketplace-toolbar { display: grid; grid-template-columns: minmax(220px, 1fr) auto 130px 130px auto; align-items: center; gap: 8px; margin-bottom: 14px; }
.marketplace-summary { display: flex; flex-wrap: wrap; gap: 16px; margin: 12px 0; color: var(--td-text-color-secondary); font-size: 13px; }
.marketplace-list { grid-template-columns: minmax(0, 1fr); }
.marketplace-permissions { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-top: 10px; color: var(--td-text-color-secondary); font-size: 12px; }
.marketplace-pagination { display: flex; align-items: center; justify-content: center; gap: 14px; margin-top: 18px; }
.plugin-list { display: grid; gap: 12px; }
.plugin-card { padding: 18px; border: 1px solid var(--td-component-border); border-radius: 10px; background: var(--td-bg-color-container); }
.plugin-main { display: flex; gap: 14px; }
.plugin-icon { display: grid; place-items: center; flex: 0 0 40px; height: 40px; border-radius: 9px; color: var(--td-brand-color); background: var(--td-brand-color-light); font-size: 20px; }
.plugin-icon--document-parser { color: var(--td-success-color); background: var(--td-success-color-light); }
.plugin-icon--web-search { color: var(--td-warning-color); background: var(--td-warning-color-light); }
.plugin-icon--model-provider { color: #8b5cf6; background: rgb(139 92 246 / 12%); }
.plugin-icon--retrieval-engine { color: #e34d59; background: rgb(227 77 89 / 12%); }
.plugin-icon--other { color: var(--td-text-color-secondary); background: var(--td-bg-color-secondarycontainer); }
.plugin-copy { min-width: 0; flex: 1; }
.plugin-title-row { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.plugin-copy p { margin: 8px 0; color: var(--td-text-color-secondary); }
.plugin-meta { display: flex; flex-wrap: wrap; gap: 6px 14px; color: var(--td-text-color-placeholder); font-size: 12px; }
.plugin-meta a { color: var(--td-brand-color); text-decoration: none; }
.plugin-health { margin-top: 8px; color: var(--td-text-color-secondary); font-size: 12px; }
.plugin-actions { display: flex; justify-content: flex-end; gap: 6px; margin-top: 14px; padding-top: 12px; border-top: 1px solid var(--td-component-stroke); }
.manifest-form { display: grid; gap: 10px; }
.manifest-form label { font-weight: 600; }
.manifest-form p { margin: 0; color: var(--td-text-color-secondary); font-size: 12px; }
.permission-review { display: grid; gap: 12px; margin-top: 8px; padding: 14px; border: 1px solid var(--td-warning-color-4); border-radius: 8px; background: var(--td-warning-color-1); }
.permission-review__title { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.permission-review__security { display: grid; gap: 4px; padding: 10px; border-radius: 6px; background: var(--td-bg-color-container); }
.manifest-progress { display: grid; gap: 16px; padding: 6px 2px; }
.manifest-step { display: grid; grid-template-columns: 28px minmax(0, 1fr); gap: 12px; min-height: 58px; }
.manifest-step__rail { display: flex; align-items: center; flex-direction: column; }
.manifest-step__dot { display: grid; place-items: center; width: 26px; height: 26px; border: 1px solid var(--td-component-border); border-radius: 50%; color: var(--td-text-color-placeholder); background: var(--td-bg-color-container); font-size: 12px; }
.manifest-step__line { width: 1px; flex: 1; background: var(--td-component-stroke); }
.manifest-step__content { padding: 3px 0 14px; }
.manifest-step__content strong { font-size: 14px; }
.manifest-step__content p { margin-top: 5px; }
.manifest-step--running .manifest-step__dot { border-color: var(--td-brand-color); color: white; background: var(--td-brand-color); animation: manifest-pulse 1.4s ease-in-out infinite; }
.manifest-step--success .manifest-step__dot { border-color: var(--td-success-color); color: white; background: var(--td-success-color); }
.manifest-step--success .manifest-step__line { background: var(--td-success-color); }
.manifest-step--error .manifest-step__dot { border-color: var(--td-error-color); color: white; background: var(--td-error-color); }
.manifest-progress__starting { display: flex; align-items: center; justify-content: center; gap: 10px; min-height: 120px; color: var(--td-text-color-secondary); }
.manifest-progress__hint { text-align: center; }
@keyframes manifest-pulse { 50% { box-shadow: 0 0 0 5px var(--td-brand-color-light); } }
.event-filters { display: grid; grid-template-columns: minmax(160px, 1fr) 120px 180px 180px auto; gap: 8px; margin-bottom: 14px; }
.event-date-input { min-width: 0; height: 32px; padding: 0 10px; border: 1px solid var(--td-component-border); border-radius: 6px; color: var(--td-text-color-primary); background: var(--td-bg-color-container); }
.event-list { display: grid; max-height: 520px; overflow: auto; }
.event-row { display: grid; grid-template-columns: 150px 120px minmax(0, 1fr) auto auto auto; align-items: center; gap: 10px; padding: 10px 4px; border-bottom: 1px solid var(--td-component-stroke); font-size: 12px; }
.event-time, .event-duration, .event-code { color: var(--td-text-color-secondary); }
.event-message { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.plugin-detail { display: grid; gap: 22px; padding: 0 4px 24px; }
.plugin-detail-section { display: grid; gap: 12px; }
.plugin-detail-section + .plugin-detail-section { padding-top: 20px; border-top: 1px solid var(--td-component-stroke); }
.plugin-detail-section h3 { margin: 0; font-size: 16px; }
.plugin-detail-section p { margin: 0; color: var(--td-text-color-secondary); }
.plugin-detail-section a { color: var(--td-brand-color); text-decoration: none; }
.plugin-detail-grid { display: grid; grid-template-columns: 140px minmax(0, 1fr); gap: 10px 16px; align-items: start; }
.plugin-detail-grid > span { color: var(--td-text-color-secondary); }
.plugin-detail-grid code { overflow-wrap: anywhere; }
.plugin-detail-tags { display: flex; flex-wrap: wrap; gap: 8px; }
.plugin-detail-secret { color: var(--td-warning-color); font-size: 13px; }
.plugin-detail-section pre { max-height: 360px; margin: 0; padding: 14px; overflow: auto; border-radius: 8px; background: var(--td-bg-color-secondarycontainer); font-size: 12px; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; }
.plugin-type-specification { display: grid; gap: 5px; padding: 12px; border: 1px solid var(--td-component-stroke); border-radius: 8px; }
.plugin-history { display: grid; gap: 4px; }
.plugin-history-item { display: grid; grid-template-columns: 12px minmax(0, 1fr); gap: 10px; padding: 8px 0; }
.plugin-history-dot { width: 9px; height: 9px; margin-top: 5px; border-radius: 50%; background: var(--td-brand-color); box-shadow: 0 0 0 4px var(--td-brand-color-light); }
.plugin-history-item p { margin-top: 5px; font-size: 12px; }
@media (max-width: 900px) {
  .plugin-header { flex-direction: column; }
  .marketplace-toolbar { grid-template-columns: minmax(0, 1fr) auto; }
  .marketplace-toolbar > *:nth-child(n + 3) { grid-column: span 1; }
  .event-filters { grid-template-columns: 1fr 1fr; }
  .plugin-actions { flex-wrap: wrap; }
}
</style>
