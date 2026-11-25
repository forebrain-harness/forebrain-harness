// The checklist tree: a read-only mirror of the agent's own `session_todo`
// list, folded out of the session state by sessionState.ts.
//
// Read-only on purpose: the items belong to the agent, and a tree the user can
// tick would fight the next `session_todo` call for the truth.
import * as vscode from 'vscode';
import type { Checklist, PlanItem, SessionState } from './sessionState';
import { EMPTY_SESSION_STATE } from './sessionState';

export type ChecklistRow =
  | { readonly kind: 'checklist'; readonly checklist: Checklist }
  | { readonly kind: 'item'; readonly checklist: Checklist; readonly item: PlanItem };

const ICONS: Record<string, string> = {
  completed: 'check-all',
  in_progress: 'sync~spin',
  cancelled: 'x',
};
const PENDING_ICON = 'circle-large';

function iconFor(item: PlanItem): vscode.ThemeIcon {
  return new vscode.ThemeIcon(ICONS[item.status.trim()] ?? PENDING_ICON);
}

export class ChecklistTree implements vscode.TreeDataProvider<ChecklistRow> {
  private readonly changed = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this.changed.event;
  /** What the tree currently shows, so a state that renders the same is not redrawn. */
  private shown = '';
  private empty?: boolean;
  private state: SessionState = EMPTY_SESSION_STATE;

  /** Called with EMPTY_SESSION_STATE at activation, so the welcome text is there before the first event. */
  update(state: SessionState): void {
    this.state = state;
    const isEmpty = state.checklists.length === 0;
    if (isEmpty !== this.empty) {
      this.empty = isEmpty;
      void vscode.commands.executeCommand('setContext', 'forebrain.checklistEmpty', isEmpty);
    }
    const signature = JSON.stringify(state.checklists);
    if (signature === this.shown) return;
    this.shown = signature;
    this.changed.fire();
  }

  /** Activates the welcome view without waiting for a first state change. */
  activate(): void {
    this.update(EMPTY_SESSION_STATE);
  }

  getTreeItem(row: ChecklistRow): vscode.TreeItem {
    if (row.kind === 'checklist') {
      const primary = row.checklist.agent === 'primary';
      const item = new vscode.TreeItem(
        primary ? row.checklist.title || 'Checklist' : `Subagent: ${row.checklist.agent}`,
        row.checklist.items.length > 0 ? vscode.TreeItemCollapsibleState.Expanded : vscode.TreeItemCollapsibleState.None,
      );
      item.description = `${row.checklist.completed}/${row.checklist.total}`;
      item.tooltip = row.checklist.explanation || row.checklist.title;
      item.contextValue = 'forebrain.checklist';
      return item;
    }
    const treeItem = new vscode.TreeItem(row.item.content, vscode.TreeItemCollapsibleState.None);
    treeItem.iconPath = iconFor(row.item);
    treeItem.description = row.item.active;
    treeItem.tooltip = row.item.content;
    treeItem.contextValue = `forebrain.checklistItem.${row.item.status.trim()}`;
    return treeItem;
  }

  getChildren(row?: ChecklistRow): ChecklistRow[] {
    if (row === undefined) return this.state.checklists.map((checklist) => ({ kind: 'checklist', checklist }));
    if (row.kind === 'item') return [];
    return row.checklist.items.map((item) => ({ kind: 'item', checklist: row.checklist, item }));
  }

  dispose(): void {
    this.changed.dispose();
  }
}