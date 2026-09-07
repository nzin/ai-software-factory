<script setup>
import FindingsTable from './FindingsTable.vue'
import { fmtDuration, fmtTime, statusTagType } from '@/lib/status'

defineProps({
  task: { type: Object, required: true },
  index: { type: Number, default: 0 },
})
</script>

<template>
  <el-card class="stage" shadow="never">
    <div class="head">
      <span class="n">{{ index + 1 }}</span>
      <span class="role">{{ task.role }}</span>

      <el-tag v-if="task.attempt" type="warning" size="small" effect="plain">
        fix pass #{{ task.attempt }}
      </el-tag>
      <el-tag
        v-if="task.verdict"
        :type="task.verdict === 'approve' ? 'success' : 'warning'"
        size="small"
        disable-transitions
      >
        {{ task.verdict.replace('_', ' ') }}
      </el-tag>

      <span class="spacer" />
      <span v-if="task.durationMs" class="dim">{{ fmtDuration(task.durationMs) }}</span>
      <span v-if="task.startedAt" class="dim">{{ fmtTime(task.startedAt) }}</span>
      <el-tag :type="statusTagType(task.state)" size="small" effect="plain">{{ task.state }}</el-tag>
    </div>

    <p v-if="task.summary" class="summary">{{ task.summary }}</p>

    <div v-if="task.commitSha" class="commit">
      <span class="label">commit</span>
      <code>{{ task.commitSha.slice(0, 10) }}</code>
    </div>

    <el-collapse v-if="task.filesWritten?.length || task.findings?.length" class="more">
      <el-collapse-item
        v-if="task.filesWritten?.length"
        :title="`${task.filesWritten.length} files written`"
        name="files"
      >
        <ul class="files">
          <li v-for="f in task.filesWritten" :key="f"><code>{{ f }}</code></li>
        </ul>
      </el-collapse-item>

      <el-collapse-item
        v-if="task.findings?.length"
        :title="`${task.findings.length} findings raised`"
        name="findings"
      >
        <FindingsTable :findings="task.findings" compact />
      </el-collapse-item>
    </el-collapse>
  </el-card>
</template>

<style scoped>
.stage { margin-bottom: 12px; }
.head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.n {
  width: 22px; height: 22px; border-radius: 50%;
  background: var(--el-color-primary-light-8);
  color: var(--el-color-primary);
  display: inline-flex; align-items: center; justify-content: center;
  font-size: 12px; font-weight: 600;
}
.role { font-weight: 600; }
.spacer { flex: 1; }
.dim { color: var(--el-text-color-secondary); font-size: 12px; }
.summary { margin: 10px 0 0; font-size: 14px; line-height: 1.55; white-space: pre-wrap; }
.commit { margin-top: 8px; font-size: 12px; }
.commit .label { color: var(--el-text-color-secondary); margin-right: 6px; }
.more { margin-top: 8px; }
.files { margin: 0; padding-left: 18px; font-size: 12px; line-height: 1.7; }
</style>
