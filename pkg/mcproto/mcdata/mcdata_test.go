package mcdata

import "testing"

// Embedded tables load, index by version, and answer lookups
func TestEmbeddedTables(t *testing.T) {
	if len(All()) == 0 {
		t.Fatal("no tables embedded")
	}
	if Newest() < 777 || Oldest() != 766 {
		t.Fatalf("protocol range %d to %d", Oldest(), Newest())
	}
	for i := 1; i < len(All()); i++ {
		if All()[i-1].Protocol >= All()[i].Protocol {
			t.Fatal("tables are not sorted by protocol")
		}
	}
	p, ok := ProtocolForVersion("26.3")
	if !ok || p != 777 {
		t.Fatalf("26.3 resolved to %d %v", p, ok)
	}
	if _, ok := ProtocolForVersion("1.20.4"); ok {
		t.Fatal("1.20.4 predates the tables")
	}
	doc := Lookup(777)
	if doc == nil {
		t.Fatal("777 missing")
	}
	if id, ok := doc.PacketID("play", "clientbound", "login"); !ok || id != 0x32 {
		t.Fatalf("777 login id %#x %v", id, ok)
	}
	if id, ok := doc.EntryID("dimension_type", "overworld"); !ok || id != 0 {
		t.Fatalf("777 overworld id %d %v", id, ok)
	}
	if _, ok := doc.StateID("beacon"); !ok {
		t.Fatal("777 beacon state missing")
	}
	if Lookup(766).HasPackets() {
		t.Fatal("766 jars ship no packet report")
	}
}

// Parse rejects documents missing what the codec needs
func TestParseRejectsIncomplete(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"protocol": 800}`,
		`{"protocol": 800, "versions": ["x"]}`,
		`{"protocol": 800, "versions": ["x"], "packets": {"play": {"clientbound": {"a": 1}}}, "registries": [{"name": "r", "entries": []}]}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
