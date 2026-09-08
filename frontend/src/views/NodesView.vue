<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import PageHeader from '@/components/PageHeader.vue'
import LiveControls from '@/components/LiveControls.vue'
import { api, encodeID } from '@/api.js'
import { useDataStore } from '@/stores/data.js'
import { useRealtimeStore } from '@/stores/realtime.js'
import { useUIStore } from '@/stores/ui.js'
import { copyText, formatBytes, nodeConnectionStats, latestHealth } from '@/utils.js'

const router = useRouter()
const data = useDataStore()
const realtime = useRealtimeStore()
const ui = useUIStore()
const { nodes, providers } = storeToRefs(data)
const { connections } = storeToRefs(realtime)
const name = ref('')
const provider = ref('')
const type = ref('')
const alive = ref('')
const menuOpen = ref('')
const layoutKey = 'surgeeb-node-layout'
const layout = ref(localStorage.getItem(layoutKey) === 'list' ? 'list' : 'grid')
const results = reactive({})
const errors = reactive({})
const busy = reactive(new Set())

const providerOptions = computed(() => [...new Map(nodes.value.filter((node) => node.provider_id).map((node) => [node.provider_id, node.provider_name || node.provider_id])).entries()])
const types = computed(() => [...new Set(nodes.value.map((node) => node.type).filter(Boolean))].sort())
const filtered = computed(() => nodes.value.filter((node) =>
  (!name.value || String(node.name || '').toLowerCase().includes(name.value.toLowerCase())) &&
  (!provider.value || node.provider_id === provider.value) &&
  (!type.value || node.type === type.value) &&
  (!alive.value || String(Boolean(node.alive)) === alive.value)))
const hasFilters = computed(() => Boolean(name.value || provider.value || type.value || alive.value))
const aliveCount = computed(() => nodes.value.filter((node) => node.alive).length)
const unavailableCount = computed(() => nodes.value.length - aliveCount.value)
const activeConnectionCount = computed(() => connections.value.connections?.length || 0)
const connectionStats = computed(() => {
  const current = connections.value.connections || []
  return new Map(nodes.value.map((node) => [node.id, nodeConnectionStats(node, current)]))
})

function clearFilters() { name.value = ''; provider.value = ''; type.value = ''; alive.value = '' }
function setLayout(value) {
  layout.value = value
  menuOpen.value = ''
  localStorage.setItem(layoutKey, value)
}
function capabilityList(node) { return [node.udp && 'UDP', node.uot && 'UOT', node.xudp && 'XUDP', node.tfo && 'TFO', node.mptcp && 'MPTCP', node.smux && 'SMUX'].filter(Boolean) }
function stats(node) { return connectionStats.value.get(node.id) || { count: 0, upload: 0, download: 0, chains: [] } }
function previousDelays(node) { return (Array.isArray(node.history) ? node.history : []).slice(-4, -1).map((item) => latestHealth([item]).label).join(' / ') }
function chainLabel(node) {
  const ownNames = new Set([node.name, node.proxy_name].filter(Boolean))
  return stats(node).chains.filter((item) => !ownNames.has(item)).join(' → ')
}
function diagnosticError(node) { return errors[node.id] || '' }
function toggleMenu(id) { menuOpen.value = menuOpen.value === id ? '' : id }
function closeMenu() { menuOpen.value = '' }
onMounted(() => document.addEventListener('click', closeMenu))
onBeforeUnmount(() => document.removeEventListener('click', closeMenu))

async function nodeAction(node, action) {
  if (busy.has(node.id)) return
  busy.add(node.id)
  try {
    if (action === 'diagnose') {
      results[node.id] = await api(`/api/nodes/${encodeID(node.id)}/diagnose`, { method: 'POST' })
      delete errors[node.id]
    } else if (action === 'health') {
      const result = await api(`/api/nodes/${encodeID(node.id)}/healthcheck`, { method: 'POST' })
      await data.refreshAfterMutation(['nodes'])
      ui.toast(`Mihomo 延迟 ${result.delay || '—'} ms`)
    } else if (action === 'copy') {
      if (!window.confirm('单节点行包含 SOCKS 密码，确认复制？')) return
      const result = await api(`/api/nodes/${encodeID(node.id)}/surge-line`, { headers: { 'X-SurgeEB-Confirm': 'reveal-node-credential' } })
      await copyText(result.line)
      ui.toast('Surge 节点行已复制')
    }
  } catch (error) {
    if (action === 'diagnose') errors[node.id] = error.message
    ui.toast(error.message, true)
  } finally { busy.delete(node.id) }
}

function runAction(node, action) {
  menuOpen.value = ''
  return nodeAction(node, action)
}
</script>

<template>
  <PageHeader eyebrow="NODES" title="当前节点" description="查看节点测速与实时使用情况；连接诊断通过网关测试 TCP/UDP，不经过 Surge。"><LiveControls /></PageHeader>

  <template v-if="nodes.length">
    <div class="node-summary" data-testid="node-summary">
      <span><b>{{ nodes.length }}</b> 个节点</span>
      <span class="ok"><b>{{ aliveCount }}</b> 个可用</span>
      <span v-if="unavailableCount" class="bad"><b>{{ unavailableCount }}</b> 个需检查</span>
      <span><b>{{ activeConnectionCount }}</b> 个实时连接</span>
      <div class="node-view-switch" role="group" aria-label="节点显示方式">
        <button type="button" aria-label="网格卡片" :aria-pressed="layout === 'grid'" @click="setLayout('grid')"><svg viewBox="0 0 16 16" aria-hidden="true"><rect x="2" y="2" width="4" height="4" rx="1"/><rect x="10" y="2" width="4" height="4" rx="1"/><rect x="2" y="10" width="4" height="4" rx="1"/><rect x="10" y="10" width="4" height="4" rx="1"/></svg>网格卡片</button>
        <button type="button" aria-label="列表卡片" :aria-pressed="layout === 'list'" @click="setLayout('list')"><svg viewBox="0 0 16 16" aria-hidden="true"><rect x="2" y="2" width="12" height="4" rx="1"/><rect x="2" y="10" width="12" height="4" rx="1"/></svg>列表卡片</button>
      </div>
    </div>

    <section class="node-filter-panel" aria-label="节点筛选">
      <div class="filter-bar">
        <input v-model="name" class="search" aria-label="按节点名称过滤" placeholder="搜索节点名称">
        <select v-model="provider" class="search" aria-label="按 Provider 过滤"><option value="">全部 Provider</option><option v-for="item in providerOptions" :key="item[0]" :value="item[0]">{{ item[1] }}</option></select>
        <select v-model="type" class="search" aria-label="按协议过滤"><option value="">全部协议</option><option v-for="item in types" :key="item" :value="item">{{ item.toUpperCase() }}</option></select>
        <select v-model="alive" class="search" aria-label="按存活状态过滤"><option value="">全部状态</option><option value="true">可用</option><option value="false">需检查</option></select>
      </div>
      <div class="node-filter-summary"><span>显示 {{ filtered.length }} / {{ nodes.length }} 个节点</span><button v-if="hasFilters" type="button" @click="clearFilters">清除筛选</button></div>
    </section>

    <div v-if="filtered.length" class="node-card-grid" :class="{ 'node-card-list': layout === 'list' }">
      <article v-for="node in filtered" :key="node.id" class="node-card" :class="{ unavailable: !node.alive }" data-testid="node-card">
        <header class="node-card-header">
          <div class="node-card-identity">
            <div class="node-card-title"><h3 :title="node.name">{{ node.name }}</h3></div>
            <div class="node-card-context"><span>{{ node.provider_name || '未知 Provider' }}</span><b>{{ String(node.type || '—').toUpperCase() }}</b><div v-if="layout === 'list' && capabilityList(node).length" class="node-capabilities" aria-label="节点能力"><span v-for="item in capabilityList(node)" :key="item">{{ item }}</span></div></div>
          </div>
        </header>
        <span class="pill node-status" :class="node.alive ? 'ok' : 'warn'">{{ node.alive ? '可用' : '需检查' }}</span>

        <div class="node-card-metrics">
          <div :title="[latestHealth(node.history).time, previousDelays(node) && `历史 ${previousDelays(node)}`].filter(Boolean).join(' · ')"><span>最近测速</span><b>{{ latestHealth(node.history).label }}</b><small v-if="latestHealth(node.history).time">{{ latestHealth(node.history).time }}</small><small v-if="previousDelays(node)">历史 {{ previousDelays(node) }}</small></div>
          <div :title="stats(node).count ? `↑ ${formatBytes(stats(node).upload)} · ↓ ${formatBytes(stats(node).download)}` : '暂无活跃连接'"><span>实时连接</span><b>{{ stats(node).count }} 个</b><small v-if="stats(node).count">↑ {{ formatBytes(stats(node).upload) }} · ↓ {{ formatBytes(stats(node).download) }}</small><small v-else>暂无活跃连接</small></div>
        </div>

        <div v-if="layout !== 'list' && capabilityList(node).length" class="node-capabilities" aria-label="节点能力"><span v-for="item in capabilityList(node)" :key="item">{{ item }}</span></div>
        <div v-if="chainLabel(node)" class="node-chain"><span>代理链</span><code>{{ chainLabel(node) }}</code></div>

        <div v-if="results[node.id]" class="node-diagnostic" aria-live="polite">
          <div v-for="protocol in ['tcp', 'udp']" :key="protocol" class="node-diagnostic-result" :class="results[node.id][protocol]?.success ? 'ok' : 'bad'">
            <div><b>{{ protocol.toUpperCase() }} {{ results[node.id][protocol]?.success ? '通过' : '失败' }}</b><span>{{ results[node.id][protocol]?.latency_ms ?? '—' }} ms</span></div>
            <p v-if="results[node.id][protocol]?.detail">{{ results[node.id][protocol].detail }}</p>
          </div>
        </div>
        <div v-if="diagnosticError(node)" class="node-error" aria-live="polite">{{ diagnosticError(node) }}</div>

        <footer class="node-card-actions">
          <button class="button ghost compact" type="button" :disabled="busy.has(node.id)" @click="runAction(node, 'health')">Mihomo 测速</button>
          <button class="button ghost compact" type="button" :disabled="busy.has(node.id)" :aria-busy="busy.has(node.id)" @click="runAction(node, 'diagnose')">端到端诊断</button>
          <div class="node-menu" @click.stop @keydown.esc.prevent.stop="menuOpen = ''">
            <button class="button ghost compact node-more" type="button" aria-haspopup="menu" :aria-expanded="menuOpen === node.id" @click.stop="toggleMenu(node.id)">更多</button>
            <div v-if="menuOpen === node.id" class="node-menu-panel" role="menu"><button class="button ghost" type="button" role="menuitem" :disabled="busy.has(node.id)" @click="runAction(node, 'copy')">复制 Surge 节点行</button></div>
          </div>
        </footer>
      </article>
    </div>
    <div v-else class="card empty-state"><div class="empty-state-icon">0</div><b>没有符合筛选的节点</b><span>调整条件，或清除全部筛选后重试。</span><div class="actions"><button class="button" type="button" @click="clearFilters">清除筛选</button></div></div>
  </template>

  <div v-else class="card empty-state"><div class="empty-state-icon">{{ providers.length ? '…' : '+' }}</div><b>{{ providers.length ? '暂时没有可投影节点' : '请先添加 Provider' }}</b><span>{{ providers.length ? '检查 Provider 是否已成功更新，以及投影名称筛选是否排除了全部节点。' : 'Provider 成功加载后，节点会自动出现在这里。' }}</span><div class="actions"><button class="button primary" type="button" @click="router.push({ name: 'providers' })">{{ providers.length ? '检查 Provider' : '添加 Provider' }}</button></div></div>
</template>

<style scoped>
.node-view-switch{margin-left:auto;display:flex;gap:3px;padding:3px;border:1px solid var(--line);border-radius:9px;background:var(--bg)}
.node-view-switch button{display:flex;align-items:center;gap:6px;padding:6px 10px;border:1px solid transparent;border-radius:6px;background:transparent;color:var(--muted);font-size:12px;white-space:nowrap}
.node-view-switch button:hover{color:var(--text)}
.node-view-switch button[aria-pressed="true"]{background:var(--panel2);border-color:#424b58;color:var(--accent)}
.node-view-switch svg{width:14px;height:14px;fill:none;stroke:currentColor;stroke-width:1.4}
.node-card-title{padding-right:68px}
.node-status{position:absolute;top:16px;right:16px;white-space:nowrap}
.node-card-list{grid-template-columns:minmax(0,1fr);gap:10px}
.node-card-list .node-card{display:grid;grid-template-columns:minmax(0,1.15fr) max-content minmax(180px,1fr) auto;grid-template-areas:"identity status metrics actions" "context status metrics actions";align-items:center;column-gap:18px;row-gap:6px;padding:12px 16px;background:var(--panel);box-shadow:none}
.node-card-list .node-card-header,.node-card-list .node-card-identity{display:contents}
.node-card-list .node-card-title{grid-area:identity;gap:8px;padding-right:0}
.node-card-list .node-status{position:static;grid-area:status;align-self:center;justify-self:end}
.node-card-list .node-card-title h3{font-size:14px}
.node-card-list .node-card-context{grid-area:context;margin:0;gap:8px;min-width:0}
.node-card-list .node-card-metrics{grid-area:metrics;display:grid;grid-template-columns:1fr 1fr;gap:16px;margin:0}
.node-card-list .node-card-metrics>div{display:grid;gap:3px;border:0;border-left:1px solid var(--line);border-radius:0;background:none;padding:0 0 0 16px}
.node-card-list .node-card-metrics span{white-space:nowrap}
.node-card-list .node-card-metrics b{font-size:14px;white-space:nowrap}
.node-card-list .node-card-metrics small{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
.node-card-list .node-capabilities{flex:0 0 auto;flex-wrap:nowrap;margin:0;gap:4px}
.node-card-list .node-capabilities span{padding:1px 6px;font-size:9px}
.node-card-list .node-card-actions{grid-area:actions;margin:0;padding:0;justify-content:flex-end}
.node-card-list .node-chain,.node-card-list .node-diagnostic,.node-card-list .node-error{grid-column:1/-1}
@media(max-width:1180px) and (min-width:621px){
  .node-card-list .node-card{grid-template-columns:minmax(0,1fr) max-content minmax(145px,.7fr) auto;column-gap:12px}
  .node-card-list .node-card-metrics{gap:8px}
  .node-card-list .node-card-metrics>div{padding-left:8px}
  .node-card-list .node-card-actions{gap:5px}
  .node-card-list .node-card-actions .button{padding:6px 8px;font-size:11px;white-space:nowrap}
  .node-card-list .node-more{min-width:42px}
}
@media(max-width:800px){
  .node-card-list .node-card{grid-template-columns:minmax(0,1fr) max-content;grid-template-areas:"identity status" "context context" "metrics metrics" "actions actions";gap:12px;align-items:center;padding:14px}
  .node-card-list .node-card-metrics{gap:16px}
  .node-card-list .node-card-context{flex-wrap:wrap}
  .node-card-list .node-capabilities{flex-wrap:wrap}
  .node-card-list .node-card-actions{gap:7px}
}
</style>
