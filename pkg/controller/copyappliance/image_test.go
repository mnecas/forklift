package copyappliance

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// The appliance is asked about the reference by name, so the reference has to
// change when the image behind the tag does. Otherwise a rebuilt appliance
// image is never picked up: the store still answers to the old name.
func TestMakeTag(t *testing.T) {
	first, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}
	second, err := random.Image(512, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}

	ref, err := makeTag(first)
	if err != nil {
		t.Fatalf("makeTag: %v", err)
	}
	if ref.Repository.Name() != ApplianceContainerImageName {
		t.Errorf("repository = %q, want %q", ref.Repository.Name(), ApplianceContainerImageName)
	}
	digest, err := first.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if !strings.HasPrefix(digest.Hex, ref.TagStr()) || ref.TagStr() == "" {
		t.Errorf("tag = %q, want a prefix of the digest %q", ref.TagStr(), digest.Hex)
	}

	other, err := makeTag(second)
	if err != nil {
		t.Fatalf("makeTag: %v", err)
	}
	if other.Name() == ref.Name() {
		t.Errorf("both images are %q, want two different references", ref.Name())
	}
}

func TestStreamImage(t *testing.T) {
	t.Run("what the appliance is fed reads back as the image", func(t *testing.T) {
		ac, server, client := applianceLogin(t)
		img, err := random.Image(4096, 3)
		if err != nil {
			t.Fatalf("random image: %v", err)
		}
		ref, err := makeTag(img)
		if err != nil {
			t.Fatalf("makeTag: %v", err)
		}

		runner := DeployRunner{context: ac}

		err = runner.streamImage(client, img, ref)
		if err != nil {
			t.Fatalf("streamImage: %v", err)
		}

		archive, ran := server.Stdin(AppliancePodmanLoadCommand)
		if !ran {
			t.Fatalf("ran %v, want %q", server.Ran(), AppliancePodmanLoadCommand)
		}
		opener := func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(archive)), nil
		}
		// The name the archive files the image under is the name the appliance
		// is asked about on the next pass, and it is recorded from ref.Name().
		// If those two ever disagree the probe never matches and every pass
		// sends the image again.
		manifest, err := tarball.LoadManifest(opener)
		if err != nil {
			t.Fatalf("read back the manifest: %v", err)
		}
		if len(manifest) != 1 || !slices.Contains(manifest[0].RepoTags, ref.Name()) {
			t.Errorf("archive holds %+v, want one image tagged %q", manifest, ref.Name())
		}
		// By position rather than by tag. tarball's own lookup re-parses the
		// RepoTag with a default registry and would not find a reference that
		// deliberately has none; podman matches the name as written.
		loaded, err := tarball.Image(opener, nil)
		if err != nil {
			t.Fatalf("read back the archive: %v", err)
		}
		config, err := loaded.ConfigName()
		if err != nil {
			t.Fatalf("config: %v", err)
		}
		want, err := img.ConfigName()
		if err != nil {
			t.Fatalf("config: %v", err)
		}
		if config != want {
			t.Errorf("config = %s, want %s", config, want)
		}
		layers, err := loaded.Layers()
		if err != nil {
			t.Fatalf("layers: %v", err)
		}
		if len(layers) != 3 {
			t.Errorf("layers = %d, want 3", len(layers))
		}
	})

	// A load that fails is the end of the deploy, so it has to say what the
	// appliance said rather than that a pipe broke.
	t.Run("a load the appliance refuses carries its output into the error", func(t *testing.T) {
		ac, _, client := applianceLogin(t, AppliancePodmanLoadCommand)
		img, err := random.Image(4096, 2)
		if err != nil {
			t.Fatalf("random image: %v", err)
		}
		ref, err := makeTag(img)
		if err != nil {
			t.Fatalf("makeTag: %v", err)
		}

		runner := DeployRunner{context: ac}

		err = runner.streamImage(client, img, ref)
		if err == nil {
			t.Fatal("streamImage succeeded against an appliance that refused the load")
		}
		if !errorMentions(t, err, commandFailureOutput) {
			t.Errorf("error = %q, want it to carry %q", err, commandFailureOutput)
		}
	})
}

func TestInjectImage(t *testing.T) {
	// The image is hundreds of megabytes, so a pass that re-entered the step
	// and sent it again would cost as much as the first one did. Asking the
	// appliance is the whole of what makes the step idempotent.
	t.Run("an image the appliance already has is not sent again", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public)
		ac := sshContext(t, private, server.addr)
		ac.Appliance.Status.ExporterImage = ApplianceContainerImageName + ":0123456789ab"
		runner := DeployRunner{context: ac}

		done, err := runner.InjectImage(context.TODO())
		if err != nil {
			t.Fatalf("InjectImage: %v", err)
		}
		if !done {
			t.Error("done = false, want the image the appliance already has to count")
		}
		if slices.Contains(server.Ran(), AppliancePodmanLoadCommand) {
			t.Errorf("ran %v, want no load at all", server.Ran())
		}
	})

	// sshd can go away between passes, and an appliance that is not answering
	// is one to come back to rather than one to fail the deploy over.
	t.Run("an appliance that is not answering is not a failure", func(t *testing.T) {
		private, _ := testKeyPair(t)
		runner := DeployRunner{context: sshContext(t, private, closedAddr(t))}

		done, err := runner.InjectImage(context.TODO())
		if err != nil {
			t.Fatalf("InjectImage: %v", err)
		}
		if done {
			t.Error("done = true, want the appliance left to come back")
		}
	})

	// WaitForNetwork does not hand over until there is one, so this is a phase
	// reached out of order rather than an appliance still booting.
	t.Run("an appliance reporting no address fails", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public)
		ac := sshContext(t, private, server.addr)
		ac.Appliance.Status.Addresses = nil
		runner := DeployRunner{context: ac}

		done, err := runner.InjectImage(context.TODO())
		if err == nil {
			t.Fatal("InjectImage succeeded with no address to reach the appliance at")
		}
		if done {
			t.Error("done = true, want nothing loaded")
		}
		if !errorMentions(t, err, ac.Appliance.Name) {
			t.Errorf("error = %q, want it to name the appliance", err)
		}
	})

	t.Run("the image is sent and the reference recorded", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public)
		ac := sshContext(t, private, server.addr)
		spec, img := pushImage(t, testRegistry(t, nil))
		runner := DeployRunner{
			context:  ac,
			registry: applianceRegistry(t, ac, spec),
		}

		done, err := runner.InjectImage(context.TODO())
		if err != nil {
			t.Fatalf("InjectImage: %v", err)
		}
		if !done {
			t.Error("done = false, want the image loaded")
		}
		want, err := makeTag(img)
		if err != nil {
			t.Fatalf("makeTag: %v", err)
		}
		if ac.Appliance.Status.ExporterImage != want.Name() {
			t.Errorf("recorded %q, want %q", ac.Appliance.Status.ExporterImage, want.Name())
		}
		if _, ran := server.Stdin(AppliancePodmanLoadCommand); !ran {
			t.Errorf("ran %v, want %q", server.Ran(), AppliancePodmanLoadCommand)
		}
	})

	// A load that was cut off part way can still leave the image in the store,
	// so the next pass has to know which reference to ask about even though
	// this one failed.
	t.Run("a load that fails still records what to ask about", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public, AppliancePodmanLoadCommand)
		ac := sshContext(t, private, server.addr)
		spec, img := pushImage(t, testRegistry(t, nil))
		runner := DeployRunner{
			context:  ac,
			registry: applianceRegistry(t, ac, spec),
		}

		done, err := runner.InjectImage(context.TODO())
		if err == nil {
			t.Fatal("InjectImage succeeded against an appliance that refused the load")
		}
		if done {
			t.Error("done = true, want the load to have failed")
		}
		if !errorMentions(t, err, commandFailureOutput) {
			t.Errorf("error = %q, want it to carry %q", err, commandFailureOutput)
		}
		want, err := makeTag(img)
		if err != nil {
			t.Fatalf("makeTag: %v", err)
		}
		if ac.Appliance.Status.ExporterImage != want.Name() {
			t.Errorf("recorded %q, want %q", ac.Appliance.Status.ExporterImage, want.Name())
		}
	})
}

// applianceRegistry is a cluster registry that resolves the ImageStreamTag from
// the appliance spec, which is the tag InjectImage asks for, to the given pull
// spec.
func applianceRegistry(t *testing.T, ac *ApplianceContext, spec string) *ClusterRegistry {
	t.Helper()
	return testClusterRegistry(
		t,
		ac.Appliance.Namespace,
		map[string]string{ac.Appliance.Spec.ContainerImage: spec})
}
