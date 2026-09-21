package review

import "testing"

// TestParseConfigCombinations is the flag matrix for `raj --review`: the
// profile, the two address spellings and the list fallback, plus the refusals
// for a missing --review and too many operands. Without ParseConfig the
// console's grammar would live in main's global flag set and none of these
// combinations would be testable.
func TestParseConfigCombinations(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		addr    string
		phone   bool
		list    bool
		wantErr bool
	}{
		{name: "bare", args: []string{"--review"}},
		{name: "phone", args: []string{"--review", "--phone"}, phone: true},
		{name: "socket", args: []string{"--review", "/run/raj/x.sock"}, addr: "/run/raj/x.sock"},
		{name: "tcp", args: []string{"--review", "tcp://host:7391"}, addr: "tcp://host:7391"},
		{name: "phone and socket", args: []string{"--review", "--phone", "/run/raj/x.sock"},
			phone: true, addr: "/run/raj/x.sock"},
		{name: "list", args: []string{"--review", "--list"}, list: true},
		{name: "missing review", args: []string{"--phone", "--list"}, wantErr: true},
		{name: "two addresses", args: []string{"--review", "a.sock", "b.sock"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseConfig(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Addr != tt.addr || got.Phone != tt.phone || got.List != tt.list {
				t.Fatalf("got %+v, want addr=%q phone=%v list=%v", got, tt.addr, tt.phone, tt.list)
			}
		})
	}
}

// TestRequestedRoutesOnlyReview pins the pre-parse detector main uses: it fires
// on --review before the first operand and ignores it after one, matching Go's
// flag grammar.
func TestRequestedRoutesOnlyReview(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"--review"}, true},
		{[]string{"-review", "--phone"}, true},
		{[]string{"--phone", "--review"}, true},
		{[]string{"file.go"}, false},
		{[]string{"file.go", "--review"}, false},
		{[]string{"--no-restore", "."}, false},
	}
	for _, c := range cases {
		if got := Requested(c.args); got != c.want {
			t.Fatalf("Requested(%v)=%v want %v", c.args, got, c.want)
		}
	}
}
