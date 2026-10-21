<template>
  <Table
    ref="shell"
    container-class="forebrain-table-wrap"
    class="forebrain-table forebrain-table--fixed"
    :table-style="{ width: `${totalSize}px`, minWidth: '100%' }"
  >
    <colgroup>
      <col v-for="column in table.getVisibleLeafColumns()" :key="column.id" :style="{ width: `${column.getSize()}px` }" />
    </colgroup>
    <TableHeader class="[&_tr]:border-0">
      <TableRow class="border-0 hover:bg-transparent">
        <TableHead
          v-for="header in headers"
          :key="header.id"
          class="h-auto"
          :class="[
            columnsById.get(header.column.id)?.headerClass,
            {
              'forebrain-sortable': header.column.getCanSort(),
              'is-on': header.column.getCanSort() && sorting?.key === header.column.id,
            },
          ]"
          :aria-sort="ariaSort(header.column.id)"
        >
          <SortButton
            v-if="header.column.getCanSort()"
            :label="labelOf(header.column.id)"
            :sort-key="header.column.id"
            :active="sorting?.key === header.column.id"
            :order="sorting?.order ?? 'asc'"
            :first="header.column.columnDef.sortDescFirst ? 'desc' : 'asc'"
            @sort="applySort"
          />
          <slot v-else :name="`header-${header.column.id}`">{{ labelOf(header.column.id) }}</slot>
          <span
            v-if="header.column.getCanResize()"
            class="forebrain-col-resizer"
            :class="{ 'is-resizing': header.column.getIsResizing() }"
            data-col-resizer
            @mousedown="header.getResizeHandler()?.($event)"
            @touchstart.prevent="header.getResizeHandler()?.($event)"
            @dblclick="autofit(header.column.id)"
          />
        </TableHead>
      </TableRow>
    </TableHeader>
    <TableBody>
      <!-- One row, and the row it opens: a table whose row expands in place
           (a tool's inputs, a cron run's output) draws that second row through
           the `#sub-row` slot, across every column. -->
      <template v-for="(row, rowIndex) in table.getRowModel().rows" :key="rowKey(row.original)">
        <TableRow
          class="border-0 hover:bg-transparent"
          :class="rowClass?.(row.original)"
          v-bind="rowAttrs?.(row.original) ?? {}"
        >
          <TableCell v-for="cell in row.getVisibleCells()" :key="cell.id" :class="columnsById.get(cell.column.id)?.cellClass">
            <slot :name="`cell-${cell.column.id}`" :row="row.original" :row-index="rowIndex">{{ cell.getValue() }}</slot>
          </TableCell>
        </TableRow>
        <TableRow
          v-if="subRow?.(row.original)"
          class="border-0 hover:bg-transparent"
          :data-sub-row="rowKey(row.original)"
        >
          <TableCell :colspan="table.getVisibleLeafColumns().length">
            <slot name="sub-row" :row="row.original" :row-index="rowIndex" />
          </TableCell>
        </TableRow>
      </template>
      <TableEmpty v-if="rows.length === 0 && $slots.empty" :colspan="table.getVisibleLeafColumns().length">
        <slot name="empty" />
      </TableEmpty>
    </TableBody>
  </Table>
</template>

<script lang="ts">
/**
 * A column of a `DataTable`. `size` is where the column starts; a column without
 * one takes the room the others leave, which is how the design's flexible
 * "models" column behaves. `enableSorting` opens the header's sort control, and
 * the column's id doubles as the key the server is asked to sort by.
 */
export interface DataColumn {
  id: string
  header: string
  size?: number
  minSize?: number
  maxSize?: number
  enableSorting?: boolean
  /** The direction the first click sorts by; false is A→Z / oldest first. */
  sortDescFirst?: boolean
  headerClass?: string
  cellClass?: string
}

/** The column the list is ordered by, and which way. */
export type TableSorting = { key: string; order: 'asc' | 'desc' }
</script>

<script setup lang="ts" generic="Row extends RowData">
/**
 * The web's table (plan 084 pilot): @tanstack/vue-table's state machine under
 * shadcn-vue's table primitives, wearing the design's own `forebrain-table`
 * classes. It owns what the design asks for and a hand-written table cannot do:
 * a column dragged to a width without a page reload, a double-click on the
 * handle that fits a column to its content, and — where a list's server can sort
 * — a header that asks for a direction.
 *
 * What it deliberately does not own:
 *
 * - **The rows.** `manualSorting` is on and the list is server-paged, so the
 *   table never orders or slices what it is given; a sort click emits the key
 *   and direction and the caller's `useServerList` asks the server for that
 *   page. A column is sortable only if its definition says so, because most
 *   list endpoints ignore `sort`.
 * - **The widths, after a reload.** Column sizes live in this component's
 *   state and nowhere else (owner 2026-10-21): a refresh starts from the design
 *   again, and no browser storage or server row remembers a drag.
 *
 * The first layout is measured rather than left to the browser: `table-layout:
 * fixed` with a percentage width spreads the leftover space between columns by
 * itself, so the first drag would jump. Laying the columns out to exactly the
 * container's width means the number the drag starts from is the number on
 * screen.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import {
  type ColumnSizingState,
  type RowData,
  type SortingState,
  type Updater,
  getCoreRowModel,
  useVueTable,
} from '@tanstack/vue-table'
import { Table, TableBody, TableCell, TableEmpty, TableHead, TableHeader, TableRow } from '@repo/shadcn-vue/components/ui/table'

import SortButton from '@/components/common/SortButton.vue'
import { COLUMN_MAX_SIZE, COLUMN_MIN_SIZE, contentWidth } from '@/lib/table/autofit'

const props = defineProps<{
  columns: DataColumn[]
  rows: Row[]
  /** The row's identity: what survives a reload, a reorder and a re-render. */
  rowKey: (row: Row) => string
  /** Attributes a row carries besides its class (`data-provider-row` and the like). */
  rowAttrs?: (row: Row) => Record<string, string | undefined>
  /** A class the row carries — the open row of a table that expands in place. */
  rowClass?: (row: Row) => string | Record<string, boolean>
  /** Whether the row opens a second row of its own (see the `#sub-row` slot). */
  subRow?: (row: Row) => boolean
  /** The order the server is holding, so the header can show its own arrow. */
  sorting?: TableSorting
}>()

const emit = defineEmits<{ 'update:sorting': [sorting: TableSorting] }>()

/** The scroll container shadcn's table sits in; measured, never restyled here. */
const shell = ref<{ $el: HTMLElement } | null>(null)

/** Where each column starts, and where a drag has since taken it. */
const sizing = ref<ColumnSizingState>({})

const sortingState = computed<SortingState>(() =>
  props.sorting ? [{ id: props.sorting.key, desc: props.sorting.order === 'desc' }] : [],
)

/**
 * The table's state, held here and nowhere else. A reactive object rather than a
 * getter: the adapter reads this once per update, so the state it holds must be
 * the one that changes underneath it — a snapshot would leave every header and
 * column reading the widths of the first frame.
 */
const tableState = reactive({ columnSizing: sizing, sorting: sortingState })

const table = useVueTable<Row>({
  get data() {
    return props.rows
  },
  get columns() {
    return props.columns.map(toColumnDef)
  },
  state: tableState,
  onColumnSizingChange: (updater: Updater<ColumnSizingState>) => {
    sizing.value = typeof updater === 'function' ? updater(sizing.value) : updater
  },
  getCoreRowModel: getCoreRowModel(),
  // The server sorts; the table only says which column and which way.
  manualSorting: true,
  enableColumnResizing: true,
  columnResizeMode: 'onChange',
  defaultColumn: { minSize: COLUMN_MIN_SIZE, maxSize: COLUMN_MAX_SIZE },
})

const columnsById = computed(() => new Map(props.columns.map((column) => [column.id, column])))

const headers = computed(() => table.getHeaderGroups()[0]?.headers ?? [])

const totalSize = computed(() =>
  table.getVisibleLeafColumns().reduce((sum, column) => sum + column.getSize(), 0),
)

function toColumnDef(column: DataColumn) {
  return {
    id: column.id,
    header: column.header,
    // The row's own field, which is also the cell text when the caller draws no
    // slot for it. A column without one is a display column, and those cannot
    // sort — so every column reads its field and the ones that sort say so.
    accessorFn: (row: Row) => (row as Record<string, unknown>)[column.id],
    size: column.size,
    minSize: column.minSize ?? COLUMN_MIN_SIZE,
    maxSize: column.maxSize ?? COLUMN_MAX_SIZE,
    enableSorting: column.enableSorting ?? false,
    sortDescFirst: column.sortDescFirst,
  }
}

function labelOf(columnId: string): string {
  return columnsById.value.get(columnId)?.header ?? ''
}

function ariaSort(columnId: string): 'ascending' | 'descending' | 'none' {
  if (props.sorting?.key !== columnId) return 'none'
  return props.sorting.order === 'desc' ? 'descending' : 'ascending'
}

function applySort(key: string, order: 'asc' | 'desc'): void {
  emit('update:sorting', { key, order })
}

/**
 * Give every column its starting width: the declared one where the design
 * declares one, and the room the others leave to the ones that do not. A table
 * of nothing but fixed columns is stretched to the container instead, which is
 * what the browser would do to the columns anyway.
 *
 * A container with no width is a table mounted somewhere not laid out yet (a tab
 * nobody opened). Nothing is measured there, so nothing is pinned: the widths
 * stay the browser's to distribute, and the table is left as it would have been
 * drawn by hand.
 */
function layOut(): void {
  const container = shell.value?.$el
  const available = container?.clientWidth ?? 0
  if (available <= 0) return

  const fixed = props.columns.filter((column) => column.size !== undefined)
  const flexible = props.columns.filter((column) => column.size === undefined)
  const fixedTotal = fixed.reduce((sum, column) => sum + (column.size ?? 0), 0)

  const next: ColumnSizingState = {}
  if (flexible.length > 0) {
    const each = Math.max(COLUMN_MIN_SIZE, Math.floor((available - fixedTotal) / flexible.length))
    for (const column of props.columns) next[column.id] = column.size ?? each
  } else {
    const grow = fixedTotal > 0 && available > fixedTotal ? available / fixedTotal : 1
    for (const column of props.columns) next[column.id] = Math.round((column.size ?? 0) * grow)
  }
  sizing.value = next
}

onMounted(layOut)

/** The width a double-click fits a column to: its widest cell, clamped. */
function autofit(columnId: string): void {
  const container = shell.value?.$el
  if (!container) return
  const index = table.getVisibleLeafColumns().findIndex((column) => column.id === columnId)
  if (index < 0) return

  const cells: HTMLElement[] = []
  for (const row of container.querySelectorAll<HTMLElement>('tbody tr')) {
    const cell = row.children[index]
    if (cell instanceof HTMLElement) cells.push(cell)
  }
  const definition = columnsById.value.get(columnId)

  table.setColumnSizing((previous) => ({
    ...previous,
    [columnId]: contentWidth(cells.map(wantedWidth), {
      padding: horizontalPadding(cells[0]),
      min: definition?.minSize ?? COLUMN_MIN_SIZE,
      max: definition?.maxSize ?? COLUMN_MAX_SIZE,
    }),
  }))
}

/**
 * The width one cell's content wants on a single line, in px, without the cell
 * padding.
 *
 * Not `scrollWidth`: a block that fits reports its own box there, and a block
 * that truncates (`.forebrain-cell-1`) still reports its box — so a column would
 * be fitted to the width it already had. Content geometry is the honest source:
 * a text node is measured by the rects of a range over it, and an element is
 * measured by its own box only when it holds no text at all (an icon button) —
 * an element that holds text is as wide as the column, and its text is what the
 * walk below measures.
 */
function wantedWidth(cell: HTMLElement): number {
  const style = getComputedStyle(cell)
  const origin = cell.getBoundingClientRect().left + (Number.parseFloat(style.paddingLeft) || 0)
  let right = origin

  const walker = document.createTreeWalker(cell, NodeFilter.SHOW_ELEMENT | NodeFilter.SHOW_TEXT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    if (node.nodeType === Node.TEXT_NODE) {
      for (const rect of textRects(node)) right = Math.max(right, rect.right)
      continue
    }
    const element = node as HTMLElement
    if ((element.textContent ?? '').trim() !== '') continue
    right = Math.max(right, element.getBoundingClientRect().right)
  }
  return right - origin
}

/**
 * The rects a text node occupies. A range is the only way to ask a text node
 * how wide it is — its parent's box answers with the column's own width. jsdom,
 * where the unit tests mount this table, lays nothing out and gives a range no
 * rects at all; zero is then the honest answer, and the e2e suite is what
 * checks the pixels.
 */
function textRects(node: Node): DOMRect[] {
  const range = document.createRange()
  range.selectNodeContents(node)
  return typeof range.getClientRects === 'function' ? Array.from(range.getClientRects()) : []
}

/** The padding a cell adds on both sides, which the content width excludes. */
function horizontalPadding(cell: HTMLElement | undefined): number {
  if (!cell) return 0
  const style = getComputedStyle(cell)
  return (Number.parseFloat(style.paddingLeft) || 0) + (Number.parseFloat(style.paddingRight) || 0)
}
</script>
