<script setup>
import { computed, ref } from 'vue'
import { severityTagType } from '@/lib/status'

const props = defineProps({
  findings: { type: Array, default: () => [] },
  compact: { type: Boolean, default: false },
})

const severity = ref('')
const source = ref('')

const sources = computed(() => [...new Set(props.findings.map((f) => f.source).filter(Boolean))])

const rows = computed(() =>
  props.findings.filter(
    (f) =>
      (!severity.value || (f.severity ?? '') === severity.value) &&
      (!source.value || f.source === source.value),
  ),
)

const SEVERITIES = ['critical', 'high', 'medium', 'low', 'info']
</script>

<template>
  <el-empty v-if="!findings.length" description="No findings" :image-size="70" />

  <template v-else>
    <div v-if="!compact" class="filters">
      <el-select v-model="severity" placeholder="All severities" clearable size="small" style="width: 160px">
        <el-option v-for="s in SEVERITIES" :key="s" :label="s" :value="s" />
      </el-select>
      <el-select v-model="source" placeholder="All sources" clearable size="small" style="width: 180px">
        <el-option v-for="s in sources" :key="s" :label="s" :value="s" />
      </el-select>
      <span class="count">{{ rows.length }} of {{ findings.length }}</span>
    </div>

    <el-table :data="rows" size="small" style="width: 100%">
      <el-table-column label="Severity" width="100">
        <template #default="{ row }">
          <el-tag :type="severityTagType(row.severity)" size="small" disable-transitions>
            {{ row.severity || 'info' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="source" label="Source" width="140" />
      <el-table-column label="Where" width="220">
        <template #default="{ row }">
          <span v-if="row.file" class="mono">{{ row.file }}<template v-if="row.line">:{{ row.line }}</template></span>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
      <el-table-column prop="title" label="Finding" min-width="260" show-overflow-tooltip />
      <el-table-column prop="targetRole" label="Owner" width="170">
        <template #default="{ row }">
          <span v-if="row.targetRole">{{ row.targetRole }}</span>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
    </el-table>
  </template>
</template>

<style scoped>
.filters { display: flex; gap: 10px; align-items: center; margin-bottom: 10px; }
.count { font-size: 12px; color: var(--el-text-color-secondary); }
.mono { font-family: var(--el-font-family-mono, monospace); font-size: 12px; }
.dim { color: var(--el-text-color-secondary); }
</style>
