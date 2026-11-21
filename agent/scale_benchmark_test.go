package agent

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func BenchmarkBlackboardViewScale(b *testing.B) {
	if os.Getenv("BLADE_PG_DSN") == "" {
		b.Skip("BLADE_PG_DSN is required for scale benchmarks")
	}
	d, err := db.Open(os.Getenv("BLADE_PG_DSN"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { d.Close() })
	for _, size := range []int{1000, 5000} {
		b.Run(fmt.Sprintf("facts_%d", size), func(b *testing.B) {
			task, err := d.CreateTask(fmt.Sprintf("blackboard-bench-%d-%d", size, time.Now().UnixNano()), "benchmark", nil, 0, 0)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = d.DeleteTask(task.ID) })
			if _, err := d.Exec(`INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin)
        SELECT $1,'fact',jsonb_build_object('summary','fact '||i,'confidence','high','polarity','positive'),1,'confirmed','bench' FROM generate_series(1,$2) i`, task.ExplorationID, size); err != nil {
				b.Fatal(err)
			}
			ts := NewToolSet(d.Exploration(task.ExplorationID), "planner")
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if out := ts.blackboardViewData(0, 20); out == nil {
					b.Fatal("empty projection")
				}
			}
		})
	}
}
