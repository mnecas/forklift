package copyappliance

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"testing"
	"time"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/nbd-container/announce"
	"github.com/kubev2v/forklift/pkg/nbd-container/runner"
)

// Both the wait and the login go through this, so an appliance that reports
// nothing must read as "not yet" and not as an empty address to dial.
func TestApplianceAddress(t *testing.T) {
	tests := []struct {
		name      string
		addresses []api.ApplianceAddress
		want      string
		wantOK    bool
	}{
		{"the address the guest reports",
			[]api.ApplianceAddress{
				{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "192.0.2.10"},
			},
			"192.0.2.10", true},
		// One adapter is reported once per address it holds, and the appliance
		// answers on any of them.
		{"an adapter with more than one address gives the first",
			[]api.ApplianceAddress{
				{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "192.0.2.10"},
				{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "2001:db8::1"},
			},
			"192.0.2.10", true},
		{"no addresses at all", nil, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := applianceAddress(tc.addresses)

			if got != tc.want || ok != tc.wantOK {
				t.Errorf("applianceAddress(%+v) = (%q, %v), want (%q, %v)",
					tc.addresses, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// A pass walks as many steps as it can, so the phase it records is not the
// phase it started on. Recording the entry phase instead would send the next
// pass back to a step that is already done, and would tell an operator the
// appliance is waiting on something it is not.
func TestRunRecordsTheStepItStoppedIn(t *testing.T) {
	private, public := testKeyPair(t)
	server := startSSHServer(t, public)
	ac := sshContext(t, private, server.addr)
	// The appliance answers every command, so it reads as already installed and
	// running. Nothing is announcing exports, so the step after it cannot
	// finish, and that is where the pass has to stop.
	ac.Appliance.Status.ExporterImage = testLoadedImage
	ac.Appliance.Status.Phase = PhaseConfigure
	runner := DeployRunner{context: ac}

	err := runner.Run(context.TODO())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ac.Appliance.Status.Phase != PhaseWaitForExports {
		t.Errorf("phase = %q, want %q: the pass configured the appliance and stopped waiting for its exports",
			ac.Appliance.Status.Phase, PhaseWaitForExports)
	}
}

// Configure used to run before LoadImage and now runs after it. An appliance
// that an older controller left in the configure phase has no loaded image, and
// nothing in the itinerary walks backwards, so without the shim it would
// install a supervisor with no image to run and then wait forever for exports
// that cannot appear.
func TestRunSendsBackAnApplianceThatSkippedTheLoad(t *testing.T) {
	private, public := testKeyPair(t)
	server := startSSHServer(t, public)
	ac := sshContext(t, private, server.addr)
	ac.Appliance.Status.Phase = PhaseConfigure
	runner := DeployRunner{context: ac}

	err := runner.Run(context.TODO())

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ac.Appliance.Status.Phase != PhaseLoadImage {
		t.Errorf("phase = %q, want %q", ac.Appliance.Status.Phase, PhaseLoadImage)
	}
	if ran := server.Ran(); len(ran) != 0 {
		t.Errorf("ran %v, want the appliance left alone until it has an image", ran)
	}
}

// The pipeline is the order the pass walks in, so it has to agree with the
// order execute implements. LoadImage before Configure is the part that has
// already been got wrong once, and the shim above is what it cost.
func TestDeployItinerary(t *testing.T) {
	runner := DeployRunner{context: &ApplianceContext{Appliance: testAppliance()}}

	got := phaseNames(t, runner.Itinerary())

	want := []string{
		PhaseCloneVM,
		PhaseWaitForClone,
		PhaseWaitForNetwork,
		PhaseLoadImage,
		PhaseConfigure,
		PhaseWaitForExports,
		PhaseDeployCompleted,
	}
	if !slices.Equal(got, want) {
		t.Errorf("pipeline = %v, want %v", got, want)
	}
	// The failure phase is not a step the walk arrives at; it is where the walk
	// ends when a step errors. A pipeline that contains it would hand it back
	// as the step after DeployCompleted.
	if slices.Contains(got, PhaseDeployFailed) {
		t.Errorf("pipeline %v walks to %q", got, PhaseDeployFailed)
	}
}

// WaitForExports is the step that proves the appliance works, so it is tested
// against a real announce server over the same mutual TLS a migration uses.
func TestWaitForExports(t *testing.T) {
	// testAppliance has two disks attached, so two exports is the whole set.
	twoExports := []runner.Export{
		{WWID: "wwn-abc", Port: 10809, Device: "/dev/sdb"},
		{WWID: "wwn-def", Port: 10810, Device: "/dev/sdc"},
	}

	t.Run("an appliance exporting every disk is done", func(t *testing.T) {
		ac := exportsContext(t, startAnnounce(t, applianceTLS().server, twoExports))

		done, err := ac.WaitForExports(context.TODO())

		if err != nil {
			t.Fatalf("WaitForExports: %v", err)
		}
		if !done {
			t.Error("done = false, want the appliance exporting its disks")
		}
		want := []api.ApplianceExport{
			{
				WWID:         "wwn-abc",
				Port:         10809,
				Device:       "/dev/sdb",
				DiskKey:      2000,
				VMDKPath:     "[datastore13] vm-a/disk-0.vmdk",
				SourceSerial: "wwn-abc",
			},
			{
				WWID:         "wwn-def",
				Port:         10810,
				Device:       "/dev/sdc",
				DiskKey:      2001,
				VMDKPath:     "[datastore13] vm-b/disk-0.vmdk",
				SourceSerial: "wwn-def",
			},
		}
		if !slices.Equal(ac.Appliance.Status.Exports, want) {
			t.Errorf("exports = %+v, want %+v", ac.Appliance.Status.Exports, want)
		}
	})

	// Discovery runs once at startup and the containers come up one at a time,
	// so a short list is a normal thing to catch mid-flight. Recording it would
	// hand the migration a set of disks with one missing.
	t.Run("an appliance short of a disk is not done", func(t *testing.T) {
		ac := exportsContext(t, startAnnounce(t, applianceTLS().server, twoExports[:1]))

		done, err := ac.WaitForExports(context.TODO())

		if err != nil {
			t.Fatalf("WaitForExports: %v", err)
		}
		if done {
			t.Error("done = true, want an appliance short of a disk left waiting")
		}
		if ac.Appliance.Status.Exports != nil {
			t.Errorf("exports = %+v, want none recorded", ac.Appliance.Status.Exports)
		}
	})

	// Configure only established that systemd has the service running; the
	// endpoint binds after that. Failing here would fail a deploy that is on
	// track, and PhaseDeployFailed has no way back.
	t.Run("an appliance not answering yet is not a failure", func(t *testing.T) {
		ac := exportsContext(t, closedAddr(t))

		done, err := ac.WaitForExports(context.TODO())

		if err != nil {
			t.Fatalf("WaitForExports: %v", err)
		}
		if done {
			t.Error("done = true, want the appliance left to come up")
		}
	})

	// Something is answering at the appliance's address with a certificate the
	// controller cannot verify. That does not improve by waiting, and accepting
	// it would mean taking a migration's disk list from whatever replied.
	t.Run("an appliance that cannot be verified fails", func(t *testing.T) {
		otherKey, otherCA, _ := issue(nil, nil, "Unrelated CA", true, "")
		_, _, certPEM, keyPEM := leaf(otherCA, otherKey, announce.ServerName, x509.ExtKeyUsageServerAuth)
		impostor, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatalf("keypair: %v", err)
		}
		ac := exportsContext(t, startAnnounce(t, impostor, twoExports))

		done, err := ac.WaitForExports(context.TODO())

		if err == nil {
			t.Fatal("WaitForExports accepted a server it could not verify")
		}
		if done {
			t.Error("done = true, want nothing taken from an unverified server")
		}
	})

	t.Run("an appliance reporting no address fails", func(t *testing.T) {
		ac := exportsContext(t, closedAddr(t))
		ac.Appliance.Status.Addresses = nil

		_, err := ac.WaitForExports(context.TODO())

		if err == nil {
			t.Fatal("WaitForExports succeeded with no address to reach the appliance at")
		}
		if !errorMentions(t, err, ac.Appliance.Name) {
			t.Errorf("error = %q, want it to name the appliance", err)
		}
	})
}

// exportsContext is an appliance announcing at addr.
func exportsContext(t *testing.T, addr string) *ApplianceContext {
	t.Helper()
	ac := sshContext(t, nil, addr)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	ac.announcePortOverride = port
	return ac
}

// startAnnounce stands in for the appliance's orchestrator: the same endpoint,
// serving with the certificate it is given and requiring a client certificate
// from the appliance's CA.
func startAnnounce(t *testing.T, certificate tls.Certificate, exports []runner.Export) (addr string) {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    applianceTLS().pool,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /disks", func(w http.ResponseWriter, _ *http.Request) {
		if exports == nil {
			exports = []runner.Export{}
		}
		_ = json.NewEncoder(w).Encode(exports)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		_ = server.Serve(listener)
	}()
	return listener.Addr().String()
}

func TestDeployBegin(t *testing.T) {
	t.Run("deploy starts at clone", func(t *testing.T) {
		appliance := testAppliance()
		appliance.Status.TaskRef = "task-7"

		runner := DeployRunner{context: testContext(appliance, "uuid-a")}
		if err := runner.Begin(); err != nil {
			t.Fatalf("Begin: %v", err)
		}

		if appliance.Status.Phase != PhaseCloneVM {
			t.Errorf("phase = %q, want %q", appliance.Status.Phase, PhaseCloneVM)
		}
		if appliance.Status.TaskRef != "" {
			t.Errorf("task reference = %q, want it cleared", appliance.Status.TaskRef)
		}
	})

	// A moRef means nothing without the vCenter it was recorded against, and
	// the clone that follows is what produces one.
	t.Run("the connected vCenter is recorded", func(t *testing.T) {
		appliance := testAppliance()

		runner := DeployRunner{context: testContext(appliance, "uuid-a")}
		if err := runner.Begin(); err != nil {
			t.Fatalf("Begin: %v", err)
		}

		if appliance.Status.VCenterInstanceUUID != "uuid-a" {
			t.Errorf("recorded vCenter = %q, want %q", appliance.Status.VCenterInstanceUUID, "uuid-a")
		}
	})
}
