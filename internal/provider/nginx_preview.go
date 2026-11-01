package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// The preview link needs a few `map` blocks in the `http` context. They live in the
// service's upstream file. Their variable names carry a short hash of the service
// name, so two services never share one.
//
// `<v>` stands for that prefix:
//   - `$<v>`      is `1` when the request carries the token (cookie or header).
//   - `$<v>_link` is the redirect target when the URL carries the token, else empty.
//   - `$<v>_set`  is the `Set-Cookie` value for the link.
//
// Without a live token every map has only its default, so nothing matches.

// previewLinkVars is the variable name prefix for a service.
func previewLinkVars(service string) string {
	sum := sha256.Sum256([]byte(service))
	return "dr_pv_" + hex.EncodeToString(sum[:])[:10]
}

// previewLinkMaps renders the maps. `token` is empty while no preview is live.
func previewLinkMaps(service, token string) string {
	v := previewLinkVars(service)
	// Token keys are regular expressions, so a long token never hits the
	// `map_hash_bucket_size` limit that exact keys have.
	match := func(source, name string) string {
		var b strings.Builder
		fmt.Fprintf(&b, "map %s $%s_%s {\n    default 0;\n", source, v, name)
		if token != "" {
			fmt.Fprintf(&b, "    \"~^%s$\" 1;\n", token)
		}
		b.WriteString("}\n")
		return b.String()
	}

	var b strings.Builder
	b.WriteString("\n# Preview link: send requests that carry the preview token to the preview servers.\n")
	b.WriteString(match("$http_x_release_preview", "header"))
	b.WriteString(match("$cookie_release_preview", "cookie"))
	fmt.Fprintf(&b, "map \"$%s_header$%s_cookie\" $%s {\n    default 1;\n    \"00\" 0;\n}\n", v, v, v)

	// The link target is the request URI without the `release-preview` parameter.
	// The path part must not start with `//` or `/\`, so the redirect stays on this host.
	fmt.Fprintf(&b, "map $request_uri $%s_link {\n    default \"\";\n", v)
	if token != "" {
		const path = `/(?:[^/\x5c?][^?]*)?`
		param := "release-preview=" + token
		// Only the parameter, the parameter first, last, or in the middle.
		fmt.Fprintf(&b, "    \"~^(%s)\\?%s&?$\" \"$1\";\n", path, param)
		fmt.Fprintf(&b, "    \"~^(%s)\\?%s&(.+)$\" \"$1?$2\";\n", path, param)
		fmt.Fprintf(&b, "    \"~^(%s\\?.+)&%s&?$\" \"$1\";\n", path, param)
		fmt.Fprintf(&b, "    \"~^(%s\\?.+)&%s&(.+)$\" \"$1&$2\";\n", path, param)
	}
	b.WriteString("}\n")

	// The cookie the link sets.
	cookie := ""
	if token != "" {
		cookie = "release_preview=" + token + "; Path=/; HttpOnly; SameSite=Lax"
	}
	fmt.Fprintf(&b, "map $%s_link $%s_set {\n    default \"%s\";\n}\n", v, v, cookie)
	return b.String()
}

// writePreviewLinkRules writes the rules every route location gets when the route has
// a preview link, ahead of the proxy rules. `secure` adds `Secure` to the cookie.
func writePreviewLinkRules(b *strings.Builder, service string, secure, previewHostOnly bool) {
	v := previewLinkVars(service)
	cookie := "$" + v + "_set"
	if secure {
		cookie = `"` + cookie + `; Secure"`
	} else {
		cookie = `"` + cookie + `"`
	}
	fmt.Fprintf(b, "    if ($%s_link) {\n", v)
	fmt.Fprintf(b, "        access_log off;\n")
	fmt.Fprintf(b, "        add_header Set-Cookie %s always;\n", cookie)
	fmt.Fprintf(b, "        return 302 $%s_link;\n", v)
	fmt.Fprintf(b, "    }\n")
	if previewHostOnly {
		fmt.Fprintf(b, "    if ($%s = 0) {\n", v)
		fmt.Fprintf(b, "        return 404;\n")
		fmt.Fprintf(b, "    }\n")
	}
}

// previewLocationPrefix starts the internal locations that proxy to the preview servers.
const previewLocationPrefix = "/.dr-preview"
