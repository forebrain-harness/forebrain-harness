<template>
  <main class="mx-auto max-w-3xl space-y-6 bg-[var(--forebrain-bg)] px-6 py-10 text-[var(--forebrain-text)]">
    <header>
      <h1 class="text-2xl font-medium">组件库</h1>
      <p class="mt-1 text-[13px] text-[var(--forebrain-text-2)]">
        Web 前端通用组件的实际渲染。此页直接挂载真实组件，样式与产品页面同源。
      </p>
    </header>

    <section class="space-y-3 rounded-lg border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">ScopeBadge 作用域标签</h2>
      <div class="flex flex-wrap items-center gap-3">
        <ScopeBadge type="agent" label="主代理" />
        <ScopeBadge type="project" label="项目" />
        <ScopeBadge type="global" label="全局" />
      </div>
    </section>

    <section class="space-y-3 rounded-lg border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">BadgeComponent 徽标</h2>
      <div class="flex flex-wrap items-center gap-3">
        <BadgeComponent>默认</BadgeComponent>
        <BadgeComponent variant="gray">灰色</BadgeComponent>
        <BadgeComponent variant="success">成功</BadgeComponent>
        <BadgeComponent variant="warning">警告</BadgeComponent>
        <BadgeComponent variant="error">错误</BadgeComponent>
      </div>
    </section>

    <section class="space-y-3 rounded-lg border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">SwitchComponent 开关</h2>
      <div class="flex flex-wrap items-center gap-3">
        <SwitchComponent v-model="off" aria-label="关闭状态" />
        <SwitchComponent v-model="on" aria-label="开启状态" />
        <SwitchComponent v-model="disabled" disabled aria-label="禁用状态" />
      </div>
    </section>

    <section class="space-y-3 rounded-lg border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">CardComponent 卡片</h2>
      <CardComponent>
        <template #header>
          <h3>卡片标题</h3>
          <BadgeComponent variant="success">已连接</BadgeComponent>
        </template>
        <p class="text-[13px] text-[var(--forebrain-text-2)]">卡片主体内容，头部可选。</p>
      </CardComponent>
    </section>

    <section class="space-y-3 rounded-lg border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">PageTopRow 页面首行</h2>
      <PageTopRow title="技能" description="本主代理能使用的全部技能。">
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs">在线安装</button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs">刷新</button>
        </template>
      </PageTopRow>
    </section>

    <section class="space-y-3 rounded-lg border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">ForebrainModal 弹窗</h2>
      <div class="flex flex-wrap gap-2">
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="modalSmOpen = true">小弹窗 sm</button>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="modalMdOpen = true">普通弹窗 md</button>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="modalXlOpen = true">编辑器弹窗 xl</button>
      </div>
      <ForebrainModal :open="modalSmOpen" size="sm" title="小弹窗" @close="modalSmOpen = false">
        <p class="text-[13px] text-[var(--forebrain-text-2)]">440px 宽，用于只有一个输入框的表单。</p>
        <template #footer>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="modalSmOpen = false">取消</button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" @click="modalSmOpen = false">创建</button>
        </template>
      </ForebrainModal>
      <ForebrainModal :open="modalMdOpen" title="普通弹窗" @close="modalMdOpen = false">
        <p class="text-[13px] text-[var(--forebrain-text-2)]">560px 宽，默认尺寸。</p>
        <template #footer>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" @click="modalMdOpen = false">好</button>
        </template>
      </ForebrainModal>
      <ForebrainModal :open="modalXlOpen" size="xl" title="编辑器弹窗" @close="modalXlOpen = false">
        <p class="text-[13px] text-[var(--forebrain-text-2)]">860px 宽，用于文件编辑器。</p>
        <template #footer>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" @click="modalXlOpen = false">保存</button>
        </template>
      </ForebrainModal>
    </section>

    <section class="space-y-3 rounded-lg border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">ConfirmDialog 确认框</h2>
      <div class="flex flex-wrap gap-2">
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="confirmOpen = true">普通确认</button>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs text-[var(--forebrain-danger)]" @click="typedOpen = true">危险确认（需输入）</button>
      </div>
      <ConfirmDialog
        :open="confirmOpen"
        title="删除主代理 docs？"
        message="删除后不能恢复；工作区目录里的文件不会删除。"
        confirm-label="删除"
        danger
        @confirm="confirmOpen = false"
        @cancel="confirmOpen = false"
      />
      <ConfirmDialog
        :open="typedOpen"
        title="删除项目？"
        message="删除项目会一起删除它的全部对话和定时任务；目录里的文件不会删除。"
        confirm-label="删除"
        danger
        typed-confirm="forebrain-harness"
        @confirm="typedOpen = false"
        @cancel="typedOpen = false"
      />
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
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import PageTopRow from '@/components/common/PageTopRow.vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'

const off = ref(false)
const on = ref(true)
const disabled = ref(true)
const modalSmOpen = ref(false)
const modalMdOpen = ref(false)
const modalXlOpen = ref(false)
const confirmOpen = ref(false)
const typedOpen = ref(false)
</script>
