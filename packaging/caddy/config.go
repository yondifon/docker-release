//go:build bundled_caddy

package caddy

var Config = []byte(`{
    admin 127.0.0.1:2019
    auto_https disable_redirects
    skip_install_trust
    grace_period 5s
    servers {
        timeouts {
            read_header 5s
        }
    }
}

import /shared/caddy-config/*.caddy

:80 {
    log {
        output stdout
        format json
    }
    handle /health {
        respond "ok" 200
    }
    import /shared/caddy-config/*.location
    handle {
        respond 404
    }
}

:443 {
    log {
        output stdout
        format json
    }
    respond 404
}
`)
