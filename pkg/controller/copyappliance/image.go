package copyappliance

import (
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
)

// ApplianceContainerImageName is where the image is filed in the appliance's podman
// store. Deliberately not the cluster pull spec: the appliance has no reason to
// carry the in-cluster registry's hostname, and a digest reference cannot be a
// tag in a docker archive.
const ApplianceContainerImageName = "localhost/forklift-copy-appliance"

// AppliancePodmanLoadCommand reads a docker archive on its standard input and adds
// what it finds to the appliance's podman store.
const AppliancePodmanLoadCommand = "/usr/local/bin/toehold-podman load"

// makeTag makes a tag for the image from its digest.
func makeTag(img v1.Image) (tag name.Tag, err error) {
	digest, err := img.Digest()
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	tag, err = name.NewTag(fmt.Sprintf("%s:%s", ApplianceContainerImageName, digest.Hex), name.WithDefaultRegistry(""))
	if err != nil {
		err = liberr.Wrap(err, "digest", digest.String())
		return
	}
	return
}
