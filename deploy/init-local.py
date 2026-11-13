"""Generate local-only secrets without printing them or overwriting an .env."""
import base64
import secrets
from pathlib import Path

root = Path(__file__).resolve().parent.parent
values = {
    "MYSQL_ROOT_PASSWD": secrets.token_hex(24),
    "MYSQL_UNICHATGO_PASSWD": secrets.token_hex(24),
    "REDIS_PASSWD": secrets.token_hex(24),
    "UNICHATGO_APIKEY_KEY": base64.b64encode(secrets.token_bytes(32)).decode(),
    "ADMIN_TOKEN": secrets.token_hex(32),
}
lines = (root / ".env.example").read_text(encoding="utf-8").splitlines()
output = []
for line in lines:
    key = line.partition("=")[0]
    output.append(f"{key}={values[key]}" if key in values else line)
try:
    with (root / ".env").open("x", encoding="utf-8", newline="\n") as handle:
        handle.write("\n".join(output) + "\n")
except FileExistsError:
    raise SystemExit(".env already exists; left unchanged.")
print("Created local .env. Secret values were not printed.")
