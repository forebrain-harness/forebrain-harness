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
      <h2 class="text-[15px] font-medium">表单控件</h2>
      <p class="text-[12px] text-[var(--forebrain-text-2)]">
        全站表单只有这一套：控件是 <code class="font-mono">@repo/shadcn-vue/components/ui/*</code>
        的组件，外观只有一个来源，就是设计类——字段是 <code class="font-mono">forebrain-field</code>，
        按钮是它的普通 / 主色 / 次要三档（都在 <code class="font-mono">main.css</code>，计划 096）。
        下面每一格都是真实可用的控件。
      </p>

      <div class="grid gap-3 md:grid-cols-2">
        <div class="grid gap-1">
          <Label for="gallery-input" class="text-[12px] font-normal text-[var(--forebrain-text-2)]">输入框 Input</Label>
          <Input id="gallery-input" v-model="sample" class="forebrain-field h-9 w-full" placeholder="请输入名称" />
        </div>
        <div class="grid gap-1">
          <Label for="gallery-select" class="text-[12px] font-normal text-[var(--forebrain-text-2)]">下拉 Select</Label>
          <Select :model-value="sampleChoice" @update:model-value="(value) => (sampleChoice = String(value ?? ''))">
            <SelectTrigger id="gallery-select" class="forebrain-field h-9 w-full">
              <SelectValue placeholder="请选择" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="chat">Chat Completions</SelectItem>
              <SelectItem value="responses">Responses</SelectItem>
              <SelectItem value="messages">Anthropic Messages</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="grid gap-1">
          <Label for="gallery-select-sm" class="text-[12px] font-normal text-[var(--forebrain-text-2)]">
            下拉 34px（工具栏筛选的形状）
          </Label>
          <Select :model-value="sampleChoice" @update:model-value="(value) => (sampleChoice = String(value ?? ''))">
            <SelectTrigger id="gallery-select-sm" class="forebrain-field !h-[34px] w-[220px]">
              <SelectValue placeholder="请选择" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="chat">Chat Completions</SelectItem>
              <SelectItem value="responses">Responses</SelectItem>
              <SelectItem value="messages">Anthropic Messages</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="grid gap-1">
          <Label for="gallery-textarea" class="text-[12px] font-normal text-[var(--forebrain-text-2)]">文本域 Textarea</Label>
          <Textarea id="gallery-textarea" v-model="sampleText" rows="3" class="forebrain-field min-h-0 w-full" />
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-4 border-t border-[var(--forebrain-divider)] pt-3">
        <span class="flex items-center gap-2 text-[13px]">
          <Checkbox
            id="gallery-checkbox"
            :model-value="sampleChecked"
            @update:model-value="(value) => (sampleChecked = value === true)"
          />
          <Label for="gallery-checkbox" class="text-[13px] font-normal">复选框 Checkbox</Label>
        </span>
        <span class="flex items-center gap-2 text-[13px]">
          开关 Switch
          <SwitchComponent v-model="sampleSwitch" aria-label="示例开关" />
        </span>
      </div>

      <div class="grid gap-3 border-t border-[var(--forebrain-divider)] pt-3">
        <RadioField v-model="sampleRadio" label="单选 RadioGroup" name="gallery-sample" :options="sampleRadioOptions" />
        <div class="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            class="forebrain-btn h-auto bg-transparent text-[var(--forebrain-text)] hover:bg-transparent"
          >
            普通按钮
          </Button>
          <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto">主要按钮</Button>
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto">次要按钮</Button>
        </div>
      </div>
    </section>

    <section class="space-y-3 rounded-lg border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">ForebrainModal 弹窗</h2>
      <div class="flex flex-wrap gap-2">
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="modalSmOpen = true">小弹窗 sm</Button>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="modalMdOpen = true">普通弹窗 md</Button>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="modalXlOpen = true">编辑器弹窗 xl</Button>
      </div>
      <ForebrainModal :open="modalSmOpen" size="sm" title="小弹窗" @close="modalSmOpen = false">
        <p class="text-[13px] text-[var(--forebrain-text-2)]">440px 宽，用于只有一个输入框的表单。</p>
        <template #footer>
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="modalSmOpen = false">取消</Button>
          <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" @click="modalSmOpen = false">创建</Button>
        </template>
      </ForebrainModal>
      <ForebrainModal :open="modalMdOpen" title="普通弹窗" @close="modalMdOpen = false">
        <p class="text-[13px] text-[var(--forebrain-text-2)]">560px 宽，默认尺寸。</p>
        <template #footer>
          <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" @click="modalMdOpen = false">好</Button>
        </template>
      </ForebrainModal>
      <ForebrainModal :open="modalXlOpen" size="xl" title="编辑器弹窗" @close="modalXlOpen = false">
        <p class="text-[13px] text-[var(--forebrain-text-2)]">860px 宽，用于文件编辑器。</p>
        <template #footer>
          <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" @click="modalXlOpen = false">保存</Button>
        </template>
      </ForebrainModal>
    </section>

    <section class="space-y-3 rounded-lg border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">ConfirmDialog 确认框</h2>
      <div class="flex flex-wrap gap-2">
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="confirmOpen = true">普通确认</Button>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs text-[var(--forebrain-danger)]" @click="typedOpen = true">危险确认（需输入）</Button>
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

    <section class="space-y-3 rounded-lg border border-[var(--forebrain-divider)] p-4">
      <h2 class="text-[15px] font-medium">列表与表单（plan 071）</h2>
      <p class="text-[12px] text-[var(--forebrain-text-2)]">
        表格 + 上方工具栏 + 下方说明 + 分页器；行操作为同尺寸图标按钮（悬停出紧凑提示），可排序列在表头排序。
      </p>

      <ListToolbar>
        <template #left>
          <Input v-model="search" class="forebrain-field h-[34px] w-[240px]" placeholder="搜索名称" />
        </template>
        <template #actions>
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="drawerOpen = true">查看入参</Button>
          <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" @click="drawerOpen = true">添加 MCP 服务</Button>
        </template>
      </ListToolbar>

      <DataTable
        :columns="galleryColumns"
        :rows="galleryRows"
        :row-key="(row) => row.name"
        :sorting="{ key: sort, order }"
        @update:sorting="onSort($event.key, $event.order)"
      >
        <template #cell-name="{ row }">
          <span v-tip.trunc="row.name" class="forebrain-cell-1">{{ row.name }}</span>
        </template>
        <template #cell-updated="{ row }">{{ row.updated }}</template>
        <template #cell-actions="{ row }">
          <RowActions>
            <RowAction :icon="Eye" label="查看" :name="row.name" @click="drawerOpen = true" />
            <RowAction :icon="Pencil" label="编辑" :name="row.name" />
            <RowAction v-if="row.removable" :icon="Trash2" label="删除" :name="row.name" tone="danger" />
            <RowActionSlot v-else />
          </RowActions>
        </template>
      </DataTable>

      <ListNote>共 3 条示例；翻页、排序与搜索都由服务端完成（q / sort / order / page / page_size）。</ListNote>

      <TablePager
        :total="196"
        :page="page"
        :page-size="pageSize"
        @update:page="page = $event"
        @update:page-size="pageSize = $event"
      />

      <div class="grid gap-5 border-t border-[var(--forebrain-divider)] pt-4">
        <RadioField
          v-model="density"
          label="列表密度"
          name="density"
          :options="densityOptions"
          :hint="`当前：${density}，一行的高度随之变化`"
        />
        <RadioField
          v-model="brand"
          label="品牌色"
          name="brand"
          vertical
          :options="brandOptions"
          hint="竖排用于每个选项都带说明；色块显示选项本身的颜色"
        />
      </div>
    </section>

    <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="drawerOpen = true">打开抽屉（只读）</Button>
    <ForebrainDrawer :open="drawerOpen" title="MCP 服务 · github" readonly @close="drawerOpen = false">
      <label class="forebrain-form-field">
        <span>名称</span>
        <Input class="forebrain-field h-[34px]" default-value="github" readonly />
      </label>
      <label class="forebrain-form-field">
        <span>命令</span>
        <Input class="forebrain-field h-[34px]" default-value="npx -y @modelcontextprotocol/server-github" readonly />
      </label>
      <template #footer>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="drawerOpen = false">取消</Button>
      </template>
    </ForebrainDrawer>
  </main>
</template>

<script setup lang="ts">
/**
 * A dev-server-only gallery: it mounts the real components so a change to one
 * of them is visible here without going through a product page. It is not part
 * of the built UI (nothing links to it and it is not a build entry).
 */
import { Eye, Pencil, Trash2 } from 'lucide-vue-next'
import { ref } from 'vue'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Checkbox } from '@repo/shadcn-vue/components/ui/checkbox'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Label } from '@repo/shadcn-vue/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'
import { Textarea } from '@repo/shadcn-vue/components/ui/textarea'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import CardComponent from '@/components/common/CardComponent.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ForebrainDrawer from '@/components/common/ForebrainDrawer.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RadioField from '@/components/common/RadioField.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import RowActions from '@/components/common/RowActions.vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import TablePager from '@/components/common/TablePager.vue'

const off = ref(false)
const on = ref(true)
const disabled = ref(true)
const modalSmOpen = ref(false)
const modalMdOpen = ref(false)
const modalXlOpen = ref(false)
const confirmOpen = ref(false)
const typedOpen = ref(false)

// The list and form pieces (plan 071): a three-column table with a cut-off
// name, a row without its delete action keeping the 28px hole, and the pager
// the product pages will use — 196 items, so the page numbers elide the way
// they do in a long list.
const search = ref('')
const sort = ref('updated')
const order = ref<'asc' | 'desc'>('desc')
const page = ref(10)
const pageSize = ref(10)
const drawerOpen = ref(false)
const density = ref('standard')
const brand = ref('teal')

// The form-controls section (plan 096): one live instance of every control the
// product's forms are made of, so the shapes can be compared to the design.
const sample = ref('')
const sampleText = ref('')
const sampleChoice = ref('')
const sampleChecked = ref(true)
const sampleSwitch = ref(true)
const sampleRadio = ref('a')
const sampleRadioOptions = [
  { value: 'a', label: '选项 A' },
  { value: 'b', label: '选项 B' },
]

const galleryRows = [
  { name: 'github', updated: '2026-10-18 09:12', removable: true },
  { name: 'playwright', updated: '2026-10-17 21:40', removable: true },
  { name: 'a very long server name that does not fit in its column and ends in an ellipsis', updated: '2026-10-16 08:03', removable: false },
]

/** The demo table's columns — the same component every page draws. */
const galleryColumns: DataColumn[] = [
  { id: 'name', header: '名称', enableSorting: true },
  { id: 'updated', header: '更新时间', enableSorting: true, sortDescFirst: true, cellClass: 'forebrain-num' },
  { id: 'actions', header: '操作', headerClass: 'text-right' },
]

const densityOptions = [
  { value: 'compact', label: '紧凑' },
  { value: 'standard', label: '标准' },
  { value: 'loose', label: '宽松' },
]

const brandOptions = [
  { value: 'teal', label: '青', swatch: '#0D91B0', description: '默认品牌色，用于主按钮与当前项' },
  { value: 'ink', label: '墨', swatch: '#101828', description: '深色品牌，用于强调说明文字' },
  { value: 'mint', label: '薄荷', swatch: '#6CE6E8', description: '运行中状态的颜色' },
]

/** The headers only announce what a server-paged table would be asked for. */
function onSort(key: string, next: 'asc' | 'desc') {
  sort.value = key
  order.value = next
}
</script>
