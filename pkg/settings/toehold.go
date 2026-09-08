package settings

import (
	"os"

	liberr "github.com/kubev2v/forklift/pkg/lib/error"
)

const (
	ToeholdBibImage      = "TOEHOLD_BIB_IMAGE"
	ToeholdUploaderImage = "TOEHOLD_UPLOADER_IMAGE"
)

// Toehold settings for the toehold controller and build Job.
type Toehold struct {
	BibImage      string
	UploaderImage string
}

func (r *Toehold) Load() error {
	r.BibImage = os.Getenv(ToeholdBibImage)
	if r.BibImage == "" {
		r.BibImage = "quay.io/centos-bootc/bootc-image-builder:latest"
	}
	if val, ok := os.LookupEnv(ToeholdUploaderImage); ok {
		r.UploaderImage = val
	} else if Settings.Role.Has(MainRole) {
		return liberr.New("failed to find environment variable " + ToeholdUploaderImage)
	}
	return nil
}
