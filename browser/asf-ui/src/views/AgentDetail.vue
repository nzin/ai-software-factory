<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { getAgent } from '@/api/agents'
import { apiError } from '@/api/client'

const MODEL_EXT_URI = 'https://ai-software-factory.dev/ext/model/v1'

const route = useRoute()
const role = route.params.role
const agent = ref(null)
const error = ref(null)
const loading = ref(true)

onMounted(async () => {
  try {
    agent.value = await getAgent(role)
  } catch (err) {
    error.value = err
  } finally {
    loading.value = false
  }
})

const info = computed(() => agent.value?.info ?? {})
const model = computed(() => agent.value?.model ?? {})
const card = computed(() => agent.value?.agentCard ?? null)

// The catalog injects the model config into the card as an A2A capability
// extension; surface it so the card is readable rather than just raw JSON.
const modelExt = computed(() =>
  (card.value?.capabilities?.extensions ?? []).find((e) => e.uri === MODEL_EXT_URI) ?? null,
)
const iface = computed(() => (card.value?.supportedInterfaces ?? [])[0] ?? null)
</script>

<template>
  <div class="page">
    <el-page-header @back="$router.push('/agents')">
      <template #content>
        <span class="title">{{ info.name || role }}</span>
        <code class="role">{{ role }}</code>
      </template>
    </el-page-header>

    <el-alert
      v-if="error"
      type="error"
      :closable="false"
      :title="`Could not load this agent: ${apiError(error)}`"
      class="alert"
    />
    <el-skeleton v-if="loading" :rows="6" animated />

    <template v-else-if="agent">
      <p v-if="info.description" class="lead">{{ info.description }}</p>

      <el-card class="block" shadow="never" header="Registration">
        <el-descriptions :column="2" size="small" border>
          <el-descriptions-item label="Base URL"><code>{{ info.baseURL }}</code></el-descriptions-item>
          <el-descriptions-item label="Transport">{{ info.transport }}</el-descriptions-item>
          <el-descriptions-item label="Concurrency">{{ info.concurrency }}</el-descriptions-item>
          <el-descriptions-item label="Enabled">
            <el-tag :type="info.enabled ? 'success' : 'info'" size="small">
              {{ info.enabled ? 'yes' : 'no' }}
            </el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="Skills" :span="2">
            <el-tag v-for="s in info.skills || []" :key="s" size="small" effect="plain" class="skill">
              {{ s }}
            </el-tag>
            <span v-if="!(info.skills || []).length" class="dim">—</span>
          </el-descriptions-item>
          <el-descriptions-item label="A2A endpoint" :span="2">
            <code v-if="iface">{{ iface.url }}</code>
            <span v-else class="dim">—</span>
          </el-descriptions-item>
        </el-descriptions>
      </el-card>

      <el-card class="block" shadow="never" header="Model">
        <el-descriptions :column="3" size="small" border>
          <el-descriptions-item label="Provider">{{ model.provider || '—' }}</el-descriptions-item>
          <el-descriptions-item label="Model"><code>{{ model.model || '—' }}</code></el-descriptions-item>
          <el-descriptions-item label="Max tokens">{{ model.maxTokens || '—' }}</el-descriptions-item>
          <el-descriptions-item label="Effort">{{ model.effort || '(default)' }}</el-descriptions-item>
          <el-descriptions-item label="Thinking">{{ model.thinking || '—' }}</el-descriptions-item>
          <el-descriptions-item label="Source">
            <span class="dim">agent_prompts/{{ role }}.md</span>
          </el-descriptions-item>
        </el-descriptions>
        <p class="hint">
          Read-only here: the prompt file is authoritative and re-asserts this configuration on
          every agent restart.
        </p>
      </el-card>

      <el-card class="block" shadow="never" header="A2A AgentCard">
        <div v-if="modelExt" class="ext">
          <span class="ext-label">model extension</span>
          <code>{{ modelExt.uri }}</code>
          <pre class="ext-params">{{ JSON.stringify(modelExt.params, null, 2) }}</pre>
        </div>
        <el-collapse>
          <el-collapse-item title="Full card JSON" name="card">
            <pre class="raw">{{ JSON.stringify(card, null, 2) }}</pre>
          </el-collapse-item>
        </el-collapse>
      </el-card>
    </template>
  </div>
</template>

<style scoped>
.title { font-size: 18px; font-weight: 600; }
.role { margin-left: 10px; font-size: 13px; color: var(--el-text-color-secondary); }
.lead { color: var(--el-text-color-secondary); font-size: 14px; line-height: 1.6; margin: 16px 0; }
.alert { margin: 12px 0; }
.block { margin-top: 16px; }
.skill { margin: 0 4px 2px 0; }
.dim { color: var(--el-text-color-secondary); }
.hint { font-size: 12px; color: var(--el-text-color-secondary); margin: 12px 0 0; line-height: 1.5; }
.ext { margin-bottom: 12px; }
.ext-label {
  font-size: 11px; text-transform: uppercase; letter-spacing: 0.4px;
  color: var(--el-color-primary); font-weight: 600; margin-right: 8px;
}
.ext-params, .raw {
  font-size: 12px;
  background: var(--el-fill-color-light);
  padding: 12px;
  border-radius: 4px;
  overflow: auto;
  margin: 8px 0 0;
}
.raw { max-height: 480px; }
</style>
