<template>
  <main class="mx-auto max-w-3xl space-y-6 bg-[var(--forebrain-bg)] px-6 py-10 text-[var(--forebrain-text)]">
    <header>
      <h1 class="text-2xl font-medium">组件库</h1>
      <p class="mt-1 text-[13px] text-[var(--forebrain-text-2)]">
        Web 前端通用组件的实际渲染。此页直接挂载真实组件，样式与产品页面同源。
      </p>
    </header>

    <section class="space-y-3 rounded-xl border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">ScopeBadge 作用域标签</h2>
      <div class="flex flex-wrap items-center gap-3">
        <ScopeBadge type="agent" label="主代理" />
        <ScopeBadge type="project" label="项目" />
        <ScopeBadge type="global" label="全局" />
      </div>
    </section>

    <section class="space-y-3 rounded-xl border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">BadgeComponent 徽标</h2>
      <div class="flex flex-wrap items-center gap-3">
        <BadgeComponent>默认</BadgeComponent>
        <BadgeComponent variant="gray">灰色</BadgeComponent>
        <BadgeComponent variant="success">成功</BadgeComponent>
        <BadgeComponent variant="warning">警告</BadgeComponent>
        <BadgeComponent variant="error">错误</BadgeComponent>
      </div>
    </section>

    <section class="space-y-3 rounded-xl border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">SwitchComponent 开关</h2>
      <div class="flex flex-wrap items-center gap-3">
        <SwitchComponent v-model="off" aria-label="关闭状态" />
        <SwitchComponent v-model="on" aria-label="开启状态" />
        <SwitchComponent v-model="disabled" disabled aria-label="禁用状态" />
      </div>
    </section>

    <section class="space-y-3 rounded-xl border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">CardComponent 卡片</h2>
      <CardComponent>
        <template #header>
          <h3>卡片标题</h3>
          <BadgeComponent variant="success">已连接</BadgeComponent>
        </template>
        <p class="text-[13px] text-[var(--forebrain-text-2)]">卡片主体内容，头部可选。</p>
      </CardComponent>
    </section>
  </main>
</template>

<script setup lang="ts">
/**
 * A dev-server-only gallery: it mounts the real components so a change to one
 * of them is visible here without going through a product page. It is not part
 * of the built UI (nothing links to it and it is not a build entry).
 */
import { ref } from 'vue'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import CardComponent from '@/components/common/CardComponent.vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'

const off = ref(false)
const on = ref(true)
const disabled = ref(true)
</script>
