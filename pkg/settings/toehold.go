package settings

import (
	"os"

	liberr "github.com/kubev2v/forklift/pkg/lib/error"
)

const (
	ToeholdBibImage      = "TOEHOLD_BIB_IMAGE"
	ToeholdImporterImage = "TOEHOLD_IMPORTER_IMAGE"
)

// Toehold settings for the toehold controller and build Job.
type Toehold struct {
	BibImage      string
	ImporterImage string
}

func (r *Toehold) Load() error {
	r.BibImage = os.Getenv(ToeholdBibImage)
	if r.BibImage == "" {
		r.BibImage = "quay.io/centos-bootc/bootc-image-builder:latest"
	}
	if val, ok := os.LookupEnv(ToeholdImporterImage); ok {
		r.ImporterImage = val
	} else if Settings.Role.Has(MainRole) {
		return liberr.New("failed to find environment variable " + ToeholdImporterImage)
	}
	return nil
}
