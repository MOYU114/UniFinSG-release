<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ChatDotRound, Close, Delete, EditPen, Menu, Picture, Plus, Promotion } from '@element-plus/icons-vue'
import { nanoid } from 'nanoid'
import {
  createSession,
  decideDraft,
  deleteSession,
  listMessages,
  listSessions,
  renameSession,
  sendMessage,
  uploadImage,
  uploadURL,
} from '../api/agent'
import { useAuthStore } from '../store/auth'
import { renderMarkdown } from '../utils/markdown'
import { normalizeSuggestions, toolLabel, toolResultSummary, verifiedHint } from '../utils/agent'
import { draftFromPart } from '../utils/drafts'
import {
  MAX_IMAGE_BYTES,
  MAX_IMAGES_PER_MESSAGE,
  imageRejectReason,
  prepareImage,
} from '../utils/images'
import DraftCard from '../components/chat/DraftCard.vue'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()

const sessions = ref([])
const sessionsLoading = ref(true)
const sessionsError = ref('')
const messages = ref([])
const messagesLoading = ref(false)
const historyError = ref('')
const streaming = ref(false)
const content = ref('')
const banner = ref(null)
const drawerOpen = ref(false)
const streamRef = ref(null)
const loadedSessionId = ref('')
const attachments = ref([])
// Follow-up questions for the latest finished reply; they are not stored, so
// they only show for replies received in this page visit.
const followUps = ref([])
const fileInput = ref(null)
const activeId = computed(() => (route.params.sessionId ? String(route.params.sessionId) : ''))

let activeController = null

const activeSession = computed(() => sessions.value.find((session) => String(session.id) === activeId.value) || null)
const uploading = computed(() => attachments.value.some((item) => item.uploading))
const readyUploads = computed(() => attachments.value.filter((item) => item.uploadId && !item.error))
const canSend = computed(() => {
  if (streaming.value || uploading.value) return false
  if (attachments.value.some((item) => !item.uploadId)) return false
  return content.value.trim().length > 0 || readyUploads.value.length > 0
})

const examples = [
  'Upload a receipt to record it',
  'How much can I still spend this month?',
  'Record lunch: 6.5',
]

function scrollToLatest() {
  void nextTick(() => {
    const element = streamRef.value
    if (element) element.scrollTop = element.scrollHeight
  })
}

function historyMessage(raw) {
  const calls = []
  const results = new Map()
  const drafts = []
  const images = []
  for (const part of raw.parts || []) {
    if (part.type === 'tool_call') {
      calls.push({ id: part.id, name: part.name, args: part.args || {}, summary: '', ok: true })
    } else if (part.type === 'tool_result') {
      results.set(part.call_id, part)
    } else if (part.type === 'draft') {
      drafts.push(draftFromPart(part))
    } else if (part.type === 'image' && part.upload_id) {
      images.push({ uploadId: part.upload_id, expired: Boolean(part.expired) })
    }
  }
  for (const call of calls) {
    const result = results.get(call.id)
    if (result) {
      call.ok = !result.is_error
      call.summary = toolResultSummary(call.name, result.result, result.is_error)
    }
  }
  return {
    id: `stored-${raw.id}`,
    role: raw.role,
    content: raw.content || '',
    verified: raw.verified ?? null,
    toolCalls: calls,
    drafts,
    images,
    pending: false,
    error: '',
    createdAt: raw.created_at,
  }
}

async function loadSessions() {
  sessionsLoading.value = true
  sessionsError.value = ''
  try {
    const data = await listSessions(authStore.userId, { limit: 50 })
    sessions.value = data.sessions || []
  } catch (error) {
    sessionsError.value = error?.response?.data?.error?.message || 'Conversations are unavailable right now.'
  } finally {
    sessionsLoading.value = false
  }
}

async function loadMessages(sessionId) {
  if (!sessionId) {
    messages.value = []
    loadedSessionId.value = ''
    return
  }
  messagesLoading.value = true
  historyError.value = ''
  try {
    const data = await listMessages(authStore.userId, sessionId, { limit: 50 })
    messages.value = (data.messages || []).map(historyMessage)
    loadedSessionId.value = String(sessionId)
    scrollToLatest()
  } catch (error) {
    historyError.value = error?.response?.data?.error?.message || 'This conversation could not be loaded.'
  } finally {
    messagesLoading.value = false
  }
}

async function ensureSession() {
  if (activeId.value) return activeId.value
  const created = await createSession(authStore.userId)
  const sessionId = String(created.id)
  sessions.value = [created, ...sessions.value]
  loadedSessionId.value = sessionId
  await router.replace({ name: 'chat-session', params: { sessionId } })
  return sessionId
}

function revokeAttachments() {
  for (const item of attachments.value) URL.revokeObjectURL(item.previewUrl)
  attachments.value = []
}

function pickFiles() {
  fileInput.value?.click()
}

function onFilesPicked(event) {
  // Copy first: clearing the input empties the live FileList in place.
  const files = Array.from(event.target.files || [])
  event.target.value = ''
  void handleFiles(files)
}

function onPaste(event) {
  const files = event.clipboardData?.files
  if (files && files.length) {
    event.preventDefault()
    void handleFiles(files)
  }
}

function onDrop(event) {
  const files = event.dataTransfer?.files
  if (files && files.length) void handleFiles(files)
}

function removeAttachment(item) {
  URL.revokeObjectURL(item.previewUrl)
  attachments.value = attachments.value.filter((entry) => entry.key !== item.key)
}

// handleFiles validates, compresses and uploads images. Uploads need a
// session, so the first image of a new conversation creates it.
async function handleFiles(fileList) {
  const files = Array.from(fileList || [])
  if (!files.length || streaming.value) return
  const room = MAX_IMAGES_PER_MESSAGE - attachments.value.length
  if (room <= 0) {
    ElMessage.warning(`You can attach up to ${MAX_IMAGES_PER_MESSAGE} images to one message.`)
    return
  }
  if (files.length > room) {
    ElMessage.warning(`Only ${MAX_IMAGES_PER_MESSAGE} images can be attached to one message.`)
  }
  let sessionId = activeId.value
  for (const file of files.slice(0, room)) {
    const reason = imageRejectReason(file)
    if (reason) {
      ElMessage.warning(reason)
      continue
    }
    if (!sessionId) {
      try {
        sessionId = await ensureSession()
      } catch (error) {
        showError(error)
        return
      }
    }
    const attachment = reactive({
      key: nanoid(),
      name: file.name || 'image',
      previewUrl: URL.createObjectURL(file),
      progress: 0,
      uploading: true,
      uploadId: null,
      error: '',
    })
    attachments.value.push(attachment)
    try {
      const prepared = await prepareImage(file)
      if (prepared.size > MAX_IMAGE_BYTES) throw new Error('Images must be 10MB or smaller.')
      const uploaded = await uploadImage(authStore.userId, sessionId, prepared, {
        onProgress: (value) => { attachment.progress = value },
      })
      attachment.uploadId = uploaded.upload_id
      attachment.progress = 100
    } catch (error) {
      attachment.error = error?.message || 'The image could not be uploaded.'
      ElMessage.error(attachment.error)
    } finally {
      attachment.uploading = false
    }
  }
}

async function selectSession(session) {
  if (streaming.value) return
  drawerOpen.value = false
  if (String(session.id) === activeId.value) return
  await router.push({ name: 'chat-session', params: { sessionId: session.id } })
}

async function startNewSession() {
  drawerOpen.value = false
  if (streaming.value) return
  revokeAttachments()
  await router.push({ name: 'chat' })
  messages.value = []
  loadedSessionId.value = ''
  content.value = ''
  banner.value = null
  followUps.value = []
}

async function renamePrompt(session) {
  try {
    const { value } = await ElMessageBox.prompt('New name for this conversation', 'Rename conversation', {
      inputValue: session.title,
      inputValidator: (text) => (text && text.trim().length > 0 && [...text.trim()].length <= 120) || 'Enter 1 to 120 characters',
      confirmButtonText: 'Save',
    })
    const updated = await renameSession(authStore.userId, session.id, value.trim())
    const index = sessions.value.findIndex((item) => item.id === session.id)
    if (index >= 0) sessions.value[index] = { ...sessions.value[index], ...updated }
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error(error?.response?.data?.error?.message || 'The conversation could not be renamed.')
  }
}

async function removeSession(session) {
  try {
    await ElMessageBox.confirm('This deletes the conversation and its messages.', 'Delete conversation', {
      type: 'warning',
      confirmButtonText: 'Delete',
      confirmButtonClass: 'el-button--danger',
    })
  } catch {
    return
  }
  try {
    await deleteSession(authStore.userId, session.id)
    sessions.value = sessions.value.filter((item) => item.id !== session.id)
    if (String(session.id) === activeId.value) await startNewSession()
  } catch (error) {
    ElMessage.error(error?.response?.data?.error?.message || 'The conversation could not be deleted.')
  }
}

function showError(error) {
  const code = error?.code || error?.response?.data?.error?.code || 'request_failed'
  if (code === 'llm_not_configured') {
    banner.value = {
      kind: 'upstream',
      text: 'Connect your model in Settings to start chatting or reading receipts.',
      link: { label: 'Connect a model', to: { name: 'settings' } },
    }
    return
  }
  if (code === 'credits_exhausted') {
    banner.value = {
      kind: 'credits',
      text: 'Your daily credits are used up. They reset at midnight (SGT), or connect your own model in Settings.',
      link: { label: 'Open settings', to: { name: 'settings' } },
    }
    return
  }
  if (code === 'llm_upstream_error') {
    banner.value = {
      kind: 'upstream',
      text: error?.upstream?.message || error?.message || 'The model provider returned an error.',
      link: { label: 'Check model settings', to: { name: 'settings' } },
    }
    return
  }
  if (code === 'turn_in_progress') {
    banner.value = { kind: 'busy', text: 'Another reply is still running in this conversation.', link: null }
    return
  }
  banner.value = { kind: 'generic', text: error?.message || 'The assistant could not answer.', link: null }
}

async function send(text = content.value) {
  const value = text.trim()
  const ready = readyUploads.value
  if ((!value && !ready.length) || streaming.value || uploading.value) return
  // An attachment that failed to upload must be removed before sending.
  if (attachments.value.some((item) => !item.uploadId)) return
  if ([...value].length > 2000) {
    ElMessage.warning('Keep your message within 2000 characters.')
    return
  }
  banner.value = null
  followUps.value = []

  let sessionId
  try {
    sessionId = await ensureSession()
  } catch (error) {
    showError(error)
    return
  }

  const uploadIds = ready.map((item) => item.uploadId)
  const localImages = uploadIds.map((uploadId) => ({
    uploadId,
    url: uploadURL(authStore.userId, uploadId),
  }))
  content.value = ''
  revokeAttachments()
  streaming.value = true
  const userMessage = {
    id: `local-user-${nanoid()}`,
    role: 'user',
    content: value,
    verified: null,
    toolCalls: [],
    drafts: [],
    images: localImages,
    pending: false,
    error: '',
  }
  const assistant = reactive({
    id: `local-assistant-${nanoid()}`,
    role: 'assistant',
    content: '',
    verified: null,
    toolCalls: [],
    drafts: [],
    pending: true,
    status: 'Thinking…',
    error: '',
  })
  messages.value.push(userMessage, assistant)
  scrollToLatest()

  const controller = new AbortController()
  activeController = controller
  try {
    await sendMessage(authStore.userId, sessionId, {
      content: value,
      ...(uploadIds.length ? { upload_ids: uploadIds } : {}),
      client_request_id: nanoid(),
    }, {
      onAck: (data) => {
        if (data?.user_message_id) userMessage.id = `stored-${data.user_message_id}`
        assistant.status = 'Thinking…'
      },
      onStatus: (data) => {
        assistant.status = data?.phase === 'extracting'
          ? 'Reading the image…'
          : data?.phase === 'calling_tool' ? 'Querying your ledger…' : 'Thinking…'
      },
      onToolCall: (data) => {
        assistant.toolCalls.push({ id: data?.id, name: data?.name, args: data?.args || {}, summary: '', ok: true })
        scrollToLatest()
      },
      onToolResult: (data) => {
        const call = assistant.toolCalls.find((item) => item.id === data?.id)
        if (call) {
          call.ok = data?.ok !== false
          call.summary = data?.summary || ''
        }
      },
      onDelta: (data) => {
        assistant.content += data?.text || ''
        assistant.status = ''
        scrollToLatest()
      },
      onDraft: (data) => {
        if (!data?.draft_id) return
        assistant.drafts.push({ ...data, status: 'pending' })
        scrollToLatest()
      },
      onError: (data) => {
        assistant.error = data?.message || 'The assistant could not finish this reply.'
        showError({ code: data?.code, message: data?.message, upstream: data?.upstream })
      },
      onDone: (data) => {
        assistant.pending = false
        assistant.verified = data?.verified ?? null
        if (data?.assistant_message_id) assistant.id = `stored-${data.assistant_message_id}`
        if (data?.status === 'failed' && !assistant.content) {
          assistant.content = ''
        }
        followUps.value = normalizeSuggestions(data?.suggestions)
        window.dispatchEvent(new CustomEvent('credits:refresh'))
        scrollToLatest()
      },
    }, { signal: controller.signal })
  } catch (error) {
    if (error.name === 'AbortError') return
    assistant.pending = false
    assistant.error = error?.message || 'The assistant could not answer.'
    showError(error)
  } finally {
    if (activeController === controller) activeController = null
    streaming.value = false
    assistant.pending = false
    scrollToLatest()
    void refreshSessionTitles()
  }
}

async function onDecide(payload, draft) {
  if (streaming.value || draft?.deciding) return
  draft.deciding = true
  banner.value = null
  followUps.value = []
  streaming.value = true
  const assistant = reactive({
    id: `local-assistant-${nanoid()}`,
    role: 'assistant',
    content: '',
    verified: null,
    toolCalls: [],
    drafts: [],
    pending: true,
    status: 'Thinking…',
    error: '',
  })
  messages.value.push(assistant)
  scrollToLatest()

  const controller = new AbortController()
  activeController = controller
  try {
    await decideDraft(authStore.userId, draft.draft_id, payload, {
      onStatus: (data) => {
        assistant.status = data?.phase === 'calling_tool' ? 'Querying your ledger…' : 'Thinking…'
      },
      onToolCall: (data) => {
        assistant.toolCalls.push({ id: data?.id, name: data?.name, args: data?.args || {}, summary: '', ok: true })
        scrollToLatest()
      },
      onToolResult: (data) => {
        const call = assistant.toolCalls.find((item) => item.id === data?.id)
        if (call) {
          call.ok = data?.ok !== false
          call.summary = data?.summary || ''
        }
      },
      onDelta: (data) => {
        assistant.content += data?.text || ''
        assistant.status = ''
        scrollToLatest()
      },
      onDraft: (data) => {
        if (!data?.draft_id) return
        assistant.drafts.push({ ...data, status: 'pending' })
        scrollToLatest()
      },
      onError: (data) => {
        assistant.error = data?.message || 'The assistant could not finish this reply.'
        showError({ code: data?.code, message: data?.message, upstream: data?.upstream })
      },
      onDone: (data) => {
        assistant.pending = false
        assistant.verified = data?.verified ?? null
        if (data?.assistant_message_id) assistant.id = `stored-${data.assistant_message_id}`
        followUps.value = normalizeSuggestions(data?.suggestions)
        window.dispatchEvent(new CustomEvent('credits:refresh'))
        scrollToLatest()
      },
    }, { signal: controller.signal })
  } catch (error) {
    if (error.name === 'AbortError') return
    assistant.pending = false
    assistant.error = error?.message || 'The decision could not be applied.'
    showError(error)
  } finally {
    if (activeController === controller) activeController = null
    streaming.value = false
    assistant.pending = false
    draft.deciding = false
    // The stored messages carry the authoritative draft status, the appended
    // decision result and the continuation, so reload after every decision.
    if (loadedSessionId.value) await loadMessages(loadedSessionId.value)
    window.dispatchEvent(new CustomEvent('credits:refresh'))
  }
}

async function refreshSessionTitles() {
  try {
    const data = await listSessions(authStore.userId, { limit: 50 })
    sessions.value = data.sessions || []
  } catch {
    // The list is informational; keep the optimistic copy on failure.
  }
}

function onComposerKeydown(event) {
  if (event.key !== 'Enter' || event.shiftKey || event.isComposing) return
  event.preventDefault()
  if (canSend.value) void send()
}

watch(() => route.params.sessionId, async (value) => {
  const id = value ? String(value) : ''
  drawerOpen.value = false
  // Uploads belong to one conversation, so a switch drops pending images.
  if (id !== loadedSessionId.value) {
    revokeAttachments()
    followUps.value = []
  }
  if (!id) {
    messages.value = []
    loadedSessionId.value = ''
    return
  }
  if (id === loadedSessionId.value) return
  await loadMessages(id)
})

onMounted(async () => {
  await loadSessions()
  if (activeId.value) await loadMessages(activeId.value)
})

onBeforeUnmount(() => {
  activeController?.abort()
  revokeAttachments()
})
</script>

<template>
  <div class="chat-page">
    <div v-if="drawerOpen" class="chat-drawer-backdrop" @click="drawerOpen = false" />
    <aside class="chat-sessions" :class="{ 'chat-sessions--open': drawerOpen }" aria-label="Conversations">
      <div class="chat-sessions__header">
        <strong>Conversations</strong>
        <el-button text circle aria-label="New conversation" :disabled="streaming" @click="startNewSession">
          <el-icon><Plus /></el-icon>
        </el-button>
      </div>
      <div class="chat-session-list">
        <el-skeleton v-if="sessionsLoading" :rows="4" animated style="padding: 8px" />
        <p v-else-if="sessionsError" class="chat-sessions__empty">{{ sessionsError }}</p>
        <p v-else-if="!sessions.length" class="chat-sessions__empty">No conversations yet. Ask your first question.</p>
        <template v-else>
          <div
            v-for="session in sessions"
            :key="session.id"
            class="chat-session-item"
            :class="{ active: String(session.id) === activeId }"
          >
            <a href="#" :title="session.title" @click.prevent="selectSession(session)">{{ session.title }}</a>
            <div class="chat-session-item__actions">
              <el-button text circle size="small" aria-label="Rename conversation" @click.stop="renamePrompt(session)">
                <el-icon><EditPen /></el-icon>
              </el-button>
              <el-button text circle size="small" aria-label="Delete conversation" @click.stop="removeSession(session)">
                <el-icon><Delete /></el-icon>
              </el-button>
            </div>
          </div>
        </template>
      </div>
    </aside>

    <section class="chat-main">
      <div class="chat-main__topbar">
        <el-button text circle aria-label="Open conversations" @click="drawerOpen = true">
          <el-icon><Menu /></el-icon>
        </el-button>
        <strong>{{ activeSession?.title || 'New conversation' }}</strong>
        <el-button text circle aria-label="New conversation" :disabled="streaming" @click="startNewSession">
          <el-icon><Plus /></el-icon>
        </el-button>
      </div>

      <div ref="streamRef" class="chat-stream" aria-live="polite">
        <div v-if="banner" class="chat-banner" :class="`chat-banner--${banner.kind}`" role="alert">
          <span>{{ banner.text }}</span>
          <router-link v-if="banner.link" :to="banner.link.to">{{ banner.link.label }}</router-link>
        </div>

        <div v-if="messagesLoading" class="chat-thinking"><i /><i /><i /> Loading conversation…</div>
        <div v-else-if="historyError" class="chat-banner chat-banner--generic" role="alert">
          <span>{{ historyError }}</span>
          <a href="#" @click.prevent="loadMessages(activeId)">Try again</a>
        </div>

        <div v-else-if="!messages.length" class="chat-empty">
          <span class="chat-empty__orb"><el-icon><ChatDotRound /></el-icon></span>
          <span class="eyebrow">Your money, in conversation</span>
          <h2>What would you like to know?</h2>
          <p>I read your saved budget and expenses with tools, so every amount I quote comes from your ledger.</p>
          <div class="chat-empty__examples">
            <button v-for="example in examples" :key="example" type="button" :disabled="streaming" @click="send(example)">
              {{ example }}
            </button>
          </div>
        </div>

        <template v-else>
          <article
            v-for="message in messages"
            :key="message.id"
            class="chat-message"
            :class="`chat-message--${message.role}`"
          >
            <div v-if="message.role === 'assistant'" class="chat-avatar"><el-icon><ChatDotRound /></el-icon></div>
            <div class="chat-bubble">
              <template v-if="message.role === 'user'">
                <div v-if="message.images?.length" class="chat-images">
                  <template v-for="image in message.images" :key="image.uploadId || image.url">
                    <span v-if="image.expired" class="chat-image-expired">Image expired</span>
                    <img
                      v-else
                      class="chat-image"
                      :src="image.url || uploadURL(authStore.userId, image.uploadId)"
                      alt="Attached image"
                      loading="lazy"
                      crossorigin="use-credentials"
                      @error="image.expired = true"
                    />
                  </template>
                </div>
                <span v-if="message.content" class="chat-text">{{ message.content }}</span>
              </template>
              <template v-else>
                <div v-if="message.toolCalls?.length" class="chat-tools">
                  <details v-for="call in message.toolCalls" :key="call.id" class="chat-tool">
                    <summary>
                      <span class="chat-tool__dot" :class="{ 'chat-tool__dot--error': call.ok === false }" />
                      <span class="chat-tool__summary">{{ call.summary || toolLabel(call.name, call.args) }}</span>
                    </summary>
                    <div class="chat-tool__body">
                      {{ call.ok === false ? 'This lookup failed and the assistant tried another way.' : `Tool: ${call.name}` }}
                    </div>
                  </details>
                </div>

                <div v-if="message.content" class="chat-answer" v-html="renderMarkdown(message.content)" />

                <DraftCard
                  v-for="draft in message.drafts"
                  :key="draft.draft_id"
                  :draft="draft"
                  :busy="streaming"
                  @decide="onDecide($event, draft)"
                />

                <span v-if="message.pending && !message.content" class="chat-thinking">
                  <i /><i /><i /> {{ message.status || 'Thinking…' }}
                </span>

                <p v-if="message.error" class="chat-verified" role="alert">{{ message.error }}</p>
                <p v-else-if="verifiedHint(message.verified)" class="chat-verified">{{ verifiedHint(message.verified) }}</p>
              </template>
            </div>
          </article>
          <div v-if="followUps.length && !streaming" class="chat-followups" aria-label="Suggested follow-up questions">
            <button v-for="item in followUps" :key="item" type="button" @click="send(item)">{{ item }}</button>
          </div>
        </template>
      </div>

      <footer class="chat-composer" @dragover.prevent @drop.prevent="onDrop">
        <div v-if="attachments.length" class="chat-attachments">
          <div v-for="item in attachments" :key="item.key" class="chat-attachment">
            <img class="chat-attachment__thumb" :src="item.previewUrl" :alt="item.name" />
            <button type="button" class="chat-attachment__remove" aria-label="Remove image" @click="removeAttachment(item)">
              <el-icon><Close /></el-icon>
            </button>
            <span v-if="item.uploading" class="chat-attachment__status">{{ item.progress }}%</span>
            <span v-else-if="item.error" class="chat-attachment__status chat-attachment__status--error">Failed</span>
          </div>
        </div>
        <div class="chat-composer__row">
          <el-button
            class="chat-composer__attach"
            text
            circle
            :disabled="streaming || uploading || attachments.length >= MAX_IMAGES_PER_MESSAGE"
            aria-label="Attach images"
            @click="pickFiles"
          >
            <el-icon><Picture /></el-icon>
          </el-button>
          <el-input
            v-model="content"
            type="textarea"
            :autosize="{ minRows: 2, maxRows: 6 }"
            maxlength="2000"
            :disabled="streaming"
            placeholder="Ask about your budget or expenses…"
            aria-label="Message"
            @keydown="onComposerKeydown"
            @paste="onPaste"
          />
          <el-button
            class="chat-composer__send"
            type="primary"
            :loading="streaming"
            :disabled="!canSend"
            aria-label="Send message"
            @click="send()"
          >
            <el-icon><Promotion /></el-icon><span class="chat-send-label">Send</span>
          </el-button>
        </div>
        <p class="chat-composer__hint">Images are sent to the model you use. Amounts come from your ledger. This is educational budgeting guidance, not professional financial advice.</p>
        <input
          ref="fileInput"
          class="chat-file-input"
          type="file"
          accept="image/png,image/jpeg,image/webp"
          multiple
          @change="onFilesPicked"
        />
      </footer>
    </section>
  </div>
</template>
