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
source repository's `LICENSE`. Six ids — `gpt-o200k`, `glm-5`, `grok-2`,
`qwen-3.8`, `minimax-m2`, `mimo-v2.5` — ship in the tiktoken format, converted
from their source repository's `tokenizer.json` by `cmd/tokenizer2tiktoken`
(see [Why the tiktoken format](#why-the-tiktoken-format)); `kimi-k3` is
published in that format upstream:

| id | file | sha256 (decompressed) | bytes | source | license | covers (model-name prefix, case-insensitive) |
|---|---|---|---|---|---|---|
| `deepseek-v4` | `tokenizer.json` | `c90dfa01249db1be4245780a052ede752e1361c612ac6d08e2bdada7d599476b` | 6367257 | deepseek-ai/DeepSeek-V4.1-Flash | MIT | `deepseek` (V3.2/V4/V4.1 share the same 128000-token vocabulary) |
| `glm-5` | `tiktoken.model` | `d2a312b6d9fa24fc27bdea3387e65477c427214e2fb2372e5f3ae980ffaa3e1d` | 2668918 | zai-org/GLM-5.3 (converted) | GLM-5.3 License | `glm` (GLM-4.7's vocabulary is a subset) |
| `glm-5` | `tokenizer_config.json` | `8194ce3f0a41cda2f33d6f42edd0624d749479ad7232999e2e32a8cc7f415eaf` | 4828 | zai-org/GLM-5.3 (converted) | GLM-5.3 License | (config the tiktoken loader needs) |
| `gpt-o200k` | `tiktoken.model` | `446a9538cb6c348e3516120d7c08b09f57c36495e2acfffe59a5bf8b0cfb1a2d` | 3613922 | openai/gpt-oss-120b (converted) | Apache-2.0 | `gpt`, `chatgpt`, `o1`, `o3`, `o4`, `codex`; also the default |
| `gpt-o200k` | `tokenizer_config.json` | `de20240e59ed07d81101b31e4fb8217fc9d51f3f9995b239e3c345b9532603b6` | 2970 | openai/gpt-oss-120b (converted) | Apache-2.0 | (config the tiktoken loader needs) |
| `kimi-k3` | `tiktoken.model` | `b6c497a7469b33ced9c38afb1ad6e47f03f5e5dc05f15930799210ec050c5103` | 2795286 | moonshotai/Kimi-K3 | Kimi K3 License | `kimi`, `moonshot` |
| `kimi-k3` | `tokenizer_config.json` | `5d0803c94db9cd78763499e0956c95fd5a225c14a727e5a6cf5db3f96f010a6e` | 3478 | moonshotai/Kimi-K3 | Kimi K3 License | (config the tiktoken loader needs) |
| `qwen-3.8` | `tiktoken.model` | `8dde380a6405e935f5de16a99eb61c824f3f814dd1ed298784c72babb7a03cdd` | 4579414 | Qwen/Qwen3.8-27B (converted) | Apache-2.0 | `qwen`, `qwq`, `deepseek-r1-distill-qwen` (the last takes precedence over `deepseek`) |
| `qwen-3.8` | `tokenizer_config.json` | `600986e338cd22803b092bf870e6d892f6f75f8937d8bd991d79f300bc0870b3` | 4500 | Qwen/Qwen3.8-27B (converted) | Apache-2.0 | (config the tiktoken loader needs) |
| `minimax-m2` | `tiktoken.model` | `9b423908eab5445f88a72b26a283d848da80884fde9e0b8e5e7a4fe495313f1e` | 3557010 | MiniMaxAI/MiniMax-M2 (converted) | MIT (with a prominent-attribution clause above 100M MAU / $30M ARR) | `minimax` |
| `minimax-m2` | `tokenizer_config.json` | `49033b681c5c96f618b21424e4f44b43f3332a73559134df0378497493b95d9a` | 7158 | MiniMaxAI/MiniMax-M2 (converted) | MIT (as above) | (config the tiktoken loader needs) |
| `mimo-v2.5` | `tiktoken.model` | `b2b1b8dfb5cc5f024bafc373121c6aba3f66f9a5a0269e243470a1de16a33186` | 2561218 | XiaomiMiMo/MiMo-V2.5 (converted) | MIT | `mimo` |
| `mimo-v2.5` | `tokenizer_config.json` | `27ede6bfed52a9c749977b9859c1394b339bae2f0ea6661958c92ad47a771504` | 4390 | XiaomiMiMo/MiMo-V2.5 (converted) | MIT | (config the tiktoken loader needs) |
| `gemma-4` | `tokenizer.json` | `cc8d3a0ce36466ccc1278bf987df5f71db1719b9ca6b4118264f45cb627bfe0f` | 32169626 | google/gemma-4-31b-it | Apache-2.0 | `gemini`, `gemma` (Gemma 4 is the approximation for Gemini, which has no public tokenizer) |
| `claude-legacy` | `tokenizer.json` | `c241737df24b4e7f7c9af4fdcee29a0ca903dcb288a8b753bc346a3092911767` | 1774213 | Xenova/claude-tokenizer | MIT | `claude` (Claude 2-era tokenizer, an approximation for current Claude) |
| `grok-2` | `tiktoken.model` | `fa70b860aeca864eab97639d1f9912025a3868cf848185032d4b1c83deae5b92` | 2208402 | alvarobartt/grok-2-tokenizer (converted) | xAI Community License (Grok 2) | `grok` (HuggingFace conversion of the official `xai-org/grok-2` tokenizer) |
| `grok-2` | `tokenizer_config.json` | `e287a7330cc08821c42e05c2c54b9309ea624febccbeb3633c6917234001cd3d` | 16434 | alvarobartt/grok-2-tokenizer (converted) | xAI Community License (Grok 2) | (config the tiktoken loader needs) |

The source `tokenizer.json` each converted id is generated from (the `Source`
of its `builtinTokenizers` entry). The hubs only serve this file, so when an
id is missing from both the cache and beside the binary, forebrain downloads
it, checks its size and sha256, converts it locally and caches the converted
files only if they match the table above:

| id | source `tokenizer.json` sha256 | bytes | HuggingFace | ModelScope |
|---|---|---|---|---|
| `gpt-o200k` | `0614fe83cadab421296e664e1f48f4261fa8fef6e03e63bb75c20f38e37d07d3` | 27868174 | openai/gpt-oss-120b | openai-mirror/gpt-oss-120b |
| `glm-5` | `19e773648cb4e65de8660ea6365e10acca112d42a854923df93db4a6f333a82d` | 20217442 | zai-org/GLM-5.3 | ZhipuAI/GLM-5.3 |
| `grok-2` | `63e538a53de71acf0585603a7f1f62cc66c5e6fa5763d753d10ad071f5c5d4fe` | 17963143 | alvarobartt/grok-2-tokenizer | — |
| `qwen-3.8` | `0997f410c57a1f4e53b09e4be8f4a172d90edd9564368fb0847030937229b9f3` | 12809320 | Qwen/Qwen3.8-27B | Qwen/Qwen3.8-27B |
| `minimax-m2` | `757622126525aeeb131756849d93298070ff3f0319c455ec8c5bb0f6b1cebbe8` | 9730160 | MiniMaxAI/MiniMax-M2 | MiniMax/MiniMax-M2 |
| `mimo-v2.5` | `633518aad78f9f61bae2ae420d621215754a4424c918b052cd8c22a3b59e99d2` | 7033572 | XiaomiMiMo/MiMo-V2.5 | XiaomiMiMo/MiMo-V2.5 |

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

## Why the tiktoken format

Loading a `tokenizer.json` through the tokenizers library keeps the whole
HuggingFace object graph (vocabulary, merges, pre-tokenizers) resident; the
same vocabulary as a tiktoken rank table (byte sequence → rank) is a fraction
of that, and counts about three times faster. The default `gpt-o200k` is
loaded by nearly every process, so this is most of forebrain's resident
memory. Measured per id in its own process (resident memory added by the
load), and token counts compared file by file over the repository's 1268
`.go`/`.md`/`.ts`/`.vue` files (about 6.3–6.7 million tokens):

| id | `tokenizer.json` | tiktoken | files that differ | total difference (tokens) |
|---|---|---|---|---|
| `gpt-o200k` | +340 MB | +57 MB | 0 | 0 |
| `grok-2` | +239 MB | +39 MB | 0 | 0 |
| `glm-5` | +251 MB | +46 MB | 4 | 37 |
| `qwen-3.8` | +158 MB | +83 MB | 3 | 17 |
| `minimax-m2` | +112 MB | +55 MB | 3 | 3 |
| `mimo-v2.5` | +92 MB | +51 MB | 3 | 17 |

Every difference (under 0.001%, negligible for an estimate) comes from an
added token's text — `<tool_call>`, `<arg_key>`, `<filename>`,
`<|endoftext|>`, … — appearing in the counted text: `tokenizer.json` matches it
as one special token, while the tiktoken form (counting with special tokens
off) encodes it as ordinary text, a few tokens more. With those strings
replaced, the two forms count every differing file the same.

The conversion is faithful only for a byte-level BPE with contiguous ids whose
pre-tokenizer is exactly one regex `Split` (keeping the matches) followed by
`ByteLevel(use_regex=false)`; `ConvertTokenizerJSONToTiktoken` refuses any
other shape. The other three keep their `tokenizer.json`:

- `deepseek-v4` splits with three regexes in sequence (digit runs, CJK runs,
  then the general pattern), which one tiktoken pattern cannot reproduce.
- `claude-legacy` normalizes with NFKC (tiktoken does not normalize) and
  splits with `ByteLevel`'s own built-in regex, with no `Split`.
- `gemma-4` is a SentencePiece-style BPE (spaces become `▁`, unknown
  characters fall back to bytes with `byte_fallback`), not a byte-level BPE.

## How to update

For a converted id (one with a `Source`):

1. Download the source `tokenizer.json` from its repository
   (`https://huggingface.co/<repo>/resolve/main/tokenizer.json`, or ModelScope
   `https://modelscope.cn/models/<repo>/resolve/master/tokenizer.json`).
2. Check its size and `shasum -a 256`; they become the entry's `Source`.
3. Convert it: `go run ./cmd/tokenizer2tiktoken <tokenizer.json> <out-dir>`.
   It prints the split pattern and each output file's sha256 and size.
4. Compress both outputs deterministically:
   `gzip -9 -n -c <out-dir>/tiktoken.model > assets/tokenizers/<id>/tiktoken.model.gz`,
   and the same for `tokenizer_config.json`.
5. Edit the entry in `pkg/llm/tokenizers.go` — `Files` and `Source` together
   (the download fallback converts the source and checks the result against
   `Files`, so the two must change in step), and `Pattern` if it changed — and
   both tables here.
6. Run `go test ./pkg/llm/`.

For any other id:

1. Download the new file from the source repository
   (`https://huggingface.co/<repo>/resolve/main/<file>`, or ModelScope
   `https://modelscope.cn/models/<repo>/resolve/master/<file>`; the two hubs
   serve byte-identical files).
2. Check its size and `shasum -a 256` against the values you will record.
3. Compress it deterministically:
   `gzip -9 -n -c <file> > assets/tokenizers/<id>/<file>.gz`.
4. Add or edit the entry in `pkg/llm/tokenizers.go` and this table.
5. Run `go test ./pkg/llm/ -run TestBundledTokenizerFilesMatchTable`.

A new family whose `tokenizer.json` has the convertible shape above should be
converted; otherwise it keeps its `tokenizer.json` (and the higher memory).

A new id gets a new directory name (e.g. `deepseek-v5`); never change the
content of an existing id. The cache validates files by checksum, so changing
a file's bytes makes every user re-extract or re-download it.

## Offline and intranet use

The cache directory is `$FOREBRAIN_HOME/cache/tokenizers/<id>/<file>`, holding
the decompressed files listed in the first table (the tiktoken files for the
converted ids; a `tokenizer.json` an older version cached for one of them is
removed). To use forebrain on a machine without
internet access, run forebrain once on a connected machine (or unpack the
release archive's `tokenizers/*.gz` and `gzip -d` them), then copy the whole
`cache/tokenizers` directory to the same location on the offline machine, and
set `agents.defaults.token_estimate.download: false` there so a tokenizer
missing from it is never fetched (an empty `hubs` list does not turn
downloading off; it means HuggingFace then ModelScope).
