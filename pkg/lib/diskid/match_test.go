package diskid

import "testing"

func TestMatch(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{
			name: "vmware uuid vs scsi_id",
			a:    "6000C297-7d53-fad7-e8b4-5194193802f7",
			b:    "36000c2977d53fad7e8b45194193802f7",
			want: true,
		},
		{
			name: "vmware uuid vs sysfs wwn",
			a:    "6000C297-7d53-fad7-e8b4-5194193802f7",
			b:    "0x5000c2977d53fad7",
			want: true,
		},
		{
			name: "scsi_id vs naa prefix",
			a:    "36000c2902b72f55a2146435072abcdef01",
			b:    "naa.5000c2902b72f55a",
			want: true,
		},
		{
			name: "different disks",
			a:    "6000C297-7d53-fad7-e8b4-5194193802f7",
			b:    "6000C2902b72f55a-2146-4350-72ab-cdef01234567",
			want: false,
		},
		{
			name: "empty",
			a:    "",
			b:    "6000C297-7d53-fad7-e8b4-5194193802f7",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Match(tt.a, tt.b); got != tt.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
