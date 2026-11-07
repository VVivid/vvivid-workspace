package account

import (
	"strings"
	"testing"
)

func TestExactProviderIdentity(t *testing.T) {
	for _, id := range []Identity{
		{Issuer: "http://issuer.example", Subject: "alice"},
		{Issuer: "https://user:password@issuer.example", Subject: "alice"},
		{Issuer: "https://issuer.example?query=1", Subject: "alice"},
		{Issuer: "https://issuer.example#fragment", Subject: "alice"},
		{Issuer: "https://issuer.example", Subject: ""},
		{Issuer: "https://issuer.example", Subject: " alice"},
		{Issuer: "https://issuer.example", Subject: "alice\x00bob"},
		{Issuer: "https://issuer.example", Subject: strings.Repeat("s", 256)},
	} {
		if id.Validate() == nil {
			t.Error("accepted an invalid provider identity")
		}
	}
	if (Identity{Issuer: "https://issuer.example/realms/agents", Subject: "synthetic-alice"}).Validate() != nil {
		t.Fatal("valid exact OIDC identity rejected")
	}
}
