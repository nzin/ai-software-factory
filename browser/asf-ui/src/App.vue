<script setup>
import { computed } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()

// Top-level nav highlight: /runs/:id and / both belong to "runs".
const active = computed(() => {
  if (route.path.startsWith('/agents')) return '/agents'
  if (route.path.startsWith('/submit')) return '/submit'
  return '/'
})
</script>

<template>
  <el-container class="app">
    <el-header class="header">
      <router-link to="/" class="brand">
        <span class="mark">⚙</span>
        <span>AI Software Factory</span>
      </router-link>

      <el-menu :default-active="active" mode="horizontal" router class="nav" :ellipsis="false">
        <el-menu-item index="/">Runs</el-menu-item>
        <el-menu-item index="/agents">Catalog</el-menu-item>
        <el-menu-item index="/submit">Submit PRD</el-menu-item>
      </el-menu>
    </el-header>

    <el-main class="main">
      <router-view />
    </el-main>
  </el-container>
</template>

<style scoped>
.app { min-height: 100vh; }
.header {
  display: flex;
  align-items: center;
  gap: 28px;
  border-bottom: 1px solid var(--el-border-color-light);
  background: var(--el-bg-color);
  padding: 0 20px;
}
.brand {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-weight: 700;
  font-size: 15px;
  color: var(--el-text-color-primary);
  text-decoration: none;
  white-space: nowrap;
}
.mark { color: var(--el-color-primary); font-size: 18px; }
.nav { border-bottom: none; }
.main { padding: 20px; background: var(--el-bg-color-page); }
</style>

<style>
html, body, #app { margin: 0; height: 100%; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
}
code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 0.92em; }
</style>
