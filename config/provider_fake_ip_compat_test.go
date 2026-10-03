package config

import (
	"net"
	"testing"
)

func TestProviderEndpointSupportsMihomoDualStackFakeIP(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		ip      string
		wantErr bool
	}{
		{name: "current IPv6 fake IP", host: "generativelanguage.googleapis.com", ip: "2001:2::141"},
		{name: "current IPv4 fake IP", host: "generativelanguage.googleapis.com", ip: "198.18.1.189"},
		{name: "existing IPv6 fake IP", host: "generativelanguage.googleapis.com", ip: "fdfe:dcba:9876::141"},
		{name: "literal IPv6 unchanged", host: "2001:2::141", ip: "2001:2::141", wantErr: true},
		{name: "neighboring IPv6 range unchanged", host: "generativelanguage.googleapis.com", ip: "2001:2:0:1::141", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProviderResolvedEndpointAccess(tt.host, net.ParseIP(tt.ip), ProviderPrivateNetworkAccess{})
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolved provider endpoint error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}
