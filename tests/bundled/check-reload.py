import argparse
import sys

sys.dont_write_bytecode = True

from bundle_checks import Bundle, check_releases, check_routing


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("proxy", choices=["nginx", "angie"], nargs="?", default="nginx")
    bundle = Bundle(parser.parse_args().proxy)
    check_routing(bundle)
    check_releases(bundle)


if __name__ == "__main__":
    main()
