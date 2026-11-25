// The text a selection or a file turns into before it is sent as a turn.
//
// Plain strings, no `vscode`: what the editor hands over is already extracted
// into these shapes, so the arrangement is testable on its own and the command
// layer only reads the editor.

/** Lines are 1-based, as a person reads them in the editor's gutter. */
export interface SelectionContext {
  readonly fsPath: string;
  readonly relPath: string;
  readonly languageId: string;
  readonly startLine: number;
  readonly endLine: number;
  readonly selection: string;
}

export interface FileContext {
  readonly fsPath: string;
  readonly relPath: string;
  readonly languageId: string;
  readonly content: string;
}

export interface DiagnosticLine {
  readonly line: number;
  readonly severity: string;
  readonly source: string;
  readonly message: string;
}

/** The shape of `vscode.Diagnostic` this module reads; `severity` is 0 Error … 3 Hint. */
export interface DiagnosticLike {
  readonly range: { readonly start: { readonly line: number } };
  readonly severity: number;
  readonly source?: string | undefined;
  readonly message: string;
}

export const FILE_CONTEXT_LIMIT = 64 * 1024;

const SEVERITY = ['ERROR', 'WARNING', 'INFO', 'HINT'] as const;

/** Block order: the header, the code, the diagnostics, the question. */
function prompt(blocks: readonly string[]): string {
  return blocks.filter((block) => block !== '').join('\n\n');
}

function fenced(languageId: string, body: string): string {
  return `\`\`\`${languageId}\n${body}\n\`\`\``;
}

function label(fsPath: string, relPath: string): string {
  // No workspace folder means no relative path: the absolute one is all there is.
  return relPath || fsPath;
}

function diagnosticsBlock(path: string, diagnostics: readonly DiagnosticLine[]): string {
  if (diagnostics.length === 0) return '';
  const lines = diagnostics.map(
    (d) => `- [${d.severity}] line ${d.line}: ${d.message}${d.source ? ` (${d.source})` : ''}`,
  );
  return [`Diagnostics reported in ${path}:`, ...lines].join('\n');
}

export function buildSelectionPrompt(
  sel: SelectionContext,
  question: string,
  diagnostics: readonly DiagnosticLine[],
): string {
  const path = label(sel.fsPath, sel.relPath);
  return prompt([
    `In ${path}:${sel.startLine}-${sel.endLine}:`,
    fenced(sel.languageId, sel.selection),
    diagnosticsBlock(path, diagnostics),
    question.trim(),
  ]);
}

export function buildFilePrompt(
  file: FileContext,
  question: string,
  diagnostics: readonly DiagnosticLine[],
): string {
  const path = label(file.fsPath, file.relPath);
  const truncated = file.content.length > FILE_CONTEXT_LIMIT;
  return prompt([
    `File ${path}:`,
    fenced(file.languageId, truncated ? file.content.slice(0, FILE_CONTEXT_LIMIT) : file.content),
    truncated ? `(${path} truncated at 64 KiB; use your file tools to read the rest)` : '',
    diagnosticsBlock(path, diagnostics),
    question.trim(),
  ]);
}

/**
 * The editor's diagnostics as prompt lines: severity spelled out, the 0-based
 * range start made 1-based, sorted by line with the most severe first on a
 * line.
 */
export function diagnosticsOf(entries: readonly DiagnosticLike[]): DiagnosticLine[] {
  return [...entries]
    .sort((a, b) => a.range.start.line - b.range.start.line || a.severity - b.severity)
    .map((entry) => ({
      line: entry.range.start.line + 1,
      severity: SEVERITY[entry.severity] ?? 'INFO',
      source: entry.source ?? '',
      message: entry.message,
    }));
}