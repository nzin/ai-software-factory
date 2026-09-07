import { createRouter, createWebHistory } from 'vue-router'

// History mode: the coordinator's SPA fallback (internal/coordinator/ui) serves
// index.html for these paths so a hard refresh on a deep link works.
const routes = [
  { path: '/', name: 'runs', component: () => import('@/views/RunsKanban.vue') },
  { path: '/runs/:id', name: 'run', component: () => import('@/views/RunDetail.vue') },
  { path: '/submit', name: 'submit', component: () => import('@/views/SubmitPRD.vue') },
  { path: '/agents', name: 'agents', component: () => import('@/views/AgentsList.vue') },
  { path: '/agents/:role', name: 'agent', component: () => import('@/views/AgentDetail.vue') },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

export default createRouter({
  history: createWebHistory(),
  routes,
})
