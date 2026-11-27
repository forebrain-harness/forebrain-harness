<template>
  <SessionsPanel :active-session-id="activeSessionId" @open="open" @create="create" />
</template>

<script setup lang="ts">
// The Sessions app's root: a pick hands the session to the host, which opens
// it in the chat view; the host reports back which conversation chat has on
// screen, and that row is the marked one.
import { inject, ref } from 'vue'
import SessionsPanel from './components/SessionsPanel.vue'
import { TRANSPORT_KEY } from './transport'

const injectedTransport = inject(TRANSPORT_KEY)
// sessions-main.ts is the only mount point and it always provides.
const transport: NonNullable<typeof injectedTransport> = injectedTransport
  ?? (() => { throw new Error('the sessions view needs the webview transport provided') })()

const activeSessionId = ref('')

transport.onActiveSession((sessionId) => {
  activeSessionId.value = sessionId
})

function open(sessionId: string) {
  transport.openChatSession(sessionId)
  // The chat view reports back when it follows; showing the pick at once
  // keeps the list honest while the other view loads.
  activeSessionId.value = sessionId
}

function create() {
  // A new chat is a chat-view affair: open chat without a session and its
  // fresh-chat page takes over.
  transport.openChatSession('')
}
</script>
