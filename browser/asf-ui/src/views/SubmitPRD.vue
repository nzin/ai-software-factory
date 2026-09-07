<script setup>
import { computed, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { submitPRD } from '@/api/runs'
import { notifyError } from '@/api/client'

const router = useRouter()
const busy = ref(false)
const advanced = ref(false)

const form = reactive({
  title: '',
  markdown: '',
  repoURL: '',
  baseBranch: 'main',
  iterationBudget: 0,
  deadlineSeconds: 0,
})

// The coordinator parses a markdown PRD itself (first `# heading` is the title),
// so a title field is only a convenience — prepend it when the body has none.
const body = computed(() => {
  const md = form.markdown.trim()
  const t = form.title.trim()
  if (!t) return md
  return md.startsWith('#') ? md : `# ${t}\n\n${md}`
})

const repoHint = computed(() => {
  const v = form.repoURL.trim()
  if (!v) return 'Empty: the factory creates a brand-new local repo in its workspace and the plan scaffolds it.'
  if (/^(https?|ssh|git):\/\//.test(v) || /^[^/]+@[^/]+:/.test(v)) {
    return 'Remote: cloned, the work branch is pushed, and a GitHub PR is opened when GITHUB_TOKEN is set.'
  }
  return 'Local: a `git worktree` is added to that repo, so the asf/run-… branch lands there ready to merge.'
})

async function submit() {
  if (!body.value) {
    ElMessage.warning('Write a PRD first')
    return
  }
  busy.value = true
  try {
    const payload = {
      markdown: body.value,
      repoURL: form.repoURL.trim(),
      baseBranch: form.baseBranch.trim() || 'main',
    }
    if (form.iterationBudget > 0) payload.iterationBudget = form.iterationBudget
    if (form.deadlineSeconds > 0) payload.deadlineSeconds = form.deadlineSeconds

    const run = await submitPRD(payload)
    ElMessage.success('Run started')
    router.push(`/runs/${run.id}`)
  } catch (err) {
    notifyError(err, 'Could not submit')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="page">
    <h2>Submit a PRD</h2>
    <p class="lead">
      The planner turns this into a task list. Non-trivial plans that touch code or deployment
      config pause for your approval before any developer runs.
    </p>

    <el-form label-position="top" class="form" @submit.prevent="submit">
      <el-form-item label="Title">
        <el-input v-model="form.title" placeholder="Tic-tac-toe REST API" />
      </el-form-item>

      <el-form-item label="PRD (markdown)">
        <el-input
          v-model="form.markdown"
          type="textarea"
          :rows="18"
          placeholder="## Background&#10;&#10;## Requirements&#10;&#10;## Acceptance criteria"
        />
      </el-form-item>

      <el-form-item label="Target repository">
        <el-input v-model="form.repoURL" placeholder="(empty) · /tmp/myrepo · https://github.com/me/repo" />
        <p class="hint">{{ repoHint }}</p>
      </el-form-item>

      <el-form-item label="Base branch">
        <el-input v-model="form.baseBranch" style="width: 240px" />
      </el-form-item>

      <el-collapse v-model="advanced" class="advanced">
        <el-collapse-item title="Budget overrides" name="1">
          <el-form-item label="Iteration budget (0 = server default)">
            <el-input-number v-model="form.iterationBudget" :min="0" :max="500" />
          </el-form-item>
          <el-form-item label="Deadline in seconds (0 = server default)">
            <el-input-number v-model="form.deadlineSeconds" :min="0" :step="600" />
          </el-form-item>
        </el-collapse-item>
      </el-collapse>

      <div class="actions">
        <el-button type="primary" :loading="busy" @click="submit">Start the run</el-button>
        <el-button :disabled="busy" @click="$router.push('/')">Cancel</el-button>
      </div>
    </el-form>
  </div>
</template>

<style scoped>
.page { max-width: 900px; }
h2 { font-size: 20px; margin: 0 0 6px; }
.lead { color: var(--el-text-color-secondary); font-size: 14px; margin: 0 0 20px; line-height: 1.6; }
.hint { font-size: 12px; color: var(--el-text-color-secondary); margin: 6px 0 0; line-height: 1.5; }
.advanced { margin-bottom: 18px; }
.actions { display: flex; gap: 10px; }
</style>
