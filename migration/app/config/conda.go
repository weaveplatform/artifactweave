package config

// Conda controls optional CEP-16 metadata; classic indexes remain available.
type Conda struct {
	ShardsEnabled bool `yaml:"shards_enabled"`
}
