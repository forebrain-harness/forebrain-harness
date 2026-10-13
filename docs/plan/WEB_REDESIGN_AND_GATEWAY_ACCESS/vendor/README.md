# 设计稿用的编辑器包

`app-preview.html` 里的文件编辑器直接用真实组件：Milkdown Crepe 7.22.3（MIT）编辑 Markdown，CodeMirror 6（MIT）编辑源码和代码文件。

- `fb-editor*.js`：由 `build/` 打出的包（esbuild，ESM + 按需加载的代码语言），可随时重建，不必提交。
- 打成本地包而不是从 jsDelivr 逐个加载：jsDelivr 把 `@milkdown/kit` 的每个子路径单独打包，ProseMirror 会被加载两份，编辑器报 "Adding different instances of a keyed plugin"。
- 代码高亮统一用 Monokai Light（`build/highlight.js`），不随主题色、菜单栏深浅变化；颜色取自 TUI 用的 chroma `monokailight`（`pkg/tui/render.go` 的 `codeStyleLight`），与网页端现有的 `@repo/elements/code-block` 同一套。Crepe 代码块、Markdown 源码、代码文件三处共用。
- 代码语言只带一份常用清单（`build/langs.js`：Bash、Go、JSON、YAML、TypeScript、JavaScript、Python、SQL、Markdown、Diff），打包时替换掉 `@codemirror/language-data` 的全量清单。

重建：

```bash
cd build && npm install && ./node_modules/.bin/esbuild entry.js --bundle --format=esm --splitting --minify \
  --outdir=.. '--entry-names=fb-editor' '--chunk-names=fb-editor-[hash]' --legal-comments=eof \
  '--alias:@codemirror/language-data=./langs.js' '--define:process.env.NODE_ENV="production"' \
  --define:__VUE_OPTIONS_API__=false --define:__VUE_PROD_DEVTOOLS__=false --define:__VUE_PROD_HYDRATION_MISMATCH_DETAILS__=false
```

许可证（2026-10-11 按 `node_modules` 逐个核对）：209 个 MIT；dompurify 为 MPL-2.0 或 Apache-2.0 双许可，按 Apache-2.0 使用；entities（BSD-2-Clause）、picocolors（ISC）、source-map-js（BSD-3-Clause）。均可商用。
