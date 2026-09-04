import { get, post, put, del } from '@/utils/request'

export type PluginOrigin = 'builtin' | 'external'
export type PluginStatus = 'enabled' | 'disabled' | 'error'

export interface InstalledPlugin {
  id: string
  name: string
  description: string
  version: string
  protocol_version: string
  weknora_version_constraint: string
  image: string
  image_digest: string
  origin: PluginOrigin
  types: string[]
  capabilities: string[]
  connector_type?: string
  manifest: Record<string, unknown>
  status: PluginStatus
  runtime_state: string
  health_message: string
  last_health_at?: string
  consecutive_health_failures: number
  recovery_attempts: number
  last_recovery_at?: string
  source_manifest_url?: string
  latest_version?: string
  update_available: boolean
  update_checked_at?: string
  update_message?: string
  call_timeout_seconds: number
  installed_by?: string
  created_at?: string
  updated_at?: string
}

export interface PluginInstallPayload {
  manifest_yaml?: string
  manifest_url?: string
  image?: string
  call_timeout_seconds?: number
  plugin_id?: string
  permissions_confirmed?: boolean
  permission_digest?: string
}

export interface PluginRuntimeEvent {
  sequence: number
  occurred_at?: string
  plugin_id: string
  kind: string
  message: string
  details?: Record<string, string>
}

export interface PluginMarketplacePermissions {
  network: boolean
  allowed_hosts: string[]
  data_access: string[]
}

export interface PluginSupplyChain {
  publisher: string
  source_repository: string
  image_digest: string
  signature: string
}

export interface PluginResources {
  memory_bytes: number
  nano_cpus: number
  pids_limit: number
  max_concurrency: number
  calls_per_minute: number
}

export interface PluginMarketplaceItem {
  id: string
  name: string
  description: string
  version: string
  protocol_version: string
  weknora_version_constraint: string
  image: string
  types: string[]
  capabilities: string[]
  icon: string
  config_schema: Record<string, unknown>
  secret_fields: string[]
  permissions: PluginMarketplacePermissions
  supply_chain: PluginSupplyChain
  resources: PluginResources
  repository: string
  repository_url: string
  manifest_url: string
  author: string
  stars: number
  updated_at: string
  compatible: boolean
  compatibility_message?: string
  certification: 'official' | 'verified' | 'community'
}

export interface PluginManifestInspection {
  id: string
  name: string
  version: string
  types: string[]
  capabilities: string[]
  permissions: PluginMarketplacePermissions
  supply_chain: PluginSupplyChain
  resources: PluginResources
  secret_fields: string[]
  config_schema: Record<string, unknown>
  permission_digest: string
  permission_changes: string[]
  weknora_version_constraint: string
}

export interface PluginTypeSpecification {
  type: string
  config_scope: string
  required_manifest_fields: string[]
  supported_secret_fields: string[]
  capability_examples: string[]
}

export type PluginOperationStatus = 'running' | 'success' | 'error'
export type PluginOperationStepStatus = 'pending' | 'running' | 'success' | 'error'

export interface PluginOperationStep {
  key: string
  title: string
  description: string
  status: PluginOperationStepStatus
  started_at?: string
  finished_at?: string
}

export interface PluginOperation {
  id: string
  kind: 'install' | 'upgrade'
  plugin_id?: string
  status: PluginOperationStatus
  steps: PluginOperationStep[]
  result?: InstalledPlugin
  error?: string
  created_at: string
  updated_at: string
}

export interface PluginHistoryEntry {
  id: number
  action: 'plugin.installed' | 'plugin.upgraded'
  actor_user_id: string
  old_version?: string
  new_version: string
  created_at: string
}

export interface PluginMarketplaceResult {
  topic: string
  items: PluginMarketplaceItem[]
  repository_count: number
  skipped_count: number
  page: number
  page_size: number
  rate_limit_remaining?: number
  github_repository_count: number
  stale: boolean
  cached_at: string
}

export async function listPlugins(): Promise<InstalledPlugin[]> {
  return (await get('/api/v1/system/admin/plugins')) as unknown as InstalledPlugin[]
}

export async function listPluginMarketplace(params: {
  search?: string
  type?: string
  certification?: string
  sort?: 'updated' | 'stars' | 'name'
  compatible_only?: boolean
  page?: number
  page_size?: number
  refresh?: boolean
} = {}): Promise<PluginMarketplaceResult> {
  return (await get('/api/v1/system/admin/plugins/marketplace', params)) as unknown as PluginMarketplaceResult
}

export async function installPlugin(payload: PluginInstallPayload): Promise<InstalledPlugin> {
  return (await post('/api/v1/system/admin/plugins', payload)) as unknown as InstalledPlugin
}

export async function inspectPluginManifest(payload: PluginInstallPayload): Promise<PluginManifestInspection> {
  return (await post('/api/v1/system/admin/plugins/inspect', payload)) as unknown as PluginManifestInspection
}

export async function listPluginTypeSpecifications(): Promise<PluginTypeSpecification[]> {
  return (await get('/api/v1/system/admin/plugins/type-specifications')) as unknown as PluginTypeSpecification[]
}

export async function startPluginInstall(payload: PluginInstallPayload): Promise<PluginOperation> {
  return (await post('/api/v1/system/admin/plugins/operations/install', payload)) as unknown as PluginOperation
}

export async function startPluginUpgrade(id: string, payload: PluginInstallPayload): Promise<PluginOperation> {
  return (await post(`/api/v1/system/admin/plugins/${encodeURIComponent(id)}/operations/upgrade`, payload)) as unknown as PluginOperation
}

export async function getPluginOperation(id: string): Promise<PluginOperation> {
  return (await get(`/api/v1/system/admin/plugins/operations/${encodeURIComponent(id)}`)) as unknown as PluginOperation
}

export async function upgradePlugin(id: string, payload: PluginInstallPayload): Promise<InstalledPlugin> {
  return (await put(`/api/v1/system/admin/plugins/${encodeURIComponent(id)}`, payload)) as unknown as InstalledPlugin
}

export async function enablePlugin(id: string): Promise<InstalledPlugin> {
  return (await post(`/api/v1/system/admin/plugins/${encodeURIComponent(id)}/enable`)) as unknown as InstalledPlugin
}

export async function disablePlugin(id: string): Promise<InstalledPlugin> {
  return (await post(`/api/v1/system/admin/plugins/${encodeURIComponent(id)}/disable`)) as unknown as InstalledPlugin
}

export async function checkPluginHealth(id: string): Promise<InstalledPlugin> {
  return (await post(`/api/v1/system/admin/plugins/${encodeURIComponent(id)}/health`)) as unknown as InstalledPlugin
}

export async function checkPluginUpdate(id: string): Promise<InstalledPlugin> {
  return (await post(`/api/v1/system/admin/plugins/${encodeURIComponent(id)}/check-update`)) as unknown as InstalledPlugin
}

export async function startPluginLatestUpgrade(id: string, payload: PluginInstallPayload): Promise<PluginOperation> {
  return (await post(`/api/v1/system/admin/plugins/${encodeURIComponent(id)}/operations/upgrade-latest`, payload)) as unknown as PluginOperation
}

export async function uninstallPlugin(id: string): Promise<void> {
  await del(`/api/v1/system/admin/plugins/${encodeURIComponent(id)}`)
}

export async function listPluginEvents(id: string, params: {
  limit?: number
  after_id?: number
  kind?: string
  outcome?: 'success' | 'failed' | 'denied'
  from?: string
  to?: string
} = {}): Promise<PluginRuntimeEvent[]> {
  return (await get(`/api/v1/system/admin/plugins/${encodeURIComponent(id)}/events`, { limit: 100, ...params })) as unknown as PluginRuntimeEvent[]
}

export async function listPluginHistory(id: string): Promise<PluginHistoryEntry[]> {
  return (await get(`/api/v1/system/admin/plugins/${encodeURIComponent(id)}/history`)) as unknown as PluginHistoryEntry[]
}
