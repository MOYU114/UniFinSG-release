<script setup>
import { computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { DataAnalysis, List, ChatDotRound, Setting, ArrowRight } from '@element-plus/icons-vue'
import { useAuthStore } from '../../store/auth'
import { participantCode } from '../../utils/evaluation'
import CreditsBadge from './CreditsBadge.vue'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()

const participant = participantCode()
const accountLabel = computed(() => authStore.user?.email || 'My account')
const initials = computed(() => accountLabel.value.slice(0, 2).toUpperCase())

const navItems = [
  { name: 'chat', label: 'Chat', icon: ChatDotRound },
  { name: 'dashboard', label: 'Dashboard', icon: DataAnalysis },
  { name: 'transactions', label: 'Transactions', icon: List },
  { name: 'settings', label: 'Settings', icon: Setting },
]

function isActive(name) {
  if (name === 'chat') return route.name === 'chat' || route.name === 'chat-session'
  return route.name === name
}

watch(() => authStore.isAuthenticated, (authenticated) => {
  if (!authenticated) {
    router.replace({ name: 'signin', query: { redirect: route.fullPath } })
  }
})
</script>

<template>
  <div class="product-shell">
    <aside class="side-navigation">
      <router-link to="/chat" class="brand-lockup" aria-label="UniFinSG chat">
        <span class="brand-mark">U</span>
        <span>
          <strong>UniFinSG</strong>
          <small>Spend with clarity</small>
        </span>
      </router-link>

      <nav class="primary-nav" aria-label="Primary navigation">
        <router-link
          v-for="item in navItems"
          :key="item.name"
          :to="{ name: item.name }"
          class="nav-link"
          :class="{ active: isActive(item.name) }"
        >
          <el-icon><component :is="item.icon" /></el-icon>
          <span>{{ item.label }}</span>
        </router-link>
      </nav>

      <div class="side-navigation__footer">
        <CreditsBadge class="side-navigation__credits" />
        <div v-if="participant" class="participant-chip">Test mode · {{ participant }}</div>
        <button class="account-link" type="button" @click="router.push({ name: 'settings' })">
          <span class="account-avatar">{{ initials }}</span>
          <span class="account-copy">
            <strong>{{ accountLabel }}</strong>
            <small>Settings</small>
          </span>
          <el-icon><ArrowRight /></el-icon>
        </button>
      </div>
    </aside>

    <main class="main-content">
      <header class="mobile-header">
        <router-link to="/chat" class="mobile-brand">
          <span class="brand-mark">U</span>
          <strong>UniFinSG</strong>
        </router-link>
        <div class="mobile-header__actions">
          <CreditsBadge compact />
          <span v-if="participant" class="participant-chip">{{ participant }}</span>
          <el-button circle text aria-label="Open settings" @click="router.push({ name: 'settings' })">
            <el-icon><Setting /></el-icon>
          </el-button>
        </div>
      </header>

      <div class="page-container">
        <router-view />
      </div>
    </main>

    <nav class="bottom-navigation" aria-label="Mobile navigation">
      <router-link
        v-for="item in navItems"
        :key="item.name"
        :to="{ name: item.name }"
        class="bottom-nav-link"
        :class="{ active: isActive(item.name) }"
      >
        <el-icon><component :is="item.icon" /></el-icon>
        <span>{{ item.label }}</span>
      </router-link>
    </nav>
  </div>
</template>
