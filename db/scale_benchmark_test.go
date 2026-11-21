package db

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func openBenchDB(b *testing.B) *DB {
	b.Helper()
	if os.Getenv("BLADE_PG_DSN") == "" {
		b.Skip("BLADE_PG_DSN is required for scale benchmarks")
	}
	d, err := Open(os.Getenv("BLADE_PG_DSN"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { d.Close() })
	return d
}
func benchAssets(b *testing.B, size int) {
	d := openBenchDB(b)
	marker := fmt.Sprintf("bench-assets-%d-%d", size, time.Now().UnixNano())
	if _, err := d.Exec(`INSERT INTO assets(type,ip,provider,last_seen) SELECT 'ip','2001:db8:'||right(md5(i::text),4)||'::'||to_hex(i),$1,now()-make_interval(secs=>i) FROM generate_series(1,$2) i`, marker, size); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _, _ = d.Exec(`DELETE FROM assets WHERE provider=$1`, marker) })
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		count := 0
		if err := d.ReadSnapshot(context.Background(), func(view *DB) error {
			return view.Assets().IterateFiltered(context.Background(), AssetFilter{Provider: marker}, func(*Asset) error { count++; return nil })
		}); err != nil {
			b.Fatal(err)
		}
		if count != size {
			b.Fatalf("scanned %d want %d", count, size)
		}
	}
}
func BenchmarkAssetExportSnapshot(b *testing.B) {
	for _, size := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("assets_%d", size), func(b *testing.B) { benchAssets(b, size) })
	}
}
func BenchmarkFindingPages(b *testing.B) {
	d := openBenchDB(b)
	task, err := d.CreateTask("scale findings", "benchmark", nil, 0, 0)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _, _ = d.Exec(`DELETE FROM findings WHERE task_id=$1`, task.ID); _ = d.DeleteTask(task.ID) })
	if _, err := d.Exec(`INSERT INTO findings(task_id,vulnclass,name,severity,summary,evidence,worker,asset_ids) SELECT $1,'bench','finding-'||i,'medium','s','e','bench','[]' FROM generate_series(1,10000) i`, task.ID); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, _, err := d.ListFindingsPage(FindingFilter{TaskID: fmt.Sprint(task.ID)}, 1, 50); err != nil {
			b.Fatal(err)
		}
		if _, _, err := d.ListFindingCaseRows(FindingFilter{TaskID: fmt.Sprint(task.ID)}, 1, 50); err != nil {
			b.Fatal(err)
		}
	}
}
