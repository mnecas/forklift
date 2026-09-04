package toehold

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// GuestUserdata returns base64-encoded cloud-init userdata with SSH authorized keys.
func GuestUserdata(publicKey string) string {
	key := strings.TrimSpace(publicKey)
	if key == "" {
		return ""
	}
	payload := fmt.Sprintf("#cloud-config\nssh_authorized_keys:\n  - %s\n", key)
	return base64.StdEncoding.EncodeToString([]byte(payload))
}
