// The words of the native approval notification.
//
// The notification is a doorway, not a replica: the headline names the tool the
// gateway wants to run and the detail shows the command it would run, and the
// fine choices (rules, prefixes) stay in the panel's card.
import type { PendingApproval } from './sessionState';

const DETAIL_LIMIT = 200;

/** One line for the notification, from a requires_action's data. */
export function approvalHeadline(pending: PendingApproval): string {
  if (pending.toolName) return `Forebrain: allow ${pending.toolName}?`;
  return `Forebrain needs your input (${pending.kind})`;
}

/** The second line: the command/input when it is a string, otherwise nothing. */
export function approvalDetail(permissionInput: unknown): string {
  if (typeof permissionInput === 'string') return permissionInput.slice(0, DETAIL_LIMIT);
  if (typeof permissionInput === 'object' && permissionInput !== null) {
    const command = (permissionInput as { readonly command?: unknown }).command;
    if (typeof command === 'string') return command;
  }
  return '';
}