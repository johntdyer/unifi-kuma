package unifi

import "testing"

func TestNetworkClient_GetIP(t *testing.T) {
	tests := []struct {
		name string
		c    NetworkClient
		want string
	}{
		{
			name: "fixed IP wins when set and in use",
			c:    NetworkClient{IP: "10.10.100.2", LastIP: "10.10.100.2", FixedIP: "10.222.222.2", UseFixedIP: true},
			want: "10.222.222.2",
		},
		{
			name: "fixed IP present but not in use is ignored",
			c:    NetworkClient{IP: "10.10.100.2", FixedIP: "10.222.222.2", UseFixedIP: false},
			want: "10.10.100.2",
		},
		{
			name: "use_fixedip true but fixed_ip empty falls through",
			c:    NetworkClient{IP: "10.10.100.2", FixedIP: "", UseFixedIP: true},
			want: "10.10.100.2",
		},
		{
			name: "no fixed IP, live ip wins over last_ip",
			c:    NetworkClient{IP: "10.10.100.2", LastIP: "10.10.100.5"},
			want: "10.10.100.2",
		},
		{
			name: "no fixed IP, offline client falls back to last_ip",
			c:    NetworkClient{LastIP: "10.10.100.5"},
			want: "10.10.100.5",
		},
		{
			name: "nothing set at all",
			c:    NetworkClient{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.GetIP(); got != tt.want {
				t.Errorf("GetIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
