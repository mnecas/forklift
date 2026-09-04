package toehold

import (
	"testing"
)

func TestGuestUserdata(t *testing.T) {
	out := GuestUserdata("ssh-rsa AAAAB3")
	if out == "" {
		t.Fatal("expected userdata")
	}
}
