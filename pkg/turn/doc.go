// Package mention implements the @ file-mention engine shared by every
// composer surface: the terminal composer and the web composer both resolve
// mentions through this package rather than reimplementing the rules against
// their own editor.
//
// A mention is a pointer, not content. Accepting a file injects nothing into
// the prompt: the "@" is resolved away in the composer and only a bare path
// remains, which the model reads with its own tools if it needs to. This
// matches upstream codex, and it is what keeps a mention from costing context
// window — an inlined file can overflow the window outright, and it perturbs
// the prompt prefix that caching depends on. Text typed by hand is never
// scanned for mentions on any surface, so a path in a prompt is just a path.
//
// Images are the one exception, since no tool can hand the model pixels: an
// accepted image is attached to the turn and its token is removed.
//
// The engine has three parts:
//
//   - search.go   candidate lookup for the typeahead (git index, walk
//     fallback, directory drill-down), cached per root
//   - compose.go  locating the "@" token under a cursor and applying an
//     accepted candidate back into the draft
//   - classify.go which targets are attachable images
//
// Path resolution is surface-specific and supplied by the caller through
// Resolver: local composers resolve against a working directory, while
// server-backed surfaces resolve strictly inside a workspace root.
//
// The package also owns auto-continue (in submit.go): a turn stopped by a
// spent usage allowance whose reset the provider stated is continued by the
// runtime once the allowance returns. Submit arms the wait on such a failure
// and any later turn in the session supersedes it, so the terminal and the web
// show, cancel and fire the same continuation; a surface supplies only the
// port that runs the continuation turn and a sink for the auto_continue_*
// lifecycle events.
package turn
