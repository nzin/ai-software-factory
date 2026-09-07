<script setup>
import { eventTagType, fmtDuration, fmtTime, statusLabel } from '@/lib/status'

defineProps({ events: { type: Array, default: () => [] } })
</script>

<template>
  <el-empty v-if="!events.length" description="No events recorded yet" :image-size="70" />

  <el-timeline v-else>
    <el-timeline-item
      v-for="e in events"
      :key="e.seq"
      :timestamp="fmtTime(e.at)"
      placement="top"
      :type="eventTagType(e.kind) === 'info' ? 'primary' : eventTagType(e.kind)"
      :hollow="e.kind === 'stage_started'"
    >
      <div class="row">
        <el-tag :type="eventTagType(e.kind)" size="small" disable-transitions>
          {{ statusLabel(e.kind) }}
        </el-tag>
        <el-tag v-if="e.actor === 'human'" type="warning" size="small" effect="plain">human</el-tag>
        <span v-if="e.durationMs" class="dim">{{ fmtDuration(e.durationMs) }}</span>
      </div>

      <div class="msg">{{ e.message }}</div>

      <el-collapse v-if="e.detail" class="detail">
        <el-collapse-item title="details" :name="e.seq">
          <pre>{{ e.detail }}</pre>
        </el-collapse-item>
      </el-collapse>
    </el-timeline-item>
  </el-timeline>
</template>

<style scoped>
.row { display: flex; align-items: center; gap: 8px; }
.msg { margin-top: 4px; font-size: 14px; line-height: 1.5; }
.dim { color: var(--el-text-color-secondary); font-size: 12px; }
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
  max-height: 320px;
  overflow: auto;
}
</style>
