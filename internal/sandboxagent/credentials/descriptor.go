package credentials

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// Descriptor is one parsed credential request: git's own git-credential(1)
// protocol writes a series of `key=value\n` lines to a helper's stdin,
// terminated by a blank line or EOF, for ALL THREE ops (get/store/erase).
// This Step only needs protocol/host/path.
type Descriptor struct {
	Protocol string
	Host     string
	Path     string
}

// ParseDescriptor reads git's key=value stdin format from r until a blank
// line or EOF. Known keys: protocol, host, path. If git sends a full
// `url=...` line instead of (or in addition to) separate protocol/host
// lines, and protocol/host themselves are absent, the URL is resolved (via
// net/url) into Protocol/Host/Path instead.
//
// A line that is not of the form key=value is a malformed stream and
// returns an error -- git's own protocol never emits one, so this is a
// genuine parse failure, not a line to silently skip.
func ParseDescriptor(r io.Reader) (Descriptor, error) {
	var d Descriptor
	var rawURL string

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Descriptor{}, fmt.Errorf("credentials: malformed descriptor line %q: want key=value", line)
		}

		switch key {
		case "protocol":
			d.Protocol = value
		case "host":
			d.Host = value
		case "path":
			d.Path = value
		case "url":
			rawURL = value
		}
	}
	if err := scanner.Err(); err != nil {
		return Descriptor{}, fmt.Errorf("credentials: read descriptor: %w", err)
	}

	if rawURL != "" && (d.Protocol == "" || d.Host == "") {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return Descriptor{}, fmt.Errorf("credentials: parse url=%q: %w", rawURL, err)
		}
		if d.Protocol == "" {
			d.Protocol = parsed.Scheme
		}
		if d.Host == "" {
			d.Host = parsed.Host
		}
		if d.Path == "" {
			d.Path = strings.TrimPrefix(parsed.Path, "/")
		}
	}

	return d, nil
}
