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
  call_timeout_seconds: number
  installed_by?: string
  created_at?: string
  updated_at?: string
}

export interface PluginInstallPayload {
  manifest_yaml: string
  image?: string
  call_timeout_seconds?: number
}

export async function listPlugins(): Promise<InstalledPlugin[]> {
  return (await get('/api/v1/system/admin/plugins')) as unknown as InstalledPlugin[]
}

export async function installPlugin(payload: PluginInstallPayload): Promise<InstalledPlugin> {
  return (await post('/api/v1/system/admin/plugins', payload)) as unknown as InstalledPlugin
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

export async function uninstallPlugin(id: string): Promise<void> {
  await del(`/api/v1/system/admin/plugins/${encodeURIComponent(id)}`)
}
