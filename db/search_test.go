package db

import (
	"fmt"
	"testing"
	"time"
)

func TestSearchTasksAndAssetsByText(t *testing.T) {
	d, assets, _ := testSetup(t)
	t.Cleanup(func() { d.Close() })

	stamp := time.Now().UnixNano()
	marker := fmt.Sprintf("palette-%d", stamp)
	task, err := d.CreateTask("任务 "+marker, "目标 "+marker, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)

	assetID, err := assets.UpsertRootDomain(UpsertRootDomainReq{
		Domain: fmt.Sprintf("%s.example", marker),
		TaskID: task.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer deleteAsset(d, assetID)

	tasks, err := d.SearchTasksByText(marker, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("task search = %+v, want task %d", tasks, task.ID)
	}

	found, err := assets.SearchAssetsByText(marker, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != assetID {
		t.Fatalf("asset search = %+v, want asset %d", found, assetID)
	}

	// LIKE metacharacters are literal text, not wildcards.
	if _, err := d.SearchTasksByText("%", 5); err != nil {
		t.Fatalf("literal %% search: %v", err)
	}
}
