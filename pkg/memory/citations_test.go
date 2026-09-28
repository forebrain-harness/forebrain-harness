package memory

import (
	"strings"
	"testing"
)

func TestStripAndParseCitations(t *testing.T) {
	in := "answer<oai-mem-citation>\n<citation_entries>\nMEMORY.md:1-2|note=[summary]\nrollout_summaries/run.md:10-12|note=[details]\n</citation_entries>\n<rollout_ids>\nfirst\nsecond\nfirst\n</rollout_ids>\n</oai-mem-citation>"
	visible, citation := StripAndParseCitations(in)
	if visible != "answer" {
		t.Fatalf("visible = %q", visible)
	}
	if citation == nil || len(citation.Entries) != 2 || citation.Entries[0].Path != "MEMORY.md" || citation.Entries[1].LineStart != 10 {
		t.Fatalf("entries = %#v", citation)
	}
	if len(citation.RolloutIDs) != 2 || citation.RolloutIDs[0] != "first" || citation.RolloutIDs[1] != "second" {
		t.Fatalf("rollout ids = %#v", citation.RolloutIDs)
	}
}

func TestCitationParserPreservesRawIDStrings(t *testing.T) {
	parsed := ParseMemoryCitation([]string{"<rollout_ids>\n  raw-id  \nraw-id\n  raw-id  \n</rollout_ids>"})
	if parsed == nil || len(parsed.RolloutIDs) != 2 || parsed.RolloutIDs[0] != "  raw-id  " || parsed.RolloutIDs[1] != "raw-id" {
		t.Fatalf("raw ids=%#v", parsed)
	}
}

func TestCitationParserSupportsLegacyThreadIDsAndPrefersRollouts(t *testing.T) {
	legacy := ParseMemoryCitation([]string{"<thread_ids>\nfirst\nsecond\n</thread_ids>"})
	if legacy == nil || len(legacy.RolloutIDs) != 2 || legacy.RolloutIDs[0] != "first" {
		t.Fatalf("legacy ids = %#v", legacy)
	}
	preferred := ParseMemoryCitation([]string{"<rollout_ids>\ncurrent\n</rollout_ids><thread_ids>\nlegacy\n</thread_ids>"})
	if preferred == nil || len(preferred.RolloutIDs) != 1 || preferred.RolloutIDs[0] != "current" {
		t.Fatalf("preferred ids = %#v", preferred)
	}
}

func TestValidMemoryCitationThreadIDsFiltersCanonicalizesAndDeduplicates(t *testing.T) {
	citation := &MemoryCitation{RolloutIDs: []string{
		"not-a-uuid",
		"550E8400-E29B-41D4-A716-446655440000",
		"550e8400-e29b-41d4-a716-446655440000",
		"6ba7b8109dad11d180b400c04fd430c8",
	}}
	got := ValidMemoryCitationThreadIDs(citation)
	if len(got) != 2 || got[0] != "550e8400-e29b-41d4-a716-446655440000" || got[1] != "6ba7b810-9dad-11d1-80b4-00c04fd430c8" {
		t.Fatalf("valid ids = %#v", got)
	}
}

func TestCitationParserRejectsUnsupportedBodies(t *testing.T) {
	for _, body := range []string{
		`{"rollout_ids":["thread"]}`,
		"thread",
	} {
		if parsed := ParseMemoryCitation([]string{body}); parsed != nil {
			t.Fatalf("unsupported body parsed: %q => %#v", body, parsed)
		}
	}
}

func TestStripAndParseCitationsNoCitation(t *testing.T) {
	visible, citation := StripAndParseCitations("plain answer")
	if visible != "plain answer" || citation != nil {
		t.Fatalf("visible=%q citation=%#v", visible, citation)
	}
}

func TestCitationStreamFilterAcrossChunkBoundaries(t *testing.T) {
	var filter CitationStreamFilter
	var visible strings.Builder
	for _, chunk := range []string{"Hello <oai-mem-", "citation><rollout_ids>\ns1\n</rollout_ids></oai-mem-", "citation> world"} {
		visible.WriteString(filter.Push(chunk))
	}
	visible.WriteString(filter.Finish())
	if visible.String() != "Hello  world" {
		t.Fatalf("visible = %q", visible.String())
	}
}

func TestCitationStreamFilterEOFBehavior(t *testing.T) {
	var partial CitationStreamFilter
	if visible := partial.Push("hello <oai-mem-") + partial.Finish(); visible != "hello <oai-mem-" {
		t.Fatalf("partial opening tag visible = %q", visible)
	}
	var open CitationStreamFilter
	if visible := open.Push("hello <oai-mem-citation>secret") + open.Finish(); visible != "hello " {
		t.Fatalf("unterminated citation visible = %q", visible)
	}
}
