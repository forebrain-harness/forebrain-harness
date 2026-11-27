<template>
  <div class="dev-shell">
    <header class="dev-head flex items-center gap-2">
      <span class="seal">F</span>
      <b>Forebrain</b>
      <span class="ver">v{{ version }}</span>
    </header>
    <main class="dev-body">
      <p class="hint">脚手架就绪：传输走宿主 RPC，主题跟随 VS Code。对话线程由后续步骤组装。</p>
    </main>
  </div>
</template>

<script setup lang="ts">
// Step 3 scaffold: this shell only proves the pipeline — the theme class the
// host pushes, the tokens main.css serves, and the build itself. The chat
// workbench (thread, composer, sessions) is assembled over Steps 5–8 on this
// same mount point.
const version = __FOREBRAIN_VERSION__
</script>

<style scoped>
.dev-shell {
  display: flex;
  flex-direction: column;
  height: 100vh;
  background: var(--forebrain-bg);
  color: var(--forebrain-text);
  font-family: var(--forebrain-ui-font);
}
.dev-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  border-bottom: 1px solid var(--forebrain-divider);
}
.dev-head b {
  font-size: 14px;
}
.seal {
  width: 22px;
  height: 22px;
  border-radius: 6px;
  background: var(--forebrain-brand-1);
  color: var(--forebrain-on-brand);
  display: grid;
  place-items: center;
  font-size: 12px;
  font-weight: 700;
}
.ver {
  margin-left: auto;
  color: var(--forebrain-muted-text);
  font-size: 12px;
}
.dev-body {
  flex: 1;
  display: grid;
  place-items: center;
  padding: 16px;
}
.hint {
  color: var(--forebrain-muted-text);
  font-size: 13px;
  text-align: center;
}
</style>
