<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { getCredits } from '../../api/credits'
import { useAuthStore } from '../../store/auth'
import { creditsLabel, creditsPopoverLines } from '../../utils/credits'

const props = defineProps({
  compact: { type: Boolean, default: false },
})

const authStore = useAuthStore()
const credits = ref(null)

const label = computed(() => creditsLabel(credits.value))
const lines = computed(() => creditsPopoverLines(credits.value))

async function load() {
  if (!authStore.userId) return
  try {
    credits.value = await getCredits(authStore.userId)
  } catch {
    // The badge is informational; leave it hidden when the endpoint is
    // unavailable (for example when no model is configured yet).
    credits.value = null
  }
}

onMounted(() => {
  load()
  window.addEventListener('credits:refresh', load)
})

onBeforeUnmount(() => {
  window.removeEventListener('credits:refresh', load)
})
</script>

<template>
  <el-popover v-if="label" placement="top-start" :width="248" trigger="click">
    <template #reference>
      <button class="credits-badge" :class="{ 'credits-badge--compact': compact }" type="button">
        <span class="credits-badge__dot" aria-hidden="true" />
        <span class="credits-badge__text">{{ label }}</span>
      </button>
    </template>
    <div class="credits-popover">
      <strong>{{ label }}</strong>
      <p v-for="line in lines" :key="line">{{ line }}</p>
    </div>
  </el-popover>
</template>
