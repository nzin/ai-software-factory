import { onMounted, onUnmounted, ref } from 'vue'

/**
 * usePolling calls `fetcher` immediately and then every `intervalMs` until the
 * component unmounts or `stopWhen(data)` returns true (e.g. a run reached a
 * terminal status — no point polling a finished run).
 *
 * Only the first load sets `loading`, so the view does not flash a spinner on
 * every tick.
 */
export function usePolling(fetcher, { intervalMs = 4000, stopWhen = () => false } = {}) {
  const data = ref(null)
  const error = ref(null)
  const loading = ref(true)
  let timer = null
  let cancelled = false
  let settled = false // stopWhen() said we are done

  function clear() {
    if (timer) clearInterval(timer)
    timer = null
  }

  function schedule() {
    if (cancelled || settled || timer) return
    timer = setInterval(tick, intervalMs)
  }

  async function tick() {
    try {
      const next = await fetcher()
      if (cancelled) return
      data.value = next
      error.value = null
      if (stopWhen(next)) {
        settled = true
        clear()
      }
    } catch (err) {
      if (!cancelled) error.value = err
    } finally {
      if (!cancelled) loading.value = false
    }
  }

  /** refresh forces a tick now and resumes polling if an action un-settled it. */
  async function refresh() {
    settled = false
    await tick()
    schedule()
  }

  onMounted(async () => {
    await tick()
    schedule()
  })

  onUnmounted(() => {
    cancelled = true
    clear()
  })

  return { data, error, loading, refresh }
}
