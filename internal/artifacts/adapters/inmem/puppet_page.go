package inmem

import "context"

func (p packages) VersionNames(ctx context.Context, packageID int64) ([]string, error) {
	var out []string
	err := p.s.Do(ctx, func() error {
		for _, v := range p.sorted(packageID) {
			out = append(out, v.Version)
		}
		return nil
	})
	return out, err
}
