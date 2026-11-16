package server

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/lin9527ya/blade/db"
)

type CompanyOverviewTask struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Findings    int    `json:"findings"`
	Footholds   int    `json:"footholds"`
	Credentials int    `json:"credentials"`
	Detections  int    `json:"detections"`
}

type CompanyOverview struct {
	Company            *db.CompanyWithScope       `json:"company"`
	Scope              []db.ScopeRule             `json:"scope"`
	AssetCounts        map[string]int             `json:"asset_counts"`
	AssetTotal         int                        `json:"asset_total"`
	Tasks              []CompanyOverviewTask      `json:"tasks"`
	Findings           *db.CompanyFindingSummary  `json:"findings"`
	RecentFindings     []FindingDTO               `json:"recent_findings"`
	LootByKind         map[string]int             `json:"loot_by_kind"`
	LootTotal          int                        `json:"loot_total"`
	CredentialAttempts int                        `json:"credential_attempts"`
	Knowledge          []db.CompanyKnowledge      `json:"knowledge"`
	Deliverables       []*db.Deliverable          `json:"deliverables"`
	CVEs               []db.CVEMatch              `json:"cves"`
	Playbooks          []db.PlaybookMatch         `json:"playbooks"`
	Activity           []db.CompanyRecentActivity `json:"activity"`
}

// companyOverview is the 360 read model: company/scope, assets, tasks, findings,
// loot, knowledge, deliverables and top CVE/playbook clues. It composes existing
// stores instead of creating a parallel company entity.
func (s *Server) companyOverview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad company id")
		return
	}
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	company, err := pg.Companies().GetCompany(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if company == nil {
		writeErr(w, 404, "company not found")
		return
	}
	scope, err := pg.Companies().GetScope(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	assets := pg.Assets()
	counts, err := assets.CountsByTypeForCompany(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	assetTotal := 0
	for _, n := range counts {
		assetTotal += n
	}

	tasks := make([]CompanyOverviewTask, 0)
	for _, t := range s.m.List() {
		matches := false
		for _, cid := range t.CompanyIDs {
			if cid == id {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		name := t.Name
		if name == "" {
			name = t.Description
		}
		item := CompanyOverviewTask{
			ID: t.ID, Name: name, Description: t.Description, Status: s.resolvedTaskStatus(t),
		}
		item.Findings, _ = t.Store.CountByKind(db.KindFinding)
		item.Footholds, _ = t.Store.CountByKind(db.KindFoothold)
		item.Credentials, _ = t.Store.CountByKind(db.KindCredential)
		item.Detections, _ = t.Store.CountByKind(db.KindDetection)
		tasks = append(tasks, item)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID > tasks[j].ID })

	findings, err := pg.CompanyFindings(id, 20)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	recentFindings := make([]FindingDTO, 0, len(findings.Recent))
	assetMap := s.resolveFindingAssets(findings.Recent)
	for _, f := range findings.Recent {
		recentFindings = append(recentFindings, findingFromDB(f, assetMap))
	}
	// The raw DB rows are an internal shape; the API exposes the DTO list above.
	findings.Recent = nil

	lootByKind := map[string]int{}
	lootTotal := 0
	for _, kind := range []string{"foothold", "credential", "artifact", "footprint"} {
		_, total, err := pg.ListLootLedger(r.Context(), db.LootFilter{CompanyID: id, Kind: kind}, 1, 1)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		lootByKind[kind] = total
		lootTotal += total
	}
	_, credentialAttempts, err := pg.ListCredentialAttemptsPage(r.Context(), db.CredentialAttemptFilter{CompanyID: id}, 1, 1)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	knowledge, err := assets.ListCompanyKnowledgeAll(id, 100)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	deliverables, err := pg.ListDeliverables()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	companyDeliverables := make([]*db.Deliverable, 0)
	for _, item := range deliverables {
		if item.CompanyID != nil && *item.CompanyID == id {
			companyDeliverables = append(companyDeliverables, item)
		}
	}
	activity, err := pg.CompanyRecentActivity(id, 30)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	cves := companyCVEHints(assets, id)
	playbooks := companyPlaybookHints(tasks, assets)
	companyWithScope := &db.CompanyWithScope{Company: *company, Scope: scope, AssetCount: assetTotal}
	out := CompanyOverview{
		Company: companyWithScope, Scope: scope, AssetCounts: counts, AssetTotal: assetTotal,
		Tasks: tasks, Findings: findings, RecentFindings: recentFindings,
		LootByKind: lootByKind, LootTotal: lootTotal, CredentialAttempts: credentialAttempts,
		Knowledge: knowledge, Deliverables: companyDeliverables,
		CVEs: cves, Playbooks: playbooks, Activity: activity,
	}
	writeJSON(w, 200, out)
}

func companyCVEHints(assets *db.AssetStore, companyID int64) []db.CVEMatch {
	list, err := assets.QueryByCompany(companyID, "", 200, 0)
	if err != nil {
		return []db.CVEMatch{}
	}
	out := []db.CVEMatch{}
	seen := map[string]bool{}
	for _, asset := range list {
		matches, err := assets.MatchCVEsForAsset(0, asset.ID, 5)
		if err != nil {
			continue
		}
		for _, match := range matches {
			if seen[match.CVEID] {
				continue
			}
			seen[match.CVEID] = true
			out = append(out, match)
			if len(out) >= 20 {
				return out
			}
		}
	}
	return out
}

func companyPlaybookHints(tasks []CompanyOverviewTask, assets *db.AssetStore) []db.PlaybookMatch {
	out := []db.PlaybookMatch{}
	seen := map[int64]bool{}
	for _, task := range tasks {
		id, err := strconv.ParseInt(task.ID, 10, 64)
		if err != nil {
			continue
		}
		matches, err := assets.MatchPlaybooksForTask(id, 3)
		if err != nil {
			continue
		}
		for _, match := range matches {
			if seen[match.ID] {
				continue
			}
			seen[match.ID] = true
			out = append(out, match)
			if len(out) >= 20 {
				return out
			}
		}
	}
	return out
}
