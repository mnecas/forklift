package copyappliance

import (
	"fmt"
	"io"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
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

// streamImage writes the image into the appliance's podman store as a docker
// archive on the load command's standard input.
func (r *ApplianceContext) streamImage(client *SSHClient, img v1.Image, ref name.Tag) (err error) {
	reader, writer := io.Pipe()
	go func() {
		// A failure part way through arrives at the load side as a read error,
		// rather than as a truncated archive that podman would reject for the
		// wrong reason.
		_ = writer.CloseWithError(tarball.Write(ref, img, writer))
	}()
	// Closing the read half is what unblocks the writer when the load command
	// gives up before the archive is finished.
	defer func() {
		_ = reader.Close()
	}()

	err = client.RunWithStdin(AppliancePodmanLoadCommand, reader)
	return
}
