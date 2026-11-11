package sanitize

import "context"

// Check runs the verifier and returns the failing tags.
func (r *Runner) Check(ctx context.Context, path string) ([]string, error) {
	_, outcome := r.verify(ctx, path)
	switch o := outcome.(type) {
	case nil:
		return nil, nil
	case Rejected:
		return o.Tags, nil
	default:
		return nil, outcome.(error)
	}
}

func (r *Runner) Exif(ctx context.Context, args ...string) ([]byte, error) {
	return r.exif(ctx, args...)
}
