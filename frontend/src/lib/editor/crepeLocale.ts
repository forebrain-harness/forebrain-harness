import type { LanguageDescription } from '@codemirror/language'
import { CrepeFeature, type CrepeConfig } from '@milkdown/crepe'

import type { Locale } from '@/locales'

/**
 * The Chinese copy for Crepe's own chrome — top bar, code-block language
 * picker, slash-menu groups and the empty-document placeholder. The library
 * ships these strings in English and lets a caller override them per feature;
 * there is no locale pack, so the translations live here, in one place, and the
 * editor reads them from `featureConfigs`.
 *
 * Everything Crepe does not let us rename (its accessibility labels, the
 * language names) stays as the library ships it — see the plan's STOP
 * condition 2. Keep the labels short: they sit in a compact toolbar.
 */
const zh = {
  top: {
    headingOptions: [
      { label: '正文', level: null as number | null },
      { label: '标题 1', level: 1 as number | null },
      { label: '标题 2', level: 2 as number | null },
      { label: '标题 3', level: 3 as number | null },
    ],
  },
  code: {
    searchPlaceholder: '搜索语言',
    noResultText: '没有匹配的语言',
    copyText: '复制',
    previewToggleText: (previewOnly: boolean) => (previewOnly ? '编辑' : '预览'),
  },
  block: {
    textGroup: {
      label: '文本',
      text: { label: '正文' },
      h1: { label: '标题 1' },
      h2: { label: '标题 2' },
      h3: { label: '标题 3' },
      quote: { label: '引用' },
      divider: { label: '分割线' },
    },
    listGroup: {
      label: '列表',
      bulletList: { label: '无序列表' },
      orderedList: { label: '有序列表' },
      taskList: { label: '任务列表' },
    },
    advancedGroup: {
      label: '插入',
      codeBlock: { label: '代码块' },
      table: { label: '表格' },
    },
  },
  placeholder: { text: '输入内容，/ 插入块' },
}

/** Crepe's own feature-config bag, named off the constructor's own type. */
type FeatureConfigs = NonNullable<CrepeConfig['featureConfigs']>

/** The highlight extension a code block is painted with. */
type CodeTheme = NonNullable<FeatureConfigs[CrepeFeature.CodeMirror]>['theme']

/**
 * Build the `featureConfigs` Crepe is constructed with: the localized text for
 * the current UI language, plus the code languages and Monokai Light highlight
 * every code block uses. English returns no text override at all, so the
 * library's own defaults show through.
 */
export function crepeFeatureConfigs(
  locale: Locale,
  code: { languages: readonly LanguageDescription[]; theme: CodeTheme },
): FeatureConfigs {
  const configs: FeatureConfigs = {
    [CrepeFeature.CodeMirror]: {
      languages: [...code.languages],
      theme: code.theme,
      previewToggleText: zh.code.previewToggleText,
      ...(locale === 'zh'
        ? {
            searchPlaceholder: zh.code.searchPlaceholder,
            noResultText: zh.code.noResultText,
            copyText: zh.code.copyText,
          }
        : {}),
    },
  }
  if (locale === 'zh') {
    configs[CrepeFeature.TopBar] = { headingOptions: zh.top.headingOptions }
    configs[CrepeFeature.BlockEdit] = zh.block
    configs[CrepeFeature.Placeholder] = { text: zh.placeholder.text }
  }
  return configs
}

/**
 * The features the editor turns on: the top bar and the default set, minus the
 * three that need a backend or a heavier bundle (AI edits, LaTeX, image
 * uploads).
 */
export function crepeFeatures(): CrepeConfig['features'] {
  return {
    [CrepeFeature.TopBar]: true,
    [CrepeFeature.AI]: false,
    [CrepeFeature.Latex]: false,
    [CrepeFeature.ImageBlock]: false,
  }
}
