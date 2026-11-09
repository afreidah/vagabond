// -------------------------------------------------------------------------------
// Registered Job Tests
//
// Author: Alex Freidah
//
// Covers the fingerprint that decides whether registering a job again creates
// a new version. Reformatting must not; a change to any value must.
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

// A changed value is a change to the job.
func TestFingerprint_SeesAChange(t *testing.T) {
	changed := `job "ci" {
  type = "service"
}
`

	if Fingerprint([]byte(tidy)) == Fingerprint([]byte(changed)) {
		t.Error("a changed job kept its fingerprint")
	}
}
