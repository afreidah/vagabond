// -------------------------------------------------------------------------------
// Cost Tests
//
// Author: Alex Freidah
//
// Cost carries one question: would this job spend anything. These tests pin
// that answer, including for the negative values a credit or refund adjustment
// could someday produce.
// -------------------------------------------------------------------------------

package job

import "testing"

func TestCost_Free(t *testing.T) {
	tests := []struct {
		name  string
		input Cost
		want  bool
	}{
		{name: "zero is free", input: 0, want: true},
		{name: "one unit is not free", input: 1, want: false},
		{name: "large amount is not free", input: 1_000_000, want: false},
		{name: "negative is not free", input: -1, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.input.Free(); got != tt.want {
				t.Errorf("Cost(%d).Free() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// The zero value must be free without any construction, because an absent
// max_cost_usd in a job file decodes to it and must not read as permission to
// spend.
func TestCost_ZeroValueIsFree(t *testing.T) {
	var c Cost
	if !c.Free() {
		t.Error("zero-value Cost is not free")
	}
}
