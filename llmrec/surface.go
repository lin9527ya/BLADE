package llmrec

import (
	"context"
	"github.com/lin9527ya/blade/db"
	"sync"
)

type surfaceKey struct{}
type SurfaceCapture struct {
	mu      sync.Mutex
	version string
	value   db.RequestSurface
}

func NewSurfaceCapture(ctx context.Context, version string) (context.Context, *SurfaceCapture) {
	c := &SurfaceCapture{version: version}
	return context.WithValue(ctx, surfaceKey{}, c), c
}
func SurfaceFrom(ctx context.Context) *SurfaceCapture {
	c, _ := ctx.Value(surfaceKey{}).(*SurfaceCapture)
	return c
}
func (c *SurfaceCapture) Set(value db.RequestSurface) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := c.value.Changed
	if c.value.Observed && value.Observed {
		changed = changed || c.value.Model != value.Model || c.value.SystemSHA != value.SystemSHA || c.value.ToolsSHA != value.ToolsSHA || c.value.ParametersSHA != value.ParametersSHA || c.value.EndpointSHA != value.EndpointSHA
	}
	value.Attempts = c.value.Attempts + 1
	value.BuildVersion = c.version
	value.Changed = changed
	c.value = value
}
func (c *SurfaceCapture) Snapshot() *db.RequestSurface {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.value
	v.BuildVersion = c.version
	return &v
}
