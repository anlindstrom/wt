package wtsync

// BulletUnion resolves a Markdown bullet list both sides only add to.
type BulletUnion struct{}

// Name identifies this strategy in errors and reports.
func (BulletUnion) Name() string { return "bullet-union" }

// Resolve is not written yet.
func (BulletUnion) Resolve(c Conflict) ([]byte, error) {
	return nil, Refuse(c.Path, "not written")
}
