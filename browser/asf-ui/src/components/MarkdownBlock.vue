<script setup>
import { computed } from 'vue'
import MarkdownIt from 'markdown-it'
import DOMPurify from 'dompurify'

// The plan is model-authored text, so it is rendered as markdown but always
// sanitized before it reaches the DOM.
const md = new MarkdownIt({ html: false, linkify: true, breaks: false })

const props = defineProps({ source: { type: String, default: '' } })

const html = computed(() =>
  props.source ? DOMPurify.sanitize(md.render(props.source)) : '',
)
</script>

<template>
  <div v-if="html" class="markdown" v-html="html" />
  <el-empty v-else description="Nothing here yet" :image-size="60" />
</template>

<style scoped>
.markdown {
  line-height: 1.6;
  font-size: 14px;
  word-break: break-word;
}
.markdown :deep(h1),
.markdown :deep(h2),
.markdown :deep(h3) {
  margin: 1.2em 0 0.5em;
  line-height: 1.3;
}
.markdown :deep(h1) { font-size: 1.4em; }
.markdown :deep(h2) { font-size: 1.2em; }
.markdown :deep(h3) { font-size: 1.05em; }
.markdown :deep(pre) {
  background: var(--el-fill-color-light);
  padding: 12px;
  border-radius: 4px;
  overflow-x: auto;
}
.markdown :deep(code) {
  background: var(--el-fill-color-light);
  padding: 1px 4px;
  border-radius: 3px;
  font-size: 0.92em;
}
.markdown :deep(pre code) { background: none; padding: 0; }
.markdown :deep(table) { border-collapse: collapse; }
.markdown :deep(th),
.markdown :deep(td) {
  border: 1px solid var(--el-border-color);
  padding: 4px 8px;
}
</style>
