import axios from 'axios'
import { ElMessage } from 'element-plus'

// Same-origin in production (the coordinator serves this bundle); the Vite dev
// server proxies /v1 and /healthz to the coordinator instead.
export const client = axios.create({
  baseURL: import.meta.env.VITE_API_BASE || '/',
  headers: { 'Content-Type': 'application/json' },
})

/** apiError pulls the {message} body the coordinator returns on errors. */
export function apiError(err) {
  return err?.response?.data?.message || err?.message || 'request failed'
}

/** notifyError surfaces a failed call without swallowing it. */
export function notifyError(err, prefix = 'Request failed') {
  ElMessage.error(`${prefix}: ${apiError(err)}`)
  return err
}
