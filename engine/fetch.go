package engine

import (
	"io"

	"github.com/gmgigi96/cernbox-sync/webdav"
)

// FetchFile streams a single remote file by relative path. The returned
// ReadCloser must be closed by the caller.
//
// Used to hydrate a placeholder on demand — typically called from a Cloud
// Files API callback on Windows. The DB is intentionally not updated here:
// the caller owns the platform-specific success/failure semantics of the
// hydration and decides when to persist the new state.
//
// The download is rate-limited via cfg.DownloadLimiter when set.
func FetchFile(cfg Config, relPath string) (io.ReadCloser, error) {
	wdc := webdav.NewClient(cfg.RemoteBase, cfg.Username, cfg.Password)
	rc, err := wdc.Get(relPath)
	if err != nil {
		return nil, err
	}
	if cfg.DownloadLimiter == nil {
		return rc, nil
	}
	return readerCloser{
		Reader: newRateLimitedReader(rc, cfg.DownloadLimiter, nil),
		closer: rc,
	}, nil
}

// readerCloser pairs an io.Reader (the rate-limited wrapper) with the
// underlying ReadCloser so the caller can still Close() the source.
type readerCloser struct {
	io.Reader
	closer io.Closer
}

func (r readerCloser) Close() error { return r.closer.Close() }
