package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/big"
	"slices"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

// Section is the descriptor section, <quotaConfig> in artifactory.xsd.
const Section = "quotaConfig"

// The section's fields, in QuotaConfigDescriptor's propOrder.
const (
	FieldEnabled = "enabled"
	FieldLimit   = "diskSpaceLimitPercentage"
	FieldWarning = "diskSpaceWarningPercentage"
)

// Fields are the section's fields in XSD order.
var Fields = []string{FieldEnabled, FieldLimit, FieldWarning}

// The policy is one configuration document of this package's own kind: only
// the descriptor writes it and only this package reads it back.
const (
	ConfigKind configdoc.ConfigKind = "storage_quota"
	ConfigKey  string               = "default"
)

// Policy is the effective quotaConfig. A field the stored section omits has
// the XSD default (false, 0, 0), which is also 7.x's
// QuotaConfigDescriptor field default: an enabled policy without a limit
// refuses every positive write, as 7.x's "used >= 0" check does.
type Policy struct {
	Enabled               bool
	LimitPercent, Warning int
}

// Level is a measured state against a Policy.
type Level int

// The levels, ordered: a Limit is also past the warning threshold when the
// warning percentage is not above the limit's.
const (
	Below Level = iota
	Warning
	Limit
)

func (l Level) String() string {
	switch l {
	case Warning:
		return "warning"
	case Limit:
		return "limit"
	default:
		return "below"
	}
}

// Level compares a measurement with the policy exactly as 7.x's
// StorageQuotaInfo does (used / total * 100 >= percentage, limit first), in
// integers so a threshold on the edge is never decided by rounding.
func (p Policy) Level(total, used int64) Level {
	switch {
	case reached(total, used, p.LimitPercent):
		return Limit
	case reached(total, used, p.Warning):
		return Warning
	default:
		return Below
	}
}

func reached(total, used int64, percent int) bool {
	left := new(big.Int).Mul(big.NewInt(used), big.NewInt(100))
	right := new(big.Int).Mul(big.NewInt(total), big.NewInt(int64(percent)))
	return left.Cmp(right) >= 0
}

// CheckFields validates a quotaConfig mapping, a patch's or the merged stored
// one, and returns the policy it describes. Unknown fields, nulls and values
// of the wrong type are refused; percentages are whole numbers from 0 to 100.
// The XSD types them xs:int and 7.x's descriptor does not range-check them,
// but a percentage outside 0..100 has no meaning for a volume, so accepting
// one would store a setting whose effect nobody could predict.
func CheckFields(doc map[string]any) (Policy, error) {
	var p Policy
	for _, k := range slices.Sorted(maps.Keys(doc)) {
		where := Section + "." + k
		v := doc[k]
		if !slices.Contains(Fields, k) {
			return Policy{}, fmt.Errorf("%w: %s: unknown field %s", configapi.ErrInvalid, Section, k)
		}
		if v == nil {
			return Policy{}, fmt.Errorf("%w: %s cannot be null", configapi.ErrInvalid, where)
		}
		if k == FieldEnabled {
			b, ok := v.(bool)
			if !ok {
				return Policy{}, fmt.Errorf("%w: %s must be a boolean", configapi.ErrInvalid, where)
			}
			p.Enabled = b
			continue
		}
		n, ok := percent(v)
		if !ok {
			return Policy{}, fmt.Errorf("%w: %s must be a whole percentage from 0 to 100", configapi.ErrInvalid, where)
		}
		if k == FieldLimit {
			p.LimitPercent = n
		} else {
			p.Warning = n
		}
	}
	return p, nil
}

// percent reads a YAML integer (int), a stored JSON number (float64) or an
// exported one (json.Number). A string such as "95" or 95.5 is refused rather
// than rounded.
func percent(v any) (int, bool) {
	var f float64
	switch n := v.(type) {
	case int:
		f = float64(n)
	case float64:
		f = n
	case json.Number:
		parsed, err := n.Float64()
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if f != math.Trunc(f) || f < 0 || f > 100 {
		return 0, false
	}
	return int(f), true
}

// Read returns the stored policy and whether one is stored, joining ctx's
// transaction. Nothing stored is the XSD's absent section: disabled.
func Read(ctx context.Context, configs configdoc.ConfigStore) (Policy, bool, error) {
	d, err := configs.Get(ctx, ConfigKind, ConfigKey)
	if errors.Is(err, kernel.ErrNotFound) {
		return Policy{}, false, nil
	}
	if err != nil {
		return Policy{}, false, fmt.Errorf("read %s: %w", Section, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(d.Document, &doc); err != nil || doc == nil {
		return Policy{}, false, fmt.Errorf("decode stored %s: %w", Section, kernel.ErrStore)
	}
	p, err := CheckFields(doc)
	if err != nil {
		return Policy{}, false, fmt.Errorf("stored %s: %w", Section, kernel.ErrStore)
	}
	return p, true, nil
}
