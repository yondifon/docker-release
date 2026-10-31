import argparse
import http.client
import json
import ssl


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("proxy", choices=["nginx", "angie"])
    parser.add_argument("--tls-port", type=int)
    parser.add_argument("--ca")
    args = parser.parse_args()
    if args.tls_port and not args.ca:
        parser.error("--tls-port needs --ca with the test certificate")
    port = 8089 if args.proxy == "nginx" else 8090
    results = {}
    for host, path in [("linear", "linear"), ("canary", "canary"), ("bluegreen", "bluegreen")]:
        connection = http.client.HTTPConnection("127.0.0.1", port, timeout=3)
        try:
            connection.request("GET", f"/{path}/", headers={"Host": f"{host}.localhost"})
            response = connection.getresponse()
            response.read()
            results[host] = response.status
            expected = 308 if args.tls_port and host == "linear" else 200
            assert response.status == expected, (host, response.status)
            if expected == 308:
                assert response.getheader("Location") == "https://linear.localhost/linear/"
        finally:
            connection.close()
    if args.tls_port:
        context = ssl.create_default_context(cafile=args.ca)
        connection = http.client.HTTPSConnection("127.0.0.1", args.tls_port, timeout=3, context=context)
        try:
            connection.request("GET", "/linear/", headers={"Host": "linear.localhost"})
            response = connection.getresponse()
            body = response.read()
            assert response.status == 200 and b"Name: linear" in body, (response.status, body)
            results["tls"] = response.status
        finally:
            connection.close()
    print(json.dumps(results))


if __name__ == "__main__":
    main()
