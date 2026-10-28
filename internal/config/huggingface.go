package config

// HuggingFace controls the opt-in native Xet transfer protocol.
type HuggingFace struct {
	XetEnabled bool `yaml:"xet_enabled"`
}
