package archive

import "context"

func (c *Catalog) currentUsageEstimation(ctx context.Context) error {
	key, err := c.usageEstimationKey(ctx)
	if err != nil {
		return err
	}
	var pending bool
	if err := c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM usage_estimation_inputs WHERE revision<>built_revision OR estimator_key<>?)`, key).Scan(&pending); err != nil {
		return err
	}
	if !pending {
		return nil
	}
	var deferred bool
	if err := c.reader().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sync_deferred WHERE name='tools' AND generation>completed_generation)").Scan(&deferred); err != nil {
		return err
	}
	if !deferred {
		c.refreshToolRollup()
	}
	return nil
}
