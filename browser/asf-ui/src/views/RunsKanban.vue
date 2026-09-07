<script setup>
import { computed } from 'vue'
import { listRuns } from '@/api/runs'
import { usePolling } from '@/composables/usePolling'
import RunCard from '@/components/RunCard.vue'
import { KANBAN_COLUMNS, groupByColumn } from '@/lib/status'
import { apiError } from '@/api/client'

const { data, error, loading, refresh } = usePolling(listRuns, { intervalMs: 4000 })

const runs = computed(() => data.value ?? [])
const columns = computed(() => groupByColumn(runs.value))
</script>

<template>
  <div class="page">
    <div class="bar">
      <h2>Runs</h2>
      <span class="dim">{{ runs.length }} total</span>
      <span class="spacer" />
      <el-button :icon="'Refresh'" size="small" @click="refresh">Refresh</el-button>
      <el-button type="primary" size="small" @click="$router.push('/submit')">
        Submit a PRD
      </el-button>
    </div>

    <el-alert
      v-if="error"
      type="error"
      :closable="false"
      :title="`Could not load runs: ${apiError(error)}`"
      class="alert"
    />

    <el-skeleton v-if="loading" :rows="6" animated />

    <div v-else class="board">
      <section v-for="col in KANBAN_COLUMNS" :key="col.key" class="col">
        <header>
          <span class="name">{{ col.title }}</span>
          <el-badge :value="columns[col.key].length" :show-zero="true" type="info" />
        </header>
        <div class="stack">
          <RunCard v-for="run in columns[col.key]" :key="run.id" :run="run" />
          <p v-if="!columns[col.key].length" class="empty">—</p>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.page { padding: 4px 0; }
.bar { display: flex; align-items: center; gap: 12px; margin-bottom: 16px; }
.bar h2 { margin: 0; font-size: 20px; }
.spacer { flex: 1; }
.dim { color: var(--el-text-color-secondary); font-size: 13px; }
.alert { margin-bottom: 12px; }

.board {
  display: grid;
  grid-template-columns: repeat(5, minmax(230px, 1fr));
  gap: 12px;
  align-items: start;
  overflow-x: auto;
}
.col {
  background: var(--el-fill-color-lighter);
  border-radius: 6px;
  padding: 10px;
  min-width: 230px;
}
.col header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
  padding-right: 6px;
}
.col .name { font-weight: 600; font-size: 13px; }
.stack { min-height: 40px; }
.empty { color: var(--el-text-color-placeholder); text-align: center; margin: 12px 0; }

@media (max-width: 1200px) {
  .board { grid-template-columns: repeat(2, minmax(230px, 1fr)); }
}
</style>
