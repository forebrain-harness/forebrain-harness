// What a finished turn delivered, and the one thing worth doing with it:
// opening it in the editor.
//
// A delivery is refused unless it is inside a workspace folder — the paths come
// from the agent, and the editor is not a file manager. The decision itself is
// deliverableGate.ts, which is pure and tested; this is the thin part that
// draws it.
import * as vscode from 'vscode';
import { openableDeliverables } from './deliverableGate';
import type { DeliveredFile, SessionState } from './sessionState';

/** One row of `GET /api/chat/sessions/<id>/artifacts`. */
interface Artifact {
  readonly path: string;
  readonly title?: string;
  readonly name?: string;
}

export interface DeliverableDeps {
  readonly client: () => { getJson<T>(path: string, token: string): Promise<T> };
  readonly token: () => string;
  readonly boundSession: () => string | undefined;
  readonly log: (line: string) => void;
}

export class Deliverables {
  /** The last run whose delivery was announced: a replay after a reconnect must not announce it twice. */
  private announced = '';

  constructor(private readonly deps: DeliverableDeps) {}

  onRunEnd(previous: SessionState, next: SessionState): void {
    const delivery = next.lastDelivery;
    if (!delivery || delivery === previous.lastDelivery || delivery.runId === this.announced) return;
    this.announced = delivery.runId;
    void this.announce(delivery.files);
  }

  /** `Forebrain: Open Delivered File`: what the last fifty artifacts of the session say. */
  async openDeliveredFile(): Promise<void> {
    const session = this.deps.boundSession();
    const token = this.deps.token();
    if (!session || !token) {
      void vscode.window.showInformationMessage('Forebrain: open the panel and let it load a conversation first.');
      return;
    }
    let artifacts: readonly Artifact[];
    try {
      const answer = await this.deps
        .client()
        .getJson<{ readonly items?: readonly Artifact[] }>(
          `/api/chat/sessions/${encodeURIComponent(session)}/artifacts?limit=50`,
          token,
        );
      artifacts = answer.items ?? [];
    } catch (err) {
      void vscode.window.showErrorMessage(
        `Forebrain could not list the session's files: ${err instanceof Error ? err.message : String(err)}`,
      );
      return;
    }
    if (artifacts.length === 0) {
      void vscode.window.showInformationMessage('Forebrain: this session has not delivered a file.');
      return;
    }
    const picked = await vscode.window.showQuickPick(
      artifacts.map((artifact) => ({
        label: artifact.title || artifact.name || artifact.path,
        description: artifact.path,
        path: artifact.path,
      })),
      { title: 'Delivered files' },
    );
    if (picked) await this.open(picked.path);
  }

  private async announce(files: readonly DeliveredFile[]): Promise<void> {
    const choice = await vscode.window.showInformationMessage(
      `Forebrain delivered ${files.length} file(s)`,
      'Open…',
      'Reveal',
    );
    if (choice === 'Open…') {
      const picked = await vscode.window.showQuickPick(
        files.map((file) => ({ label: file.title || file.path, description: file.path, path: file.path })),
        { title: 'Delivered files' },
      );
      if (picked) await this.open(picked.path);
      return;
    }
    if (choice === 'Reveal') {
      const [openable] = this.openable(files);
      if (openable) await vscode.commands.executeCommand('revealInExplorer', vscode.Uri.file(openable.path));
    }
  }

  private openable(files: readonly DeliveredFile[]): readonly DeliveredFile[] {
    const roots = (vscode.workspace.workspaceFolders ?? []).map((folder) => folder.uri.fsPath);
    return openableDeliverables(files, roots);
  }

  private async open(path: string): Promise<void> {
    if (this.openable([{ path, title: '' }]).length === 0) {
      void vscode.window.showInformationMessage(`Forebrain: this delivery is outside the workspace: ${path}`);
      return;
    }
    await vscode.window.showTextDocument(vscode.Uri.file(path), { preview: false });
  }
}