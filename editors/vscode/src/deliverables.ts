// What a finished turn delivered, and the one thing worth doing with it:
// opening it in the editor.
//
// A delivery is refused unless it is inside a workspace folder — the paths come
// from the agent, and the editor is not a file manager. The decision itself is
// deliverableGate.ts, which is pure and tested; this is the thin part that
// draws it.
import * as vscode from 'vscode';
import { deliverableOpenKind, openableDeliverables } from './deliverableGate';
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
      void vscode.window.showInformationMessage(vscode.l10n.t('Forebrain: open the panel and let it load a conversation first.'));
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
        vscode.l10n.t("Forebrain could not list the session's files: {0}", err instanceof Error ? err.message : String(err)),
      );
      return;
    }
    if (artifacts.length === 0) {
      void vscode.window.showInformationMessage(vscode.l10n.t('Forebrain: this session has not delivered a file.'));
      return;
    }
    const picked = await vscode.window.showQuickPick(
      artifacts.map((artifact) => ({
        label: artifact.title || artifact.name || artifact.path,
        description: artifact.path,
        path: artifact.path,
      })),
      { title: vscode.l10n.t('Delivered files') },
    );
    if (picked) await this.open(picked.path);
  }

  private async announce(files: readonly DeliveredFile[]): Promise<void> {
    const open = vscode.l10n.t('Open…');
    const reveal = vscode.l10n.t('Reveal');
    const choice = await vscode.window.showInformationMessage(
      vscode.l10n.t('Forebrain delivered {0} file(s)', files.length),
      open,
      reveal,
    );
    if (choice === open) {
      const picked = await vscode.window.showQuickPick(
        files.map((file) => ({ label: file.title || file.path, description: file.path, path: file.path })),
        { title: vscode.l10n.t('Delivered files') },
      );
      if (picked) await this.open(picked.path);
      return;
    }
    if (choice === reveal) {
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
      void vscode.window.showInformationMessage(vscode.l10n.t('Forebrain: this delivery is outside the workspace: {0}', path));
      return;
    }
    const uri = vscode.Uri.file(path);
    switch (deliverableOpenKind(path)) {
      case 'image':
        await vscode.commands.executeCommand('vscode.open', uri);
        return;
      case 'markdown':
        await vscode.commands.executeCommand('markdown.showPreview', uri);
        return;
      case 'external':
        await vscode.env.openExternal(uri);
        return;
      default:
        await vscode.window.showTextDocument(uri, { preview: false });
    }
  }
}