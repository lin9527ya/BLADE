package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

// GlobalSearchHit is one navigable result in the command palette.
type GlobalSearchHit struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	URL      string `json:"url"`
}

type GlobalSearchResponse struct {
	Tasks     []GlobalSearchHit `json:"tasks"`
	Findings  []GlobalSearchHit `json:"findings"`
	Assets    []GlobalSearchHit `json:"assets"`
	CVEs      []GlobalSearchHit `json:"cves"`
	Playbooks []GlobalSearchHit `json:"playbooks"`
}

func searchClip(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// globalSearch is the read-only fan-out behind Cmd/Ctrl+J. Every group has its
// own cap; a slow group cannot hold the palette hostage (700ms budget), and the
// browser can still navigate to whatever finished.
func (s *Server) globalSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := atoiDefault(r.URL.Query().Get("limit"), 5)
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	out := GlobalSearchResponse{
		Tasks:     []GlobalSearchHit{},
		Findings:  []GlobalSearchHit{},
		Assets:    []GlobalSearchHit{},
		CVEs:      []GlobalSearchHit{},
		Playbooks: []GlobalSearchHit{},
	}
	if q == "" || len([]rune(q)) > 200 || s.m == nil || s.m.PG() == nil {
		writeJSON(w, 200, out)
		return
	}

	ctx := r.Context()
	type result struct {
		label string
		hits  []GlobalSearchHit
	}
	ch := make(chan result, 5)
	go func() {
		tasks, err := s.m.PG().SearchTasksByText(q, limit)
		if err != nil {
			ch <- result{label: "tasks"}
			return
		}
		hits := make([]GlobalSearchHit, 0, len(tasks))
		for _, t := range tasks {
			title := strings.TrimSpace(t.Name)
			if title == "" {
				title = searchClip(t.Description, 80)
			}
			if title == "" {
				title = "任务 #" + i64s(t.ID)
			}
			subtitle := t.Status
			if t.Paused {
				subtitle += " · 已暂停"
			} else if t.Queued {
				subtitle += " · 排队中"
			}
			if desc := searchClip(t.Description, 100); desc != "" && desc != title {
				subtitle += " · " + desc
			}
			hits = append(hits, GlobalSearchHit{
				ID:       i64s(t.ID),
				Title:    title,
				Subtitle: subtitle,
				URL:      "/function/tasks/detail?id=" + url.QueryEscape(i64s(t.ID)),
			})
		}
		ch <- result{label: "tasks", hits: hits}
	}()
	go func() {
		items, err := s.m.PG().SearchChatMentions(ctx, "finding", q)
		if err != nil {
			ch <- result{label: "findings"}
			return
		}
		hits := make([]GlobalSearchHit, 0, len(items))
		for _, item := range items {
			if len(hits) >= limit {
				break
			}
			hits = append(hits, GlobalSearchHit{
				ID:       i64s(item.ID),
				Title:    item.Label,
				Subtitle: item.Description,
				URL:      "/function/findings/detail?id=" + url.QueryEscape(i64s(item.ID)),
			})
		}
		ch <- result{label: "findings", hits: hits}
	}()
	go func() {
		items, err := s.m.PG().SearchChatMentions(ctx, "asset", q)
		if err != nil {
			ch <- result{label: "assets"}
			return
		}
		hits := make([]GlobalSearchHit, 0, len(items))
		for _, item := range items {
			if len(hits) >= limit {
				break
			}
			hits = append(hits, GlobalSearchHit{
				ID:       i64s(item.ID),
				Title:    item.Label,
				Subtitle: item.Description,
				URL:      "/function/assets/detail?id=" + url.QueryEscape(i64s(item.ID)),
			})
		}
		ch <- result{label: "assets", hits: hits}
	}()
	go func() {
		cves, _, err := s.m.PG().Assets().ListCVEs(db.CVEFilter{Q: q, Status: "all", Limit: limit})
		if err != nil {
			ch <- result{label: "cves"}
			return
		}
		hits := make([]GlobalSearchHit, 0, len(cves))
		for _, c := range cves {
			hits = append(hits, GlobalSearchHit{
				ID:       i64s(c.ID),
				Title:    fmt.Sprintf("%s · %s", c.CVEID, c.Title),
				Subtitle: strings.TrimSpace(c.Severity + " · " + searchClip(c.Description, 100)),
				URL:      "/function/cve?q=" + url.QueryEscape(c.CVEID),
			})
		}
		ch <- result{label: "cves", hits: hits}
	}()
	go func() {
		books, _, err := s.m.PG().Assets().ListPlaybooks(db.PlaybookFilter{Q: q, Status: "active", Limit: limit})
		if err != nil {
			ch <- result{label: "playbooks"}
			return
		}
		hits := make([]GlobalSearchHit, 0, len(books))
		for _, b := range books {
			hits = append(hits, GlobalSearchHit{
				ID:       i64s(b.ID),
				Title:    b.Title,
				Subtitle: searchClip(b.Summary, 120),
				URL:      "/function/playbooks?q=" + url.QueryEscape(b.Title),
			})
		}
		ch <- result{label: "playbooks", hits: hits}
	}()

	timer := time.NewTimer(700 * time.Millisecond)
	defer timer.Stop()
collect:
	for i := 0; i < 5; i++ {
		select {
		case res := <-ch:
			switch res.label {
			case "tasks":
				out.Tasks = res.hits
			case "findings":
				out.Findings = res.hits
			case "assets":
				out.Assets = res.hits
			case "cves":
				out.CVEs = res.hits
			case "playbooks":
				out.Playbooks = res.hits
			}
		case <-timer.C:
			break collect
		}
	}
	writeJSON(w, 200, out)
}
