<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  checkPluginHealth,
  disablePlugin,
  enablePlugin,
  installPlugin,
  listPlugins,
  uninstallPlugin,
  upgradePlugin,
  type InstalledPlugin,
} from '@/api/plugin'

const loading = ref(false)
const operatingID = ref('')
const plugins = ref<InstalledPlugin[]>([])
const editorVisible = ref(false)
const editorMode = ref<'install' | 'upgrade'>('install')
const editingPlugin = ref<InstalledPlugin | null>(null)
const manifestYAML = ref('')
const callTimeoutSeconds = ref(600)

const builtinCount = computed(() => plugins.value.filter(item => item.origin === 'builtin').length)
const externalCount = computed(() => plugins.value.filter(item => item.origin === 'external').length)

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

function openInstall() {
  editorMode.value = 'install'
  editingPlugin.value = null
  manifestYAML.value = ''
  callTimeoutSeconds.value = 600
  editorVisible.value = true
}

function openUpgrade(plugin: InstalledPlugin) {
  editorMode.value = 'upgrade'
  editingPlugin.value = plugin
  manifestYAML.value = ''
  callTimeoutSeconds.value = plugin.call_timeout_seconds || 600
  editorVisible.value = true
}

async function submitManifest() {
  if (!manifestYAML.value.trim()) {
    MessagePlugin.warning('请粘贴 plugin.yaml 内容')
    return
  }
  operatingID.value = editingPlugin.value?.id || '__install__'
  try {
    const payload = {
      manifest_yaml: manifestYAML.value,
      call_timeout_seconds: callTimeoutSeconds.value,
    }
    if (editorMode.value === 'upgrade' && editingPlugin.value) {
      await upgradePlugin(editingPlugin.value.id, payload)
      MessagePlugin.success('插件升级成功')
    } else {
      await installPlugin(payload)
      MessagePlugin.success('插件安装并校验成功，启用后才会提供能力')
    }
    editorVisible.value = false
    await refresh()
  } catch (error: any) {
    MessagePlugin.error(error?.message || '插件操作失败')
  } finally {
    operatingID.value = ''
  }
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

onMounted(refresh)
</script>

<template>
  <div class="plugin-manager">
    <div class="plugin-header">
      <div>
        <h2>插件管理</h2>
        <p>安装和管理系统级插件。外部插件在隔离容器中运行，不能直接访问数据库或 Redis。</p>
      </div>
      <t-button theme="primary" @click="openInstall">
        <template #icon><t-icon name="add" /></template>
        安装插件
      </t-button>
    </div>

    <div class="plugin-summary">
      <span>内置 {{ builtinCount }}</span>
      <span>外部 {{ externalCount }}</span>
    </div>

    <t-loading :loading="loading" show-overlay>
      <div v-if="plugins.length" class="plugin-list">
        <article v-for="plugin in plugins" :key="plugin.id" class="plugin-card">
          <div class="plugin-main">
            <div class="plugin-icon"><t-icon :name="plugin.origin === 'builtin' ? 'component-layout' : 'extension'" /></div>
            <div class="plugin-copy">
              <div class="plugin-title-row">
                <strong>{{ plugin.name }}</strong>
                <t-tag size="small" variant="light">{{ plugin.origin === 'builtin' ? '内置' : '外部' }}</t-tag>
                <t-tag size="small" variant="light" :theme="stateTheme(plugin)">{{ plugin.runtime_state }}</t-tag>
              </div>
              <p>{{ plugin.description || '暂无描述' }}</p>
              <div class="plugin-meta">
                <span>{{ plugin.id }}</span>
                <span>v{{ plugin.version }}</span>
                <span v-if="plugin.connector_type">数据源：{{ plugin.connector_type }}</span>
                <span v-for="type in plugin.types" :key="type">{{ type }}</span>
              </div>
              <div v-if="plugin.health_message" class="plugin-health">{{ plugin.health_message }}</div>
            </div>
          </div>

          <div v-if="plugin.origin === 'external'" class="plugin-actions">
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
            <t-button size="small" variant="text" @click="openUpgrade(plugin)">升级</t-button>
            <t-popconfirm
              content="卸载前必须先停用，且不能仍有数据源实例使用此插件。"
              @confirm="run(plugin, 'uninstall')"
            >
              <t-button size="small" variant="text" theme="danger" :disabled="plugin.status === 'enabled'">卸载</t-button>
            </t-popconfirm>
          </div>
        </article>
      </div>
      <t-empty v-else-if="!loading" description="暂无插件" />
    </t-loading>

    <t-dialog
      v-model:visible="editorVisible"
      :header="editorMode === 'install' ? '安装插件' : `升级 ${editingPlugin?.name || ''}`"
      width="680px"
      :confirm-btn="{ content: editorMode === 'install' ? '安装并校验' : '升级并校验', loading: operatingID !== '' }"
      @confirm="submitManifest"
    >
      <div class="manifest-form">
        <label>plugin.yaml</label>
        <t-textarea
          v-model="manifestYAML"
          placeholder="粘贴完整插件清单"
          :autosize="{ minRows: 14, maxRows: 22 }"
          spellcheck="false"
        />
        <label>单次调用超时（秒）</label>
        <t-input-number v-model="callTimeoutSeconds" :min="1" :max="3600" />
        <p>镜像地址从清单的 spec.image 读取；安装阶段会拉取镜像并执行协议握手，成功后默认保持停用。</p>
      </div>
    </t-dialog>
  </div>
</template>

<style scoped>
.plugin-manager { min-height: 480px; color: var(--td-text-color-primary); }
.plugin-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 24px; margin-bottom: 18px; }
.plugin-header h2 { margin: 0 0 8px; font-size: 22px; }
.plugin-header p { margin: 0; color: var(--td-text-color-secondary); }
.plugin-summary { display: flex; gap: 18px; margin-bottom: 14px; color: var(--td-text-color-secondary); font-size: 13px; }
.plugin-list { display: grid; gap: 12px; }
.plugin-card { padding: 18px; border: 1px solid var(--td-component-border); border-radius: 10px; background: var(--td-bg-color-container); }
.plugin-main { display: flex; gap: 14px; }
.plugin-icon { display: grid; place-items: center; flex: 0 0 40px; height: 40px; border-radius: 9px; color: var(--td-brand-color); background: var(--td-brand-color-light); font-size: 20px; }
.plugin-copy { min-width: 0; flex: 1; }
.plugin-title-row { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.plugin-copy p { margin: 8px 0; color: var(--td-text-color-secondary); }
.plugin-meta { display: flex; flex-wrap: wrap; gap: 6px 14px; color: var(--td-text-color-placeholder); font-size: 12px; }
.plugin-health { margin-top: 8px; color: var(--td-text-color-secondary); font-size: 12px; }
.plugin-actions { display: flex; justify-content: flex-end; gap: 6px; margin-top: 14px; padding-top: 12px; border-top: 1px solid var(--td-component-stroke); }
.manifest-form { display: grid; gap: 10px; }
.manifest-form label { font-weight: 600; }
.manifest-form p { margin: 0; color: var(--td-text-color-secondary); font-size: 12px; }
</style>
