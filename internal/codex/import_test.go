package codex

import "testing"

func TestCompareHistories(t *testing.T) {
	short := []byte("{\"turn\":1}\n")
	long := []byte("{\"turn\":1}\n{\"turn\":2}\n")
	other := []byte("{\"turn\":1}\n{\"turn\":3}\n")
	for _, test := range []struct {
		name            string
		local, incoming []byte
		want            historyComparison
	}{
		{name: "equal", local: long, incoming: long, want: historiesEqual},
		{name: "incoming ahead", local: short, incoming: long, want: incomingAhead},
		{name: "local ahead", local: long, incoming: short, want: localAhead},
		{name: "diverged", local: long, incoming: other, want: historiesDiverged},
	} {
		t.Run(test.name, func(t *testing.T) {
			// when, then
			if got := compareHistories(test.local, test.incoming); got != test.want {
				t.Fatalf("compareHistories() = %d, want %d", got, test.want)
			}
		})
	}
}
