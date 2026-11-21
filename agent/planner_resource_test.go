package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lin9527ya/Norma/llm"
	actool "github.com/lin9527ya/Norma/tool"
)

type probePlannerProvider struct {
	recordingProvider
	turns int
	fail  bool
}

func (p *probePlannerProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if p.fail {
		return llm.Message{}, "", llm.Usage{}, errors.New("injected provider failure")
	}
	p.turns++
	if p.turns%2 == 1 {
		return toolUseMsg(fmt.Sprint(p.turns), "resource_probe", "{}"), "tool_use", llm.Usage{}, nil
	}
	return assistantMsg("probe completed"), "end_turn", llm.Usage{}, nil
}

func TestPlannerHotSessionOwnsToolResources(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("hot resources", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	old := CurrentOverrides()
	t.Cleanup(func() { SetOverrides(old) })
	type resource struct{ closes, calls atomic.Int32 }
	var resources []*resource
	o := old
	o.ToolResolve = nil
	var p *Planner
	o.ToolAugment = func(ctx context.Context, key string) ([]actool.CoreTool, DeferredInfo, func()) {
		r := &resource{}
		resources = append(resources, r)
		probe := readTool("resource_probe", "test owned resource", obj(nil), func(ctx context.Context, _ json.RawMessage) (actool.Result, error) {
			if r.closes.Load() != 0 {
				return actool.Errorf("resource already closed"), nil
			}
			r.calls.Add(1)
			// Populate the ToolSet actually bound to the retained registry. Its
			// transport must be released at the end of every actual round.
			if p.hotTools != nil {
				if _, err := p.hotTools.buildHTTPClient(nil, true, true, time.Second); err != nil {
					return actool.Errorf(err.Error()), nil
				}
			}
			return actool.Text("alive"), nil
		})
		return []actool.CoreTool{probe}, DeferredInfo{}, func() { r.closes.Add(1) }
	}
	SetOverrides(o)
	prov := &probePlannerProvider{}
	p = NewPlanner(prov, "fixture", t.TempDir(), nil, 10000, 4)
	p.SetNonStreaming(func() bool { return true })
	t.Cleanup(p.CloseHotSession)
	for i := 0; i < 2; i++ {
		if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), d.Exploration(task.ExplorationID), "fixture", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(resources) != 2 || resources[0].calls.Load() != 2 || resources[0].closes.Load() != 0 || resources[1].closes.Load() != 1 {
		t.Fatal("hot registry lost resources or unused resources leaked")
	}
	if p.hotTools.httpPool == nil || len(p.hotTools.httpPool.transports) != 0 {
		t.Fatal("actual hot HTTP pool not cleaned")
	}
	p.CloseHotSession()
	p.CloseHotSession()
	if resources[0].closes.Load() != 1 || resources[1].closes.Load() != 1 {
		t.Fatal("cleanup not exactly once")
	}

	if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), d.Exploration(task.ExplorationID), "fixture", nil, nil); err != nil {
		t.Fatal(err)
	}
	p.hotLastUsed = time.Now().Add(-plannerHotIdleTTL - time.Second)
	if session, _, _, _, _ := p.takeHotSession(); session != nil || resources[2].closes.Load() != 1 {
		t.Fatal("TTL did not release owned resources")
	}
	if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), d.Exploration(task.ExplorationID), "fixture", nil, nil); err != nil {
		t.Fatal(err)
	}
	prov.fail = true
	if _, _, err := p.Plan(t.Context(), task.ID, d.Assets(), d.Exploration(task.ExplorationID), "fixture", nil, nil); err == nil {
		t.Fatal("injected failure was swallowed")
	}
	if p.hot != nil || len(resources) != 5 || resources[3].closes.Load() != 1 || resources[4].closes.Load() != 1 {
		t.Fatal("failed hot round did not release both owned and unused resources")
	}
}
