package db

import (
	"net/url"
	"strings"
)

func writerProtocolDSN(dsn string) string {
	if strings.Contains(dsn, "://") {
		if u, err := url.Parse(dsn); err == nil {
			q := u.Query()
			q.Set("blade.writer_protocol", "2")
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	return dsn + " blade.writer_protocol=2"
}
