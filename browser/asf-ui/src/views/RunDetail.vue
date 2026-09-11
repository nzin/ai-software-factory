<script setup>
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { approveRun, deleteRun, getRun, rejectRun, resumeRun, reviewRun } from '@/api/runs'
import { apiError, notifyError } from '@/api/client'
import { usePolling } from '@/composables/usePolling'
import StatusTag from '@/components/StatusTag.vue'
import EventTimeline from '@/components/EventTimeline.vue'
import RevisionList from '@/components/RevisionList.vue'
import StageCard from '@/components/StageCard.vue'
import FindingsTable from '@/components/FindingsTable.vue'
import MarkdownBlock from '@/components/MarkdownBlock.vue'
import { DEVELOPER_ROLES, columnFor, fmtRelative, isTerminal, statusLabel } from '@/lib/status'

const route = useRoute()
const router = useRouter()
const id = route.params.id

const { data, error, loading, refresh } = usePolling(() => getRun(id), {
  intervalMs: 3000,
  stopWhen: (r) => isTerminal(r?.status),
})

const run = computed(() => data.value ?? {})
const tab = ref('timeline')
const busy = ref(false)

const canApprove = computed(() => run.value.status === 'awaiting_approval')
const canResume = computed(() => ['needs_human_review', 'failed'].includes(run.value.status))
const canReview = computed(() => ['pr_ready', 'pr_open'].includes(run.value.status))
const isFailed = computed(() => run.value.status === 'failed')
const canDelete = computed(() => columnFor(run.value.status) === 'closed')

// Show the revisions tab once there is a PR or more than one branch-ready cycle.
const showRevisions = computed(() => {
  if (run.value.prURL) return true
  return (run.value.events || []).filter(
    (e) => e.kind === 'finished' && ['pr_ready', 'pr_open'].includes(e.status),
  ).length > 0
})

// --- dialogs ---
const reject = reactive({ open: false, feedback: '', abandon: false })
const resume = reactive({
  open: false,
  iterationBudget: 20,
  deadlineSeconds: 3600,
  comment: '',
  targetRole: '',
})
const review = reactive({ open: false, comments: [{ note: '', targetRole: '' }] })

async function act(fn, okMsg) {
  busy.value = true
  try {
    await fn()
    ElMessage.success(okMsg)
    await refresh()
    return true
  } catch (err) {
    notifyError(err)
    return false
  } finally {
    busy.value = false
  }
}

const doApprove = () => act(() => approveRun(id), 'Plan approved — the run is moving on')
const openRevise = () => {
  reject.abandon = false
  reject.open = true
}
const openReject = () => {
  reject.abandon = true
  reject.open = true
}
const doReject = async () => {
  if (!reject.abandon && !reject.feedback.trim()) {
    ElMessage.warning('Add a comment for the planner')
    return
  }
  const ok = await act(
    () => rejectRun(id, { feedback: reject.feedback.trim(), abandon: reject.abandon }),
    reject.abandon ? 'Run rejected' : 'Plan sent back for revision',
  )
  if (ok) {
    reject.open = false
    reject.feedback = ''
  }
}
const doResume = async () => {
  const body = {
    iterationBudget: resume.iterationBudget,
    deadlineSeconds: resume.deadlineSeconds,
  }
  if (resume.comment.trim()) body.comment = resume.comment.trim()
  if (resume.targetRole) body.targetRole = resume.targetRole
  const ok = await act(() => resumeRun(id, body), isFailed.value ? 'Run restarted' : 'Run resumed')
  if (ok) {
    resume.open = false
    resume.comment = ''
    resume.targetRole = ''
  }
}
const doAbandon = () => act(() => resumeRun(id, { abandon: true }), 'Run abandoned')
const doDelete = async () => {
  try {
    await ElMessageBox.confirm(
      'Delete this run and its workspace? This cannot be undone.',
      'Delete run',
      { type: 'warning', confirmButtonText: 'Delete', confirmButtonClass: 'el-button--danger' },
    )
  } catch {
    return // dismissed
  }
  busy.value = true
  try {
    await deleteRun(id)
    ElMessage.success('Run deleted')
    router.push('/')
  } catch (err) {
    notifyError(err)
    busy.value = false
  }
}
const doAcceptAsIs = () =>
  act(() => resumeRun(id, { accept: true }), 'Accepted as-is — pushing the branch')
const doAccept = () =>
  act(() => reviewRun(id, { decision: 'accept' }), 'Accepted — pushing the branch')
const doRequestChanges = async () => {
  const comments = review.comments.filter((c) => c.note.trim())
  if (!comments.length) {
    ElMessage.warning('Add at least one comment')
    return
  }
  const ok = await act(
    () => reviewRun(id, { decision: 'request_changes', comments }),
    'Sent back to the factory',
  )
  if (ok) {
    review.open = false
    review.comments = [{ note: '', targetRole: '' }]
  }
}

const addComment = () => review.comments.push({ note: '', targetRole: '' })
const removeComment = (i) => review.comments.splice(i, 1)
</script>

<template>
  <div class="page">
    <el-page-header @back="$router.push('/')">
      <template #content>
        <span class="title">{{ run.prd?.title || id }}</span>
      </template>
    </el-page-header>

    <el-alert
      v-if="error"
      type="error"
      :closable="false"
      :title="`Could not load this run: ${apiError(error)}`"
      class="alert"
    />
    <el-skeleton v-if="loading" :rows="8" animated />

    <template v-else>
      <el-card class="header" shadow="never">
        <el-descriptions :column="3" size="small" border>
          <el-descriptions-item label="Status">
            <StatusTag :status="run.status" />
          </el-descriptions-item>
          <el-descriptions-item label="Stage">
            {{ statusLabel(run.stage || run.lastStage) }}
          </el-descriptions-item>
          <el-descriptions-item label="Budget">
            {{ run.iterationsRemaining ?? 0 }} iterations left
          </el-descriptions-item>

          <el-descriptions-item label="Repository">
            <code>{{ run.repoURL || '(new local repo)' }}</code>
            <el-tag v-if="run.repoKind" size="small" effect="plain" class="ml">{{ run.repoKind }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="Branch">
            <code>{{ run.workBranch || '—' }}</code>
            <span class="dim"> from {{ run.baseBranch }}</span>
          </el-descriptions-item>
          <el-descriptions-item label="Pull request">
            <a v-if="run.prURL" :href="run.prURL" target="_blank" rel="noreferrer noopener">{{ run.prURL }}</a>
            <span v-else class="dim">—</span>
          </el-descriptions-item>

          <el-descriptions-item label="Updated">{{ fmtRelative(run.updatedAt) }}</el-descriptions-item>
          <el-descriptions-item label="Workspace" :span="2">
            <code class="small">{{ run.workspaceDir || '—' }}</code>
          </el-descriptions-item>
        </el-descriptions>

        <el-alert v-if="run.reason" class="reason" type="info" :closable="false" :title="run.reason" />

        <el-alert
          v-if="canApprove && run.approval?.reason"
          class="reason"
          type="warning"
          :closable="false"
          :title="`Approval required: ${run.approval.reason}`"
        />

        <div v-if="canApprove || canResume || canReview || canDelete" class="actions">
          <template v-if="canApprove">
            <el-button type="primary" :loading="busy" @click="doApprove">Approve plan</el-button>
            <el-button :disabled="busy" @click="openRevise">Request changes…</el-button>
            <el-button type="danger" plain :disabled="busy" @click="openReject">Reject</el-button>
          </template>
          <template v-if="canResume">
            <el-button type="primary" :disabled="busy" @click="resume.open = true">
              {{ isFailed ? 'Restart…' : 'Resume…' }}
            </el-button>
            <el-button type="success" plain :loading="busy" @click="doAcceptAsIs">Accept as-is</el-button>
            <el-button type="danger" plain :loading="busy" @click="doAbandon">Abandon</el-button>
          </template>
          <template v-if="canReview">
            <el-button type="success" :loading="busy" @click="doAccept">Accept changes</el-button>
            <el-button type="warning" plain :disabled="busy" @click="review.open = true">
              Request changes…
            </el-button>
          </template>
          <el-button v-if="canDelete" type="danger" plain :loading="busy" @click="doDelete">
            Delete
          </el-button>
        </div>
      </el-card>

      <el-tabs v-model="tab" class="tabs">
        <el-tab-pane label="Timeline" name="timeline">
          <EventTimeline :events="run.events || []" />
        </el-tab-pane>

        <el-tab-pane v-if="showRevisions" label="Revisions" name="revisions">
          <RevisionList :events="run.events || []" />
        </el-tab-pane>

        <el-tab-pane :label="`Steps (${(run.tasks || []).length})`" name="steps">
          <el-empty v-if="!(run.tasks || []).length" description="No steps yet" :image-size="70" />
          <StageCard v-for="(t, i) in run.tasks || []" :key="i" :task="t" :index="i" />
        </el-tab-pane>

        <el-tab-pane label="Plan" name="plan">
          <el-descriptions v-if="run.approval" :column="1" size="small" border class="approval">
            <el-descriptions-item label="Human approval">
              <el-tag :type="run.approval.required ? 'warning' : 'success'" size="small">
                {{ run.approval.required ? 'required' : 'not required' }}
              </el-tag>
              <span v-if="run.approval.reason" class="ml">{{ run.approval.reason }}</span>
            </el-descriptions-item>
          </el-descriptions>

          <el-table v-if="(run.planTasks || []).length" :data="run.planTasks" size="small" class="plantasks">
            <el-table-column prop="id" label="#" width="70" />
            <el-table-column prop="role" label="Role" width="180" />
            <el-table-column prop="title" label="Task" min-width="240" />
            <el-table-column prop="details" label="Details" min-width="280" show-overflow-tooltip />
          </el-table>

          <MarkdownBlock :source="run.plan" />
        </el-tab-pane>

        <el-tab-pane :label="`Findings (${(run.findings || []).length})`" name="findings">
          <FindingsTable :findings="run.findings || []" />
        </el-tab-pane>

        <el-tab-pane label="Raw" name="raw">
          <pre class="raw">{{ JSON.stringify(run, null, 2) }}</pre>
        </el-tab-pane>
      </el-tabs>
    </template>

    <!-- Request changes to the plan / reject the run -->
    <el-dialog
      v-model="reject.open"
      :title="reject.abandon ? 'Reject and end this run' : 'Request changes to the plan'"
      width="520px"
    >
      <el-form label-position="top">
        <el-form-item :label="reject.abandon ? 'Reason (optional)' : 'What should the planner change?'">
          <el-input
            v-model="reject.feedback"
            type="textarea"
            :rows="4"
            :placeholder="reject.abandon
              ? 'Recorded in the run log.'
              : 'The planner revises its plan with these comments.'"
          />
        </el-form-item>
        <p v-if="!reject.abandon" class="hint">
          The run goes back to the planner, which revises the current plan and pauses again for
          your approval.
        </p>
      </el-form>
      <template #footer>
        <el-button @click="reject.open = false">Cancel</el-button>
        <el-button
          :type="reject.abandon ? 'danger' : 'primary'"
          :loading="busy"
          @click="doReject"
        >
          {{ reject.abandon ? 'Reject run' : 'Send for revision' }}
        </el-button>
      </template>
    </el-dialog>

    <!-- Resume / restart a parked or failed run -->
    <el-dialog v-model="resume.open" :title="isFailed ? 'Restart this run' : 'Resume this run'" width="520px">
      <el-form label-position="top">
        <el-form-item label="Comment for the agent (optional)">
          <el-input
            v-model="resume.comment"
            type="textarea"
            :rows="3"
            placeholder="What should the agent do differently? Left blank, the run just retries."
          />
        </el-form-item>
        <el-form-item label="Route the comment to">
          <el-select v-model="resume.targetRole" placeholder="Auto-route" clearable style="width: 240px">
            <el-option v-for="r in DEVELOPER_ROLES" :key="r" :label="r" :value="r" />
          </el-select>
        </el-form-item>
        <el-form-item label="Extra iterations">
          <el-input-number v-model="resume.iterationBudget" :min="1" :max="500" />
        </el-form-item>
        <el-form-item label="Extra time (seconds)">
          <el-input-number v-model="resume.deadlineSeconds" :min="60" :step="600" />
        </el-form-item>
        <p class="hint">
          A comment becomes a high-severity finding routed to the responsible developer for a fix
          pass. Per-stage attempt counters are reset either way.
        </p>
      </el-form>
      <template #footer>
        <el-button @click="resume.open = false">Cancel</el-button>
        <el-button type="primary" :loading="busy" @click="doResume">
          {{ isFailed ? 'Restart' : 'Resume' }}
        </el-button>
      </template>
    </el-dialog>

    <!-- Request changes -->
    <el-dialog v-model="review.open" title="Request changes" width="640px">
      <p class="hint">
        Each comment becomes a high-severity finding and sends the run back to a developer, through
        the reviewers, and around to a new revision.
      </p>
      <div v-for="(c, i) in review.comments" :key="i" class="comment">
        <el-input v-model="c.note" type="textarea" :rows="2" placeholder="What needs to change?" />
        <div class="comment-meta">
          <el-select v-model="c.targetRole" placeholder="Auto-route" clearable size="small" style="width: 210px">
            <el-option v-for="r in DEVELOPER_ROLES" :key="r" :label="r" :value="r" />
          </el-select>
          <el-button
            v-if="review.comments.length > 1"
            size="small"
            text
            type="danger"
            @click="removeComment(i)"
          >Remove</el-button>
        </div>
      </div>
      <el-button size="small" text type="primary" @click="addComment">+ Add another comment</el-button>
      <template #footer>
        <el-button @click="review.open = false">Cancel</el-button>
        <el-button type="warning" :loading="busy" @click="doRequestChanges">Request changes</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.title { font-size: 18px; font-weight: 600; }
.alert { margin: 12px 0; }
.header { margin: 16px 0; }
.reason { margin-top: 12px; }
.actions { margin-top: 16px; display: flex; gap: 10px; }
.tabs { margin-top: 8px; }
.dim { color: var(--el-text-color-secondary); }
.ml { margin-left: 8px; }
.small { font-size: 12px; }
.approval { margin-bottom: 14px; }
.plantasks { margin-bottom: 18px; }
.hint { font-size: 12px; color: var(--el-text-color-secondary); margin: 0 0 12px; line-height: 1.6; }
.comment { margin-bottom: 12px; }
.comment-meta { display: flex; align-items: center; gap: 10px; margin-top: 6px; }
.raw {
  font-size: 12px;
  background: var(--el-fill-color-light);
  padding: 12px;
  border-radius: 4px;
  overflow: auto;
  max-height: 640px;
}
</style>
