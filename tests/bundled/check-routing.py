import argparse
import ssl
import sys

sys.dont_write_bytecode = True

from bundle_checks import Bundle, check_routing


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("proxy", choices=["nginx", "angie", "caddy", "haproxy", "traefik"])
    parser.add_argument("--tls-port", type=int)
    parser.add_argument("--ca")
    args = parser.parse_args()
    if args.tls_port and not args.ca:
        parser.error("--tls-port needs --ca with the test certificate")
    context = ssl.create_default_context(cafile=args.ca) if args.tls_port else None
    bundle = Bundle(args.proxy, context, args.tls_port)
    check_routing(bundle, tls_routes=bundle.routes[:1], redirect=bool(args.tls_port))


if __name__ == "__main__":
    main()
