package owner

import "testing"

func indexOverwrites(items []Overwrite) map[string]Overwrite {
	result := map[string]Overwrite{}
	for _, item := range items {
		result[item.ID] = item
	}
	return result
}

func TestPolicyClosesEveryManagementBypassPreservingOtherBits(t *testing.T) {
	const send uint64 = 1 << 11
	const high uint64 = 1 << 60
	input := []Overwrite{
		{ID: "1", Type: 0, Allow: ManageChannels | send, Deny: high},
		{ID: "2", Type: 0, Allow: management | high, Deny: send},
		{ID: "3", Type: 1, Allow: management | send, Deny: high},
		{ID: "4", Type: 1, Allow: send, Deny: ViewChannel | ManageChannels},
	}
	output, err := BuildOverwrites(input, "1", "5", "4", false)
	if err != nil {
		t.Fatal(err)
	}
	got := indexOverwrites(output)
	for _, id := range []string{"1", "2", "3"} {
		if got[id].Allow&management != 0 || got[id].Deny&management != management {
			t.Fatalf("management bypass for %s: %+v", id, got[id])
		}
	}
	for _, old := range input {
		current := got[old.ID]
		preserve := ^(management)
		if old.ID == "4" {
			preserve &^= ViewChannel
		}
		if old.Allow&preserve != current.Allow&preserve || old.Deny&preserve != current.Deny&preserve {
			t.Fatalf("unrelated bits changed: %+v -> %+v", old, current)
		}
	}
	if got["4"].Allow&(ManageChannels|ViewChannel) != (ManageChannels|ViewChannel) || got["4"].Allow&ManageRoles != 0 || got["4"].Deny&ManageRoles == 0 {
		t.Fatalf("owner policy: %+v", got["4"])
	}
	if got["5"].Allow&(management|ViewChannel) != (management|ViewChannel) || got["5"].Deny&(management|ViewChannel) != 0 {
		t.Fatalf("bot policy: %+v", got["5"])
	}
	if input[0].Allow != ManageChannels|send {
		t.Fatal("mutated caller's slice")
	}
	again, err := BuildOverwrites(output, "1", "5", "4", false)
	if err != nil {
		t.Fatal(err)
	}
	same, err := EqualOverwrites(output, again)
	if err != nil || !same {
		t.Fatalf("policy is not idempotent: %v", err)
	}
}

func TestPolicyUnknownOwnerAndBotCreator(t *testing.T) {
	sealed, err := BuildOverwrites(nil, "1", "2", "", true)
	if err != nil {
		t.Fatal(err)
	}
	got := indexOverwrites(sealed)
	if len(got) != 2 || got["1"].Deny&management != management || got["2"].Allow&Connect == 0 {
		t.Fatalf("bad sealed policy: %+v", got)
	}
	owned, err := BuildOverwrites(nil, "1", "2", "2", true)
	if err != nil {
		t.Fatal(err)
	}
	bot := indexOverwrites(owned)["2"]
	if bot.Allow&management != management || bot.Deny&management != 0 {
		t.Fatalf("bot creator lost permission editing: %+v", bot)
	}
}

func TestPolicyRejectsContradictoryIDs(t *testing.T) {
	cases := []struct {
		name, guild, bot, owner string
		entries                 []Overwrite
	}{
		{"same guild bot", "1", "1", "", nil},
		{"same guild owner", "1", "2", "1", nil},
		{"bad snowflake", "0", "2", "", nil},
		{"overflow", "18446744073709551616", "2", "", nil},
		{"duplicate", "1", "2", "", []Overwrite{{ID: "3", Type: 0}, {ID: "3", Type: 1}}},
		{"everyone member", "1", "2", "", []Overwrite{{ID: "1", Type: 1}}},
		{"bot role", "1", "2", "", []Overwrite{{ID: "2", Type: 0}}},
		{"owner role", "1", "2", "3", []Overwrite{{ID: "3", Type: 0}}},
		{"bad type", "1", "2", "", []Overwrite{{ID: "4", Type: 2}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildOverwrites(test.entries, test.guild, test.bot, test.owner, false); err == nil {
				t.Fatal("accepted malformed policy")
			}
		})
	}
}

func TestEqualOverwritesIgnoresOrderButChecksValues(t *testing.T) {
	a := []Overwrite{{ID: "1", Type: 0, Deny: ManageChannels}, {ID: "2", Type: 1, Allow: ManageRoles}}
	b := []Overwrite{a[1], a[0]}
	equal, err := EqualOverwrites(a, b)
	if err != nil || !equal {
		t.Fatal("order mattered")
	}
	b[0].Allow = 0
	equal, err = EqualOverwrites(a, b)
	if err != nil || equal {
		t.Fatal("permission difference ignored")
	}
}

func TestValidID(t *testing.T) {
	for _, id := range []string{"1", "18446744073709551615"} {
		if !ValidID(id) {
			t.Fatalf("rejected %s", id)
		}
	}
	for _, id := range []string{"", "0", "01", "-1", "+1", "1.0", "1e3", " 1", "18446744073709551616"} {
		if ValidID(id) {
			t.Fatalf("accepted %s", id)
		}
	}
}
