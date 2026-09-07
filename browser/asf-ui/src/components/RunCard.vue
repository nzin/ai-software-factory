<script setup>
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import StatusTag from './StatusTag.vue'
import { fmtRelative, statusLabel } from '@/lib/status'

const props = defineProps({ run: { type: Object, required: true } })
const router = useRouter()

const title = computed(() => props.run.title || props.run.id?.slice(0, 8) || 'untitled')
const open = () => router.push(`/runs/${props.run.id}`)
</script>

<template>
  <el-card class="run-card" shadow="hover" @click="open">
    <div class="head">
      <span class="title" :title="title">{{ title }}</span>
      <StatusTag :status="run.status" />
    </div>

    <div class="meta">
      <span v-if="run.stage" class="stage">
        <el-icon><Loading /></el-icon> {{ statusLabel(run.stage) }}
      </span>
      <span v-if="run.workBranch" class="branch" :title="run.workBranch">
        {{ run.workBranch }}
      </span>
    </div>

    <div class="foot">
      <el-tooltip content="Iterations left in the run's budget" placement="bottom">
        <span class="budget">{{ run.iterationsRemaining ?? 0 }} left</span>
      </el-tooltip>
      <span class="spacer" />
      <a
        v-if="run.prURL"
        class="pr"
        :href="run.prURL"
        target="_blank"
        rel="noreferrer noopener"
        @click.stop
      >PR</a>
      <span class="age">{{ fmtRelative(run.updatedAt) }}</span>
    </div>
  </el-card>
</template>

<style scoped>
.run-card {
  cursor: pointer;
  margin-bottom: 10px;
}
.run-card :deep(.el-card__body) { padding: 12px; }
.head {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  justify-content: space-between;
}
.title {
  font-weight: 600;
  font-size: 14px;
  line-height: 1.35;
  overflow: hidden;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
}
.meta {
  display: flex;
  gap: 10px;
  align-items: center;
  margin-top: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.stage { display: inline-flex; align-items: center; gap: 4px; }
.branch {
  font-family: var(--el-font-family-mono, monospace);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.foot {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.spacer { flex: 1; }
.pr { color: var(--el-color-primary); text-decoration: none; }
</style>
