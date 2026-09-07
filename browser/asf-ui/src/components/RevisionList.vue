<script setup>
import { computed } from 'vue'
import { revisionsFrom } from '@/lib/revisions'
import { fmtDuration, fmtTime } from '@/lib/status'

const props = defineProps({ events: { type: Array, default: () => [] } })

const revisions = computed(() => revisionsFrom(props.events))

const TRIGGER_TYPE = {
  initial: 'info',
  reviewer: 'primary',
  'build gate': 'danger',
  'human review': 'warning',
  'retry cap': 'danger',
  resumed: 'warning',
}
</script>

<template>
  <el-empty v-if="revisions.length < 2 && revisions[0]?.open" description="No revisions yet" :image-size="70" />

  <el-timeline v-else>
    <el-timeline-item
      v-for="r in revisions"
      :key="r.n"
      :timestamp="fmtTime(r.startedAt)"
      placement="top"
      :type="r.open ? 'primary' : 'success'"
      :hollow="r.open"
    >
      <div class="head">
        <span class="n">Revision {{ r.n }}</span>
        <el-tag :type="TRIGGER_TYPE[r.trigger?.kind] || 'info'" size="small" disable-transitions>
          {{ r.trigger?.kind || 'reviewer' }}
        </el-tag>
        <el-tag v-if="r.open" type="info" size="small" effect="plain">in progress</el-tag>
        <span class="spacer" />
        <span v-if="r.steps" class="dim">{{ r.steps }} steps</span>
        <span v-if="r.durationMs" class="dim">{{ fmtDuration(r.durationMs) }}</span>
        <span v-if="r.status" class="dim">{{ r.status.replace('_', ' ') }}</span>
      </div>
      <p v-if="r.trigger?.text && r.trigger.kind !== 'initial'" class="why">{{ r.trigger.text }}</p>
      <el-collapse v-if="r.trigger?.detail" class="detail">
        <el-collapse-item title="what changed" :name="r.n">
          <pre>{{ r.trigger.detail }}</pre>
        </el-collapse-item>
      </el-collapse>
    </el-timeline-item>
  </el-timeline>
</template>

<style scoped>
.head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.n { font-weight: 600; }
.spacer { flex: 1; }
.dim { color: var(--el-text-color-secondary); font-size: 12px; }
.why { margin: 6px 0 0; font-size: 14px; line-height: 1.5; }
.detail { margin-top: 4px; }
.detail :deep(.el-collapse-item__header),
.detail :deep(.el-collapse-item__wrap) { border: none; }
.detail pre {
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 12px;
  background: var(--el-fill-color-light);
  padding: 10px;
  border-radius: 4px;
  margin: 0;
}
</style>
