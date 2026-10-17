package scan

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// mdls prints attributes in sorted order regardless of flag order, so keep this sorted.
var mdAttrs = []string{
	"kMDItemContentType",
	"kMDItemContentTypeTree",
	"kMDItemIsScreenCapture",
	"kMDItemWhereFroms",
}

const (
	mdlsBatch  = 50
	nullMarker = "<mac-cleaner-null>" // must not look like an array or a quoted string
)

// readMeta asks Spotlight for metadata in batches. Files Spotlight knows nothing
// about get an empty Meta and are classified by extension later.
func readMeta(ctx context.Context, paths []string, onDone func(done int)) []Meta {
	out := make([]Meta, len(paths))
	for start := 0; start < len(paths); start += mdlsBatch {
		end := min(start+mdlsBatch, len(paths))
		metas, err := mdls(ctx, paths[start:end])
		if err != nil {
			// One odd file can break a whole batch; retry them one by one.
			for i, p := range paths[start:end] {
				if m, err := mdls(ctx, []string{p}); err == nil {
					out[start+i] = m[0]
				}
			}
		} else {
			copy(out[start:], metas)
		}
		onDone(end)
	}
	return out
}

func mdls(ctx context.Context, paths []string) ([]Meta, error) {
	args := []string{"-raw", "-nullMarker", nullMarker}
	for _, a := range mdAttrs {
		args = append(args, "-name", a)
	}
	args = append(args, paths...) // always absolute, so never mistaken for flags
	raw, err := exec.CommandContext(ctx, "mdls", args...).Output()
	if err != nil {
		return nil, err
	}
	vals := strings.Split(string(raw), "\x00")
	if len(vals) != len(paths)*len(mdAttrs) {
		return nil, fmt.Errorf("mdls: %d değer bekleniyordu, %d geldi", len(paths)*len(mdAttrs), len(vals))
	}
	metas := make([]Meta, len(paths))
	for i := range paths {
		v := vals[i*len(mdAttrs):]
		metas[i] = Meta{
			ContentType:   scalar(v[0]),
			TypeTree:      array(v[1]),
			ScreenCapture: scalar(v[2]) == "1",
			WhereFroms:    array(v[3]),
		}
	}
	return metas, nil
}

func scalar(s string) string {
	s = strings.TrimSpace(s)
	if s == nullMarker {
		return ""
	}
	return s
}

// array parses mdls's NSArray description:
//
//	(
//	    "public.data",
//	    "public.item"
//	)
func array(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, ")") {
		return nil
	}
	var out []string
	for _, line := range strings.Split(s[1:len(s)-1], "\n") {
		line = strings.TrimSuffix(strings.TrimSpace(line), ",")
		if line != "" {
			out = append(out, unquote(line))
		}
	}
	return out
}

// unquote handles NSString description escapes, including \UXXXX for non-ASCII.
func unquote(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	s = s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'U', 'u':
			if i+4 < len(s) {
				if r, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(r))
					i += 4
					continue
				}
			}
			b.WriteByte(s[i])
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
