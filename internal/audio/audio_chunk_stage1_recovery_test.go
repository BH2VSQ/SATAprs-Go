package audio

import "testing"

func TestStage1RecoveryMissingAudioChunks(t *testing.T) {
	cases := []struct {
		name     string
		previous uint64
		current  uint64
		want     uint64
	}{
		{name: "first delivered block", previous: 0, current: 1, want: 0},
		{name: "contiguous blocks", previous: 10, current: 11, want: 0},
		{name: "one lost block", previous: 10, current: 12, want: 1},
		{name: "multiple lost blocks", previous: 10, current: 15, want: 4},
		{name: "non-increasing sequence", previous: 10, current: 10, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := missingAudioChunks(tc.previous, tc.current); got != tc.want {
				t.Fatalf("missingAudioChunks(%d, %d) = %d, want %d", tc.previous, tc.current, got, tc.want)
			}
		})
	}
}
