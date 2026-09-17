// Package diskid normalizes and compares disk identifiers across VMware
// inventory, Linux guests, and SCSI tooling.
package diskid

import (
	"strings"
)

// Canonical returns a lowercase hex-oriented form with common prefixes and
// punctuation removed so identifiers from different sources can be compared.
func Canonical(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	s = strings.TrimPrefix(s, "naa.")
	s = strings.TrimPrefix(s, "wwn-")
	s = strings.TrimPrefix(s, "eui.")
	s = strings.TrimPrefix(s, "0x")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "_", "")
	// scsi_id prefixes NAA identifiers with "3".
	if len(s) > 1 && s[0] == '3' && strings.HasPrefix(s[1:], "6000c29") {
		s = s[1:]
	}
	return s
}

// Match reports whether two disk identifiers refer to the same device. It
// accepts VMware backing.Uuid values, scsi_id output, and sysfs WWN forms.
func Match(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ca, cb := Canonical(a), Canonical(b)
	if ca == cb {
		return true
	}
	if ca == "" || cb == "" {
		return false
	}
	pa, pb := naaPrefix(ca), naaPrefix(cb)
	if pa != "" && pa == pb {
		return true
	}
	// One identifier may be a truncated NAA form of the other.
	if len(ca) >= 16 && len(cb) >= 16 {
		if strings.HasPrefix(ca, cb) || strings.HasPrefix(cb, ca) {
			return true
		}
	}
	return false
}

// naaPrefix returns the first 16 hex characters of an NAA identifier in the
// IEEE Registered (type 5) form used by short guest WWNs.
func naaPrefix(canonical string) string {
	if len(canonical) < 16 {
		return ""
	}
	if strings.HasPrefix(canonical, "6000c29") {
		return "5000c29" + canonical[7:16]
	}
	if strings.HasPrefix(canonical, "5000c29") {
		return canonical[:16]
	}
	return ""
}
