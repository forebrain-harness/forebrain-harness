<template>
  <div class="grid gap-3">
    <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

    <ListToolbar>
      <template #left>
        <Input
          v-model="q"
          type="search"
          :placeholder="t('models.searchPlaceholder')"
          class="forebrain-field h-[34px] w-auto"
          style="max-width: 260px"
          data-testid="models-search"
        />
      </template>
      <template #actions>
        <Button
          type="button"
          class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs"
          data-testid="models-add"
          @click="openAdd"
        >
          <Plus class="size-3.5" aria-hidden="true" />{{ t('models.add') }}
        </Button>
        <Button
          type="button"
          variant="ghost"
          class="forebrain-btn forebrain-btn--ghost h-auto gap-1.5 text-xs"
          data-testid="models-subscribe"
          @click="openSubscribe('')"
        >
          <LogIn class="size-3.5" aria-hidden="true" />{{ t('models.subscribe') }}
        </Button>
        <Button
          type="button"
          variant="ghost"
          class="forebrain-btn forebrain-btn--ghost h-auto gap-1.5 text-xs"
          :disabled="loading"
          data-testid="models-refresh"
          @click="refresh"
        >
          <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
        </Button>
      </template>
    </ListToolbar>

    <div v-if="loading && !rows.length" class="py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
      {{ t('common.loading') }}
    </div>
    <ModelsTable
      v-else
      :rows="rows"
      :flash-id="flashId"
      @edit="openEdit"
      @remove="askRemove"
      @set-default="makeDefault"
    />

    <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />

    <ModelFormDrawer
      :open="drawerOpen"
      :mode="drawerMode"
      :row="drawerRow"
      :providers="providers"
      :protocols="protocols"
      :custom-id="customId"
      :suggestions="suggestions"
      @close="drawerOpen = false"
      @saved="onSaved"
      @duplicate="gotoRow"
      @open-subscribe="relogin"
    />

    <SubscriptionLoginModal :open="subscribeOpen" :initial-id="subscribeId" @close="subscribeOpen = false" @saved="onSubscribed" />

    <ConfirmDialog
      :open="removeRow !== null"
      :title="t('models.deleteTitle')"
      :message="t('models.deleteMessage')"
      :confirm-label="t('models.delete')"
      danger
      @cancel="removeRow = null"
      @confirm="confirmRemove"
    />

    <!-- A default model cannot be deleted: the row names the agents that would
         lose their default, and the page says which instead of a bare refusal. -->
    <ForebrainModal :open="refusedFor.length > 0" size="sm" :title="t('models.deleteRefusedTitle')" @close="refusedFor = []">
      <p class="text-[13px] text-[var(--forebrain-text-2)]">{{ t('models.deleteRefusedMessage') }}</p>
      <div class="mt-2 flex flex-wrap gap-1.5">
        <BadgeComponent v-for="agent in refusedFor" :key="agent" data-testid="models-refused-agent">{{ agent }}</BadgeComponent>
      </div>
      <template #footer>
        <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" data-testid="models-refused-ok" @click="refusedFor = []">
          {{ t('models.deleteRefusedOk') }}
        </Button>
      </template>
    </ForebrainModal>
  </div>
</template>

<script setup lang="ts">
/**
 * Settings · Models (plan 266): the install's one table of models, which every
 * primary agent shares. It replaced the per-agent 「模型服务」 page, so this is
 * the only place a model is added, edited or removed — and the page a model
 * picker sends you to when the row you want is missing.
 *
 * The table is server-paged and server-searched like every other list here. A
 * save, a delete or a new default reloads the page it was on; the row a refusal
 * named is found by the id the gateway answered with, which is what lets
 * 「去编辑那一条」 land on the row that already exists, on whatever page it sits.
 */
import { computed, onMounted, ref } from 'vue'
import { LogIn, Plus, RotateCw } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'

import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import TablePager from '@/components/common/TablePager.vue'
import ModelFormDrawer from '@/components/models/ModelFormDrawer.vue'
import ModelsTable from '@/components/models/ModelsTable.vue'
import SubscriptionLoginModal from '@/components/models/SubscriptionLoginModal.vue'
import { useServerList } from '@/composables/useServerList'
import {
  forebrainApi,
  GatewayHttpError,
  getErrorMessage,
  type LlmModelRow,
  type LlmProviderOption,
} from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()

const { rows, total, page, pageSize, q, loading, error, load, reload } = useServerList<LlmModelRow>(
  (query) => forebrainApi.llmModelsPage(query),
  { pageSize: 10 },
)

/** The provider registry the form is built from, and the protocols it offers. */
const providers = ref<LlmProviderOption[]>([])
const protocols = ref<{ apiPath: string; name: string }[]>([])
const customId = ref('custom')
/** The models the catalog suggests per provider (「常用」), read once and kept:
 *  it is a convenience, and a failure only leaves the chips out. */
const suggestions = ref<Record<string, string[]>>({})

const drawerOpen = ref(false)
const drawerMode = ref<'add' | 'edit'>('add')
const drawerRow = ref<LlmModelRow | null>(null)

const subscribeOpen = ref(false)
const subscribeId = ref('')

const removeRow = ref<LlmModelRow | null>(null)
const refusedFor = ref<string[]>([])
/** The row a refusal pointed at: flashed for a moment after the jump. */
const flashId = ref('')

const currentPage = computed(() => rows.value)

async function loadRegistry(): Promise<void> {
  try {
    const listing = await forebrainApi.llmProviders()
    providers.value = listing.providers
    protocols.value = listing.protocols
    customId.value = listing.customId
  } catch {
    // The picker falls back to 「自定义」 only: a form that cannot name a
    // built-in provider still lets a person type one by hand.
    providers.value = []
  }
  try {
    const listing = await forebrainApi.getModels({ limit: 500 })
    const byProvider: Record<string, string[]> = {}
    for (const record of listing.records) {
      const provider = String(record.provider ?? '').trim()
      const model = String(record.apiModel ?? record.modelId ?? '').trim()
      if (!provider || !model) continue
      const list = (byProvider[provider] ??= [])
      if (list.length < 6 && !list.includes(model)) list.push(model)
    }
    suggestions.value = byProvider
  } catch {
    suggestions.value = {}
  }
}

function refresh(): void {
  void reload()
}

function openAdd(): void {
  drawerMode.value = 'add'
  drawerRow.value = null
  drawerOpen.value = true
}

function openEdit(row: LlmModelRow): void {
  drawerMode.value = 'edit'
  drawerRow.value = row
  drawerOpen.value = true
}

function openSubscribe(id: string): void {
  subscribeId.value = id
  drawerOpen.value = false
  subscribeOpen.value = true
}

/** 「重新登录」 from a subscription row's drawer: the same modal, already on that
 *  subscription's sign-in. */
function relogin(id: string): void {
  openSubscribe(id)
}

function onSaved(): void {
  drawerOpen.value = false
  void reload()
}

function onSubscribed(): void {
  subscribeOpen.value = false
  void reload()
}

async function makeDefault(row: LlmModelRow): Promise<void> {
  if (row.isDefault) return
  try {
    await forebrainApi.setDefaultLlmModel(row.id)
    await reload()
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

function askRemove(row: LlmModelRow): void {
  removeRow.value = row
}

async function confirmRemove(): Promise<void> {
  const row = removeRow.value
  removeRow.value = null
  if (!row) return
  try {
    await forebrainApi.deleteLlmModel(row.id)
    await reload()
  } catch (cause) {
    if (cause instanceof GatewayHttpError && cause.status === 409) {
      const agents = cause.payload?.defaultFor
      refusedFor.value = Array.isArray(agents) ? agents.map(String) : []
      return
    }
    error.value = getErrorMessage(cause)
  }
}

/** 「去编辑那一条」: find which page the row is on, go there, open it and flash it.
 *  The table's own page size decides the page, so a row on the second page of
 *  ten opens the second page of ten. */
async function gotoRow(id: string): Promise<void> {
  drawerOpen.value = false
  try {
    const all = await forebrainApi.llmModelsPage({ page: 1, pageSize: 1000 })
    const index = all.rows.findIndex((row) => row.id === id)
    if (index < 0) {
      await reload()
      return
    }
    q.value = ''
    page.value = Math.floor(index / pageSize.value) + 1
    await reload()
    const row = currentPage.value.find((entry) => entry.id === id) ?? all.rows[index]
    flashId.value = id
    window.setTimeout(() => {
      if (flashId.value === id) flashId.value = ''
    }, 2000)
    openEdit(row)
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

onMounted(() => {
  void load()
  void loadRegistry()
})
</script>
