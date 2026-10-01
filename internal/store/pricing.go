package store

import (
	"context"
	"database/sql"
	"time"
)

// ModelPrices returns the whole price table, ordered by key.
func (d *DB) ModelPrices(ctx context.Context) ([]ModelPrice, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT model_key,input_per_m,output_per_m,cache_read_per_m,cache_write_per_m,source,updated_at
		 FROM model_prices ORDER BY model_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelPrice{}
	for rows.Next() {
		var p ModelPrice
		if err := rows.Scan(&p.ModelKey, &p.InputPerM, &p.OutputPerM, &p.CacheReadPerM,
			&p.CacheWritePerM, &p.Source, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertModelPrice writes one price. A manual row is never overwritten by a sync.
func (d *DB) UpsertModelPrice(ctx context.Context, p ModelPrice, replaceManual bool) error {
	if p.UpdatedAt == 0 {
		p.UpdatedAt = time.Now().UnixMilli()
	}
	q := `INSERT INTO model_prices
	      (model_key,input_per_m,output_per_m,cache_read_per_m,cache_write_per_m,source,updated_at)
	      VALUES (?,?,?,?,?,?,?)
	      ON CONFLICT(model_key) DO UPDATE SET
	        input_per_m=excluded.input_per_m, output_per_m=excluded.output_per_m,
	        cache_read_per_m=excluded.cache_read_per_m, cache_write_per_m=excluded.cache_write_per_m,
	        source=excluded.source, updated_at=excluded.updated_at`
	if !replaceManual {
		q += ` WHERE model_prices.source <> 'manual'`
	}
	_, err := d.w.ExecContext(ctx, q, p.ModelKey, p.InputPerM, p.OutputPerM, p.CacheReadPerM,
		p.CacheWritePerM, p.Source, p.UpdatedAt)
	return err
}

// UpsertModelPrices writes a batch inside one transaction (catalog sync path).
func (d *DB) UpsertModelPrices(ctx context.Context, prices []ModelPrice) error {
	if len(prices) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`INSERT INTO model_prices
			 (model_key,input_per_m,output_per_m,cache_read_per_m,cache_write_per_m,source,updated_at)
			 VALUES (?,?,?,?,?,?,?)
			 ON CONFLICT(model_key) DO UPDATE SET
			   input_per_m=excluded.input_per_m, output_per_m=excluded.output_per_m,
			   cache_read_per_m=excluded.cache_read_per_m, cache_write_per_m=excluded.cache_write_per_m,
			   source=excluded.source, updated_at=excluded.updated_at
			 WHERE model_prices.source <> 'manual'`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, p := range prices {
			if _, err := stmt.ExecContext(ctx, p.ModelKey, p.InputPerM, p.OutputPerM, p.CacheReadPerM,
				p.CacheWritePerM, p.Source, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteModelPrice removes one price row.
func (d *DB) DeleteModelPrice(ctx context.Context, modelKey string) error {
	_, err := d.w.ExecContext(ctx, `DELETE FROM model_prices WHERE model_key = ?`, modelKey)
	return err
}

// PriceRules returns every per-model multiplier rule.
func (d *DB) PriceRules(ctx context.Context) ([]PriceRule, error) {
	rows, err := d.r.QueryContext(ctx,
		`SELECT model_key,input_mult,output_mult,cache_read_mult,cache_write_mult,disabled
		 FROM price_rules ORDER BY model_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PriceRule{}
	for rows.Next() {
		var r PriceRule
		var disabled int64
		if err := rows.Scan(&r.ModelKey, &r.InputMult, &r.OutputMult, &r.CacheReadMult,
			&r.CacheWriteMult, &disabled); err != nil {
			return nil, err
		}
		r.Disabled = disabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertPriceRule writes one rule.
func (d *DB) UpsertPriceRule(ctx context.Context, r PriceRule) error {
	disabled := 0
	if r.Disabled {
		disabled = 1
	}
	_, err := d.w.ExecContext(ctx,
		`INSERT INTO price_rules (model_key,input_mult,output_mult,cache_read_mult,cache_write_mult,disabled)
		 VALUES (?,?,?,?,?,?)
		 ON CONFLICT(model_key) DO UPDATE SET
		   input_mult=excluded.input_mult, output_mult=excluded.output_mult,
		   cache_read_mult=excluded.cache_read_mult, cache_write_mult=excluded.cache_write_mult,
		   disabled=excluded.disabled`,
		r.ModelKey, r.InputMult, r.OutputMult, r.CacheReadMult, r.CacheWriteMult, disabled)
	return err
}

// RepriceableEvent is the minimum an event costs to re-price: its id plus the buckets.
type RepriceableEvent struct {
	ID         int64
	Model      string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// RepriceableEvents returns one page of events whose cost came from the price table, in total
// control.
//
// It selects every row that is NOT reported, which includes rows currently marked
// "unavailable": adding a price for a model that had none has to be able to price those rows,
// and they are the whole reason a user opens the pricing page.
//
// Passing afterID = 0 starts from the beginning; the returned nextID advances the cursor.
func (d *DB) RepriceableEvents(ctx context.Context, afterID int64, limit int) ([]RepriceableEvent, int64, error) {
	if limit <= 0 {
		limit = 5000
	}
	rows, err := d.r.QueryContext(ctx,
		`SELECT id, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
		 FROM usage_events WHERE cost_source <> ? AND id > ? ORDER BY id LIMIT ?`,
		CostSourceReported, afterID, limit)
	if err != nil {
		return nil, afterID, err
	}
	defer rows.Close()
	out := []RepriceableEvent{}
	next := afterID
	for rows.Next() {
		var e RepriceableEvent
		if err := rows.Scan(&e.ID, &e.Model, &e.Input, &e.Output, &e.CacheRead, &e.CacheWrite); err != nil {
			return nil, afterID, err
		}
		out = append(out, e)
		next = e.ID
	}
	return out, next, rows.Err()
}

// CountRepriceable counts the events whose cost can come from the price table.
func (d *DB) CountRepriceable(ctx context.Context) (int64, error) {
	var n int64
	err := d.r.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_events WHERE cost_source <> ?`, CostSourceReported).Scan(&n)
	return n, err
}

// UpdateEventCosts applies new costs. A nil cost downgrades the row to unavailable,
// which is what an unpriced model must look like rather than a zero.
func (d *DB) UpdateEventCosts(ctx context.Context, updates map[int64]*float64) error {
	if len(updates) == 0 {
		return nil
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx,
			`UPDATE usage_events SET cost_usd = ?, cost_source = ? WHERE id = ?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for id, cost := range updates {
			source := CostSourceUnavailable
			if cost != nil {
				source = CostSourceEstimated
			}
			if _, err := stmt.ExecContext(ctx, cost, source, id); err != nil {
				return err
			}
		}
		return nil
	})
}
