package config

import (
	"os"
	"testing"
)

func TestSIPTrunkIsOffByDefault(t *testing.T) {
	if got := Default().SIPTrunkAddress; got != "" {
		t.Fatalf("default trunk address %q, want empty so the listener stays off", got)
	}
}

func TestSIPTrunkEnvironmentOverrides(t *testing.T) {
	t.Setenv("VOCAT_SIP_TRUNK_ADDR", "127.0.0.1:5062")
	t.Setenv("VOCAT_SIP_TRUNK_PEERS", "127.0.0.1, 10.8.0.0/24 ,")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SIPTrunkAddress != "127.0.0.1:5062" {
		t.Fatalf("address %q", cfg.SIPTrunkAddress)
	}
	// The trailing comma must not become an unnamed peer: an empty entry in
	// the allow list would be a silent configuration error.
	want := []string{"127.0.0.1", "10.8.0.0/24"}
	if len(cfg.SIPTrunkPeers) != len(want) {
		t.Fatalf("peers %q, want %q", cfg.SIPTrunkPeers, want)
	}
	for index := range want {
		if cfg.SIPTrunkPeers[index] != want[index] {
			t.Fatalf("peers %q, want %q", cfg.SIPTrunkPeers, want)
		}
	}
}

func TestSIPTrunkFromConfigFile(t *testing.T) {
	path := t.TempDir() + "/vocat.json"
	body := `{"sip_trunk_address":"127.0.0.1:5062","sip_trunk_peers":["172.20.0.2"]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VOCAT_CONFIG", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SIPTrunkAddress != "127.0.0.1:5062" ||
		len(cfg.SIPTrunkPeers) != 1 || cfg.SIPTrunkPeers[0] != "172.20.0.2" {
		t.Fatalf("file config not applied: %q %q", cfg.SIPTrunkAddress, cfg.SIPTrunkPeers)
	}
}
