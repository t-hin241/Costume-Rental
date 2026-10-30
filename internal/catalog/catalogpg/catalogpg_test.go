package catalogpg_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"webee_rentor/internal/catalog"
	"webee_rentor/internal/catalog/catalogpg"
	"webee_rentor/internal/platform/postgres"
	"webee_rentor/internal/platform/postgres/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m, nil) }

func TestCatalogRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	c := f.Catalog()

	items, err := catalogpg.Items(ctx, f.Pool, []catalog.ItemID{c.Jacket.ID, c.Belt.ID, c.Jacket.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(items[c.Jacket.ID], c.Jacket) || !reflect.DeepEqual(items[c.Belt.ID], c.Belt) {
		t.Fatalf("items differ:\n got %+v\nwant %+v", items[c.Jacket.ID], c.Jacket)
	}
	b, err := catalogpg.Bundle(ctx, f.Pool, c.Set.ID)
	if err != nil || !reflect.DeepEqual(b, c.Set) {
		t.Fatalf("bundle differs (%v):\n got %+v\nwant %+v", err, b, c.Set)
	}
	p, err := catalogpg.Package(ctx, f.Pool, c.ThreeDays.ID)
	if err != nil || p != c.ThreeDays {
		t.Fatalf("package = %+v, %v", p, err)
	}
	if _, err := catalogpg.Items(ctx, f.Pool, []catalog.ItemID{"missing"}); !errors.Is(err, postgres.ErrNotFound) {
		t.Fatalf("missing item err = %v", err)
	}
}

func TestStaleItemVersionIsRejected(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	c := f.Catalog()
	newer := c.Jacket
	newer.Name, newer.Version = "Áo khoác mới", 2
	if err := catalogpg.SaveItem(ctx, f.Pool, newer, pgtest.Now); err != nil {
		t.Fatal(err)
	}
	if err := catalogpg.SaveItem(ctx, f.Pool, c.Jacket, pgtest.Now); !errors.Is(err, postgres.ErrStale) {
		t.Fatalf("saving version 1 over version 2: err = %v", err)
	}
}

// The composite keys stop a bundle option pointing at another item's
// variant even if a writer bypasses domain validation.
func TestBundleOptionMustBelongToComponentItem(t *testing.T) {
	ctx := context.Background()
	f := pgtest.New(t)
	c := f.Catalog()
	_, err := f.Pool.Exec(ctx, `INSERT INTO bundle_component_options (bundle_id, component_key, item_id, variant_id, position)
		VALUES ($1, 'jacket', $2, $3, 9)`, c.Set.ID, c.Jacket.ID, c.PantsM)
	if err == nil {
		t.Fatal("an option from another item must be rejected")
	}
}
