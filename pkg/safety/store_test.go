package safety

import (
	"testing"
)

func TestStoreRetainsDenyGlobGrant(t *testing.T) {
	depth := 3
	store := NewStore()
	grant := FileSystemPermissionGrant{
		Entry: FileSystemPermissionEntry{
			Path:   FileSystemPermissionPath{Type: FileSystemPermissionPathTypeGlobPattern, Pattern: "**/*.env"},
			Access: FileSystemAccessDeny,
		},
		Scope: GrantScopeSession, SessionID: "session-1", GlobScanMaxDepth: &depth,
	}
	store.AddFileSystemGrant(grant)
	got := store.FileSystemGrants()
	if len(got) != 1 || got[0].Entry.Path.Pattern != "**/*.env" || got[0].GlobScanMaxDepth == nil || *got[0].GlobScanMaxDepth != depth {
		t.Fatalf("grants=%+v", got)
	}
}

func TestStoreFilesystemGrantSnapshotsAreDefensiveCopies(t *testing.T) {
	depth := 3
	value := &FileSystemSpecialPath{Kind: FileSystemSpecialPathRoot}
	store := NewStore()
	store.AddFileSystemGrant(FileSystemPermissionGrant{
		Entry: FileSystemPermissionEntry{
			Path:   FileSystemPermissionPath{Type: FileSystemPermissionPathTypeSpecial, Value: value},
			Access: FileSystemAccessWrite,
		},
		Scope: GrantScopeSession, SessionID: "session-1", GlobScanMaxDepth: &depth,
	})
	value.Kind = FileSystemSpecialPathMinimal
	depth = 99

	snapshot := store.SnapshotForSession("session-1")
	if len(snapshot.FileSystemGrants) != 1 {
		t.Fatalf("snapshot grants=%+v", snapshot.FileSystemGrants)
	}
	snapshot.FileSystemGrants[0].Entry.Path.Value.Kind = FileSystemSpecialPathSlashTmp
	*snapshot.FileSystemGrants[0].GlobScanMaxDepth = 42

	stored := store.FileSystemGrants()
	if stored[0].Entry.Path.Value.Kind != FileSystemSpecialPathRoot || *stored[0].GlobScanMaxDepth != 3 {
		t.Fatalf("store was mutated through an input or snapshot: %+v", stored[0])
	}
}

func TestStoreStrictAutoReviewIsSessionScoped(t *testing.T) {
	store := NewStore()
	ApplyUpdate(store, PermissionUpdate{
		Type: UpdateAddPermissionGrants, Destination: DestinationSession,
		SessionID: "session-1", StrictAutoReviewRunID: "run-1",
	})
	if !store.StrictAutoReviewEnabledForSession("session-1", "run-1") {
		t.Fatal("owning session did not receive strict auto review")
	}
	if store.StrictAutoReviewEnabledForSession("session-2", "run-1") {
		t.Fatal("strict auto review leaked to another session")
	}
	if got := store.SnapshotForSession("session-1").StrictAutoReviewRunIDs; len(got) != 1 || got[0] != "run-1" {
		t.Fatalf("owning snapshot run IDs=%v", got)
	}
	if got := store.SnapshotForSession("session-2").StrictAutoReviewRunIDs; len(got) != 0 {
		t.Fatalf("other snapshot run IDs=%v", got)
	}
	if got := store.Snapshot().StrictAutoReviewRunIDs; len(got) != 0 {
		t.Fatalf("global snapshot leaked session run IDs=%v", got)
	}
}

func TestStoreRejectsUnscopedRuntimeGrants(t *testing.T) {
	store := NewStore()
	entry := FileSystemPermissionEntry{Path: NewFileSystemPermissionPath("/tmp/external"), Access: FileSystemAccessRead}
	store.AddFileSystemGrant(FileSystemPermissionGrant{Entry: entry, Scope: GrantScopeSession})
	store.AddFileSystemGrant(FileSystemPermissionGrant{Entry: entry, Scope: GrantScopeTurn, SessionID: "session-1"})
	store.AddNetworkGrant(NetworkPermissionGrant{Enabled: true, Scope: GrantScopeSession})
	store.AddNetworkGrant(NetworkPermissionGrant{Enabled: true, Scope: GrantScopeTurn, SessionID: "session-1"})
	if len(store.FileSystemGrants()) != 0 || len(store.NetworkGrants()) != 0 {
		t.Fatalf("unscoped grants were retained: filesystem=%+v network=%+v", store.FileSystemGrants(), store.NetworkGrants())
	}
}
