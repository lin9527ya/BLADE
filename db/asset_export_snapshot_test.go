package db

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAssetExportIteratorUsesOneSnapshotAcrossMutation(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	marker := fmt.Sprintf("export-snapshot-%d", time.Now().UnixNano())
	network := fmt.Sprintf("%x", uint64(time.Now().UnixNano())&0xffff)
	_, err = d.Exec(`INSERT INTO assets(type,ip,provider) SELECT 'ip','2001:db8:'||$2||'::'||to_hex(g),$1 FROM generate_series(1,2100) g`, marker, network)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM assets WHERE provider=$1`, marker) })
	var lastID int64
	if err := d.QueryRow(`SELECT min(id) FROM assets WHERE provider=$1`, marker).Scan(&lastID); err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	err = d.ReadSnapshot(t.Context(), func(view *DB) error {
		return view.Assets().IterateFiltered(t.Context(), AssetFilter{Provider: marker}, func(a *Asset) error {
			if seen[a.ID] {
				return fmt.Errorf("duplicate id %d", a.ID)
			}
			seen[a.ID] = true
			if len(seen) == 1000 {
				if _, err := d.Exec(`UPDATE assets SET last_seen='2099-01-01' WHERE id=$1`, lastID); err != nil {
					return err
				}
			}
			if a.ID == lastID && strings.HasPrefix(a.LastSeen, "2099") {
				return fmt.Errorf("mixed revisions in snapshot")
			}
			return nil
		})
	})
	if err != nil || len(seen) != 2100 {
		t.Fatalf("snapshot rows=%d: %v", len(seen), err)
	}
}
