<script setup>
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { listAgents } from '@/api/agents'
import { usePolling } from '@/composables/usePolling'
import { apiError } from '@/api/client'

const router = useRouter()
const { data, error, loading, refresh } = usePolling(listAgents, { intervalMs: 15000 })

const agents = computed(() => data.value ?? [])
const open = (row) => router.push(`/agents/${row.role}`)
</script>

<template>
  <div class="page">
    <div class="bar">
      <h2>A2A catalog</h2>
      <span class="dim">{{ agents.length }} registered agents</span>
      <span class="spacer" />
      <el-button size="small" @click="refresh">Refresh</el-button>
    </div>

    <p class="lead">
      The catalog is the source of truth for the roster: each agent's A2A registration and the
      Claude model it runs on. Agents re-assert their <code>agent_prompts/&lt;role&gt;.md</code>
      on every restart, so this reflects the prompt files.
    </p>

    <el-alert
      v-if="error"
      type="error"
      :closable="false"
      :title="`Could not reach the catalog: ${apiError(error)}`"
      class="alert"
    />
    <el-skeleton v-if="loading" :rows="6" animated />

    <el-table
      v-else
      :data="agents"
      style="width: 100%"
      row-class-name="clickable"
      @row-click="open"
    >
      <el-table-column prop="role" label="Role" width="200">
        <template #default="{ row }"><code>{{ row.role }}</code></template>
      </el-table-column>
      <el-table-column prop="name" label="Name" width="180" />
      <el-table-column label="Skills" min-width="240">
        <template #default="{ row }">
          <el-tag v-for="s in row.skills || []" :key="s" size="small" effect="plain" class="skill">
            {{ s }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="Model" width="180">
        <template #default="{ row }">
          <span v-if="row.model?.model">{{ row.model.model }}</span>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
      <el-table-column label="Max tokens" width="110">
        <template #default="{ row }">{{ row.model?.maxTokens || '—' }}</template>
      </el-table-column>
      <el-table-column label="Effort" width="90">
        <template #default="{ row }">{{ row.model?.effort || '—' }}</template>
      </el-table-column>
      <el-table-column prop="concurrency" label="Conc." width="80" />
      <el-table-column label="Enabled" width="90">
        <template #default="{ row }">
          <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
            {{ row.enabled ? 'yes' : 'no' }}
          </el-tag>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<style scoped>
.bar { display: flex; align-items: center; gap: 12px; margin-bottom: 8px; }
.bar h2 { margin: 0; font-size: 20px; }
.spacer { flex: 1; }
.lead { color: var(--el-text-color-secondary); font-size: 13px; margin: 0 0 16px; line-height: 1.6; }
.dim { color: var(--el-text-color-secondary); font-size: 13px; }
.alert { margin-bottom: 12px; }
.skill { margin: 0 4px 2px 0; }
:deep(.clickable) { cursor: pointer; }
</style>
