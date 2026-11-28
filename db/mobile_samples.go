package db

import (
	"encoding/json"
	"path/filepath"
	"slices"

	"github.com/lin9527ya/blade/mobile"
)

// AppendTaskMobileSamples preserves the registration order and the primary
// identity when uploads to a task overlap. Metadata belongs to paths[0].
func (d *DB) AppendTaskMobileSamples(id int64, workspace string, paths []string, meta *mobile.PackageMetadata) (TaskMobileConfig, error) {
	c := TaskMobileConfig{TaskID: id, AppMode: "mobile"}
	tx, e := d.Begin()
	if e != nil {
		return c, e
	}
	defer tx.Rollback()
	var prev []byte
	e = tx.QueryRow(`SELECT app_sample_paths,app_manifest_json,app_platform,app_package_name FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&prev, &c.AppManifestJSON, &c.AppPlatform, &c.AppPackageName)
	if e != nil {
		return c, e
	}
	var saved []string
	if e = json.Unmarshal(prev, &saved); e != nil {
		return c, e
	}
	for _, p := range append(saved, paths...) {
		if filepath.IsAbs(p) {
			if rel, err := filepath.Rel(workspace, p); err == nil && filepath.IsLocal(rel) {
				p = filepath.ToSlash(rel)
			}
		}
		if !slices.Contains(c.AppSamplePaths, p) {
			c.AppSamplePaths = append(c.AppSamplePaths, p)
		}
	}
	if len(c.AppSamplePaths) > 0 && meta != nil && meta.SamplePath == c.AppSamplePaths[0] {
		c.AppManifestJSON, e = json.Marshal(meta)
		if e != nil {
			return c, e
		}
		c.AppPlatform = string(meta.Platform)
		c.AppPackageName = meta.PackageName
	}
	raw, e := json.Marshal(c.AppSamplePaths)
	if e != nil {
		return c, e
	}
	_, e = tx.Exec(`UPDATE tasks SET app_mode='mobile',app_sample_paths=$2::jsonb,app_manifest_json=$3::jsonb,app_platform=$4,app_package_name=$5 WHERE id=$1`, id, string(raw), string(c.AppManifestJSON), c.AppPlatform, c.AppPackageName)
	if e != nil {
		return c, e
	}
	return c, tx.Commit()
}
