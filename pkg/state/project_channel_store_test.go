package state

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// newProjectChannelStoresForTest opens one state database with two agents, so a
// channel account's owner and a channel's own project are both reachable.
func newProjectChannelStoresForTest(t *testing.T) (*ProjectStore, *ProjectChannelStore, *ProjectChannelStore) {
	t.Helper()
	db, err := OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewProjectStore(db, "main"), NewProjectChannelStore(db, "main"), NewProjectChannelStore(db, "acme")
}

// An upsert of the same (project, kind) replaces the row: the channel's
// identity is that pair, so a second write is not a second channel.
func TestProjectChannelUpsertReplacesByProjectAndKind(t *testing.T) {
	ctx := context.Background()
	projects, channels, _ := newProjectChannelStoresForTest(t)
	p, err := projects.Create(ctx, CreateProjectInput{Name: "one", Root: "/tmp/channel-one"})
	if err != nil {
		t.Fatal(err)
	}
	if err := channels.Upsert(ctx, ProjectChannel{ProjectID: p.ID, Kind: "webhook", Enabled: true, ConfigJSON: `{"v":1}`}); err != nil {
		t.Fatal(err)
	}
	if err := channels.Upsert(ctx, ProjectChannel{ProjectID: p.ID, Kind: "webhook", Enabled: false, ConfigJSON: `{"v":2}`}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := channels.Get(ctx, p.ID, "webhook")
	if err != nil || !ok {
		t.Fatalf("get channel: ok=%v err=%v", ok, err)
	}
	if got.Enabled || got.ConfigJSON != `{"v":2}` {
		t.Fatalf("upsert must replace the row: %+v", got)
	}
	list, err := channels.List(ctx, p.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d channels, err %v; want 1", len(list), err)
	}
}

// An account key that is already bound to another project is refused with the
// project that holds it — the unique index is what makes that race-free, and a
// second project's write must not overwrite the first project's row.
func TestProjectChannelAccountBelongsToOneProject(t *testing.T) {
	ctx := context.Background()
	projects, channels, other := newProjectChannelStoresForTest(t)
	first, err := projects.Create(ctx, CreateProjectInput{Name: "first", Root: "/tmp/channel-first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewProjectStore(projects.db, "acme").Create(ctx, CreateProjectInput{Name: "second", Root: "/tmp/channel-second"})
	if err != nil {
		t.Fatal(err)
	}
	if err := channels.Upsert(ctx, ProjectChannel{ProjectID: first.ID, Kind: "feishu", AccountKey: "acct-1", ConfigJSON: `{"a":1}`}); err != nil {
		t.Fatal(err)
	}
	err = other.Upsert(ctx, ProjectChannel{ProjectID: second.ID, Kind: "feishu", AccountKey: "acct-1", ConfigJSON: `{"a":2}`})
	var taken ErrChannelAccountTaken
	if !errors.As(err, &taken) || taken.Project != first.ID {
		t.Fatalf("second bind err = %v, want ErrChannelAccountTaken for %s", err, first.ID)
	}
	kept, ok, err := channels.Get(ctx, first.ID, "feishu")
	if err != nil || !ok {
		t.Fatalf("first project's channel must survive: ok=%v err=%v", ok, err)
	}
	if kept.ConfigJSON != `{"a":1}` {
		t.Fatalf("first project's channel was overwritten: %+v", kept)
	}
}

// A channel with no account yet is the ordinary case for several projects at
// once: the account index is partial, so the empty key is never a conflict.
func TestProjectChannelEmptyAccountIsRepeatable(t *testing.T) {
	ctx := context.Background()
	projects, channels, other := newProjectChannelStoresForTest(t)
	for index, store := range []*ProjectChannelStore{channels, other} {
		agentProject := NewProjectStore(projects.db, store.AgentID())
		p, err := agentProject.Create(ctx, CreateProjectInput{Name: fmt.Sprintf("p%d", index), Root: fmt.Sprintf("/tmp/channel-empty-%d", index)})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Upsert(ctx, ProjectChannel{ProjectID: p.ID, Kind: "webhook", ConfigJSON: `{}`}); err != nil {
			t.Fatalf("channel %d with an empty account key must be accepted: %v", index, err)
		}
	}
}
