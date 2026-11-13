#!/usr/bin/env python3
"""Read-only HTTP smoke checks for the deployed same-origin application."""
import argparse
import json
from urllib.parse import urlparse, parse_qs
import urllib.error
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("url")
    args = parser.parse_args()
    base = args.url.rstrip("/")
    if urlparse(base).scheme != "https":
        raise SystemExit("HTTPS deployment URL required")
    opener = urllib.request.build_opener(NoRedirect)
    results = []

    def get(path):
        try:
            response = opener.open(base + path, timeout=25)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.code, response.headers, response.read(2 << 20)

    for path in ("/", "/chat", "/dashboard", "/settings"):
        code, headers, body = get(path)
        assert code == 200 and b'<!doctype html' in body.lower(), f"SPA route failed: {path} ({code})"
        results.append({"check": "spa" + path, "status": code})
    code, headers, body = get("/api/readyz")
    assert code == 200 and json.loads(body).get("ready") is True, f"Dependencies not ready ({code})"
    results.append({"check": "mysql_and_redis_readiness", "status": code})
    for path in ("/api/me", "/api/users/1/profile"):
        code, headers, body = get(path)
        assert code == 401 and "json" in headers.get("Content-Type", ""), f"API auth/SPA boundary failed: {path} ({code})"
        results.append({"check": "unauthenticated" + path, "status": code})
    code, headers, body = get("/api/auth/login?redirect=/chat")
    target = urlparse(headers.get("Location", ""))
    query = parse_qs(target.query)
    assert code in (302, 303, 307) and target.scheme == "https" and target.hostname.endswith(".amazoncognito.com"), "Cognito redirect unavailable"
    assert query.get("redirect_uri") == [base + "/api/auth/callback"], "Cognito callback does not match the public site"
    assert query.get("response_type") == ["code"], "Cognito code flow unavailable"
    results.append({"check": "real_cognito_authorization_redirect", "status": code})
    print(json.dumps({"url": base, "checks": results, "result": "passed"}, indent=2))


if __name__ == "__main__":
    main()
