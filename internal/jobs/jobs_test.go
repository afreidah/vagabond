// -------------------------------------------------------------------------------
// Registered Job Tests
//
// Author: Alex Freidah
// -------------------------------------------------------------------------------

package jobs

import "testing"

const tidy = `job "ci" {
  type = "batch"
}
`

// Whitespace is formatting, not a change to the job.
func TestFingerprint_IgnoresFormatting(t *testing.T) {
	messy := "job   \"ci\"   {\n      type=\"batch\"\n}\n"

	if Fingerprint([]byte(tidy)) != Fingerprint([]byte(messy)) {
		t.Error("reformatting the same job changed its fingerprint")
	}
}

func TestFingerprint_SeesAChange(t *testing.T) {
	changed := `job "ci" {
  type = "service"
}
`

	if Fingerprint([]byte(tidy)) == Fingerprint([]byte(changed)) {
		t.Error("a changed job kept its fingerprint")
	}
}
