# Bundled tokenizer files

Local token estimation counts tokens with the tokenizer of the model in use,
not with one global tiktoken encoder. The tokenizer files for the mainstream
model families ship with forebrain — committed here, not embedded in the
binary — and are installed beside the binary (like `models.json`; see
`pkg/llm/assets.go`). On first use the runtime copies them from beside the
binary into the cache directory, and only downloads one when it is missing
both there and in the cache.

The table in `pkg/llm/tokenizers.go` (`builtinTokenizers`) is the single source
of truth for the ids, file names, checksums and sizes. This document mirrors
it; `TestBundledTokenizerFilesMatchTable` fails if the two drift apart.

## Files

Each id is a directory holding the tokenizer file(s) gzipped, plus the
source repository's `LICENSE`:

| id | file | sha256 (decompressed) | bytes | source | license | covers (model-name prefix, case-insensitive) |
|---|---|---|---|---|---|---|
| `deepseek-v4` | `tokenizer.json` | `c90dfa01249db1be4245780a052ede752e1361c612ac6d08e2bdada7d599476b` | 6367257 | deepseek-ai/DeepSeek-V4.1-Flash | MIT | `deepseek` (V3.2/V4/V4.1 share the same 128000-token vocabulary) |
| `glm-5` | `tokenizer.json` | `19e773648cb4e65de8660ea6365e10acca112d42a854923df93db4a6f333a82d` | 20217442 | zai-org/GLM-5.3 | GLM-5.3 License | `glm` (GLM-4.7's vocabulary is a subset) |
| `gpt-o200k` | `tokenizer.json` | `0614fe83cadab421296e664e1f48f4261fa8fef6e03e63bb75c20f38e37d07d3` | 27868174 | openai/gpt-oss-120b | Apache-2.0 | `gpt`, `chatgpt`, `o1`, `o3`, `o4`, `codex`; also the default |
| `kimi-k3` | `tiktoken.model` | `b6c497a7469b33ced9c38afb1ad6e47f03f5e5dc05f15930799210ec050c5103` | 2795286 | moonshotai/Kimi-K3 | Kimi K3 License | `kimi`, `moonshot` |
| `kimi-k3` | `tokenizer_config.json` | `5d0803c94db9cd78763499e0956c95fd5a225c14a727e5a6cf5db3f96f010a6e` | 3478 | moonshotai/Kimi-K3 | Kimi K3 License | (config the tiktoken loader needs) |
| `qwen-3.8` | `tokenizer.json` | `0997f410c57a1f4e53b09e4be8f4a172d90edd9564368fb0847030937229b9f3` | 12809320 | Qwen/Qwen3.8-27B | Apache-2.0 | `qwen`, `qwq`, `deepseek-r1-distill-qwen` (the last takes precedence over `deepseek`) |
| `minimax-m2` | `tokenizer.json` | `757622126525aeeb131756849d93298070ff3f0319c455ec8c5bb0f6b1cebbe8` | 9730160 | MiniMaxAI/MiniMax-M2 | MIT (with a prominent-attribution clause above 100M MAU / $30M ARR) | `minimax` |
| `mimo-v2.5` | `tokenizer.json` | `633518aad78f9f61bae2ae420d621215754a4424c918b052cd8c22a3b59e99d2` | 7033572 | XiaomiMiMo/MiMo-V2.5 | MIT | `mimo` |
| `gemma-4` | `tokenizer.json` | `cc8d3a0ce36466ccc1278bf987df5f71db1719b9ca6b4118264f45cb627bfe0f` | 32169626 | google/gemma-4-31b-it | Apache-2.0 | `gemini`, `gemma` (Gemma 4 is the approximation for Gemini, which has no public tokenizer) |
| `claude-legacy` | `tokenizer.json` | `c241737df24b4e7f7c9af4fdcee29a0ca903dcb288a8b753bc346a3092911767` | 1774213 | Xenova/claude-tokenizer | MIT | `claude` (Claude 2-era tokenizer, an approximation for current Claude) |
| `grok-2` | `tokenizer.json` | `63e538a53de71acf0585603a7f1f62cc66c5e6fa5763d753d10ad071f5c5d4fe` | 17963143 | alvarobartt/grok-2-tokenizer | xAI Community License (Grok 2) | `grok` (HuggingFace conversion of the official `xai-org/grok-2` tokenizer) |

When a model name matches no prefix the provider decides:
`deepseek`→`deepseek-v4`, `zhipuai`/`zai`→`glm-5`, `openai`→`gpt-o200k`,
`moonshotai`→`kimi-k3`, `alibaba`/`dashscope`→`qwen-3.8`,
`minimax`→`minimax-m2`, `xiaomi`→`mimo-v2.5`, `google`→`gemma-4`,
`anthropic`→`claude-legacy`, `xai`→`grok-2`. With neither a prefix nor a
provider match the default (`gpt-o200k`, or `token_estimate.default_tokenizer`)
is used.

Claude, Grok and Gemini have no contemporary public tokenizer, so their
entries are approximations (`claude-legacy`, `grok-2`, `gemma-4`). The
authoritative signal for compaction is the provider's reported native usage
(see the anchor rule), not this local estimate.

## How to update

1. Download the new file from the source repository
   (`https://huggingface.co/<repo>/resolve/main/<file>`, or ModelScope
   `https://modelscope.cn/models/<repo>/resolve/master/<file>`; the two hubs
   serve byte-identical files).
2. Check its size and `shasum -a 256` against the values you will record.
3. Compress it deterministically:
   `gzip -9 -n -c <file> > assets/tokenizers/<id>/<file>.gz`.
4. Add or edit the entry in `pkg/llm/tokenizers.go` and this table.
5. Run `go test ./pkg/llm/ -run TestBundledTokenizerFilesMatchTable`.

A new id gets a new directory name (e.g. `deepseek-v5`); never change the
content of an existing id. The cache validates files by checksum, so changing
a file's bytes makes every user re-extract or re-download it.

## Offline and intranet use

The cache directory is `$FOREBRAIN_HOME/cache/tokenizers/<id>/<file>`, holding
the decompressed original files. To use forebrain on a machine without
internet access, run forebrain once on a connected machine (or unpack the
release archive's `tokenizers/*.gz` and `gzip -d` them), then copy the whole
`cache/tokenizers` directory to the same location on the offline machine.
